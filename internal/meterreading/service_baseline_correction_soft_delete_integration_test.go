//go:build integration

package meterreading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestBaselineCorrection_SoftDeleteEditViaRecreate is the Phase 7 B9
// anchor: operator typo recovery via Soft Delete + Recreate. Locks two
// independent doctrine lines:
//
//  1. Pending correction can be soft-deleted (Edit-via-Delete pattern).
//     A second commit against the same source then succeeds (the partial
//     unique index sees only one live anchor).
//  2. Applied correction CANNOT be soft-deleted — the operator must reverse
//     via the bill correction flow instead. ErrCorrectionAlreadyApplied
//     surfaces with the typed code BASELINE_CORRECTION_ALREADY_APPLIED.
//
// Doctrine: feedback_reading_recovery_doctrine.md (Phase 7 doctrine line
// 193 — Edit-via-Delete replaces second-recovery-row pattern; line 196 —
// immutable post-apply). Locked 2026-06-25.
// Plan:     /Users/anantakit/.claude/plans/smooth-coalescing-flute.md (B9).
func TestBaselineCorrection_SoftDeleteEditViaRecreate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B9-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billSvc, _ := buildPhase7Services(t, db)

	// Source: past misread.
	srcMonth := time.Now().AddDate(0, -3, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  500,
		WaterPrevious:       40,
		WaterCurrent:        80,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// First commit with WRONG values (operator's typo).
	first, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 999, // typo
		WaterCurrent:       65,
		AnchorNote:         "B9 — typo round",
	})
	if err != nil {
		t.Fatalf("first CreateBaselineCorrection: %v", err)
	}

	// Soft-delete the typo'd row (PENDING, latest, owned).
	if err := meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, first.ID, nil); err != nil {
		t.Fatalf("SoftDeletePendingBaselineCorrection (typo): %v", err)
	}

	// Re-commit with correct values — second commit against the same source
	// must succeed because the first row is gone.
	second, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 380, // correct
		WaterCurrent:       65,
		AnchorNote:         "B9 — recreated after delete",
	})
	if err != nil {
		t.Fatalf("second CreateBaselineCorrection: %v", err)
	}
	if second.ElectricityCurrent != 380 {
		t.Errorf("second.ElectricityCurrent = %d, want 380", second.ElectricityCurrent)
	}

	// Sanity check: pending list returns the recreated row (only).
	pending, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom: %v", err)
	}
	if len(pending) != 1 || pending[0].RecoveryID != second.ID {
		t.Fatalf("pending = %+v, want [%v]", pending, second.ID)
	}

	// Apply via UpdateMonthlyDraft to flip the recreated row to APPLIED.
	currentMonth := time.Now().Format("2006-01")
	draft := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: currentMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed DRAFT: %v", err)
	}
	if _, err := billSvc.UpdateMonthlyDraft(ctx, draft.ID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{{
			RecoveryReadingID: second.ID.String(),
			Amount:            -400,
			AdjustmentNote:    "B9 — apply the recreated correction",
		}},
	}, nil); err != nil {
		t.Fatalf("UpdateMonthlyDraft: %v", err)
	}

	// Soft delete on the APPLIED row must now reject with the typed error.
	err = meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, second.ID, nil)
	if !errors.Is(err, meterreading.ErrCorrectionAlreadyApplied) {
		t.Fatalf("SoftDelete on applied = %v, want ErrCorrectionAlreadyApplied", err)
	}
}
