//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestBaselineCorrection_CommitBeforeDraft_ApplyAtBillEdit is the Phase 7
// B7 anchor: the operator commits a baseline correction BEFORE the monthly
// DRAFT bill exists, and the Adjustment Application happens later at bill
// edit time. Locks the "no RECOVERY_NO_DRAFT_BILL ordering" doctrine.
//
// Sequence:
//  1. Commit correction with no DRAFT bill present → succeeds (meter-only).
//  2. Seed DRAFT bill manually (proxy for monthly batch creating the bill).
//  3. ListPendingBaselineCorrectionsByRoom returns the pending row.
//  4. UpdateMonthlyDraft with applied_corrections inserts ADJUSTMENT line
//     + emits APPLY_RECOVERY_ADJUSTMENT audit row.
//  5. ListPendingBaselineCorrectionsByRoom now returns empty (derived from
//     inverse-FK presence on the non-VOID line item).
//
// Doctrine: feedback_reading_recovery_doctrine.md (Phase 7 — Split Meter
// Truth from Financial Truth, locked 2026-06-25).
// Plan:     /Users/anantakit/.claude/plans/smooth-coalescing-flute.md (B7).
func TestBaselineCorrection_CommitBeforeDraft_ApplyAtBillEdit(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B7-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billSvc, _ := buildPhase7Services(t, db)

	// Step 1: source meter (past misread) + commit baseline correction
	// with NO DRAFT bill present.
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
		t.Fatalf("seed source meter: %v", err)
	}

	correction, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 380,
		WaterCurrent:       65,
		AnchorNote:         "B7 — commit before DRAFT exists",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Step 2: seed the current-month DRAFT bill (proxy for monthly batch).
	currentMonth := time.Now().Format("2006-01")
	draft := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: currentMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed DRAFT bill: %v", err)
	}

	// Step 3: pending list returns the correction.
	pending, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom: %v", err)
	}
	if len(pending) != 1 || pending[0].RecoveryID != correction.ID {
		t.Fatalf("pending list = %+v, want [%v]", pending, correction.ID)
	}

	// Step 4: apply via UpdateMonthlyDraft.
	note := "B7 — operator applies adjustment in bill context"
	updated, err := billSvc.UpdateMonthlyDraft(ctx, draft.ID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{{
			RecoveryReadingID: correction.ID.String(),
			Amount:            -640, // refund 640 baht
			AdjustmentNote:    note,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateMonthlyDraft: %v", err)
	}

	var adjLines int64
	if err := db.Table("bill_line_items").
		Where("bill_id = ? AND line_type = ? AND adjustment_recovery_reading_id = ?",
			draft.ID, billing.LineItemAdjustment, correction.ID).
		Count(&adjLines).Error; err != nil {
		t.Fatalf("count adjustment lines: %v", err)
	}
	if adjLines != 1 {
		t.Errorf("ADJUSTMENT lines = %d, want 1", adjLines)
	}

	var auditRows int64
	if err := db.Table("bill_audit_log").
		Where("bill_id = ? AND action = ?", draft.ID, billing.AuditApplyRecoveryAdjustment).
		Count(&auditRows).Error; err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditRows != 1 {
		t.Errorf("APPLY_RECOVERY_ADJUSTMENT audit rows = %d, want 1", auditRows)
	}

	if updated.TotalAmount != -64000 {
		t.Errorf("bill total after adjustment = %d satang, want -64000", updated.TotalAmount)
	}

	// Step 5: pending list now empty (applied state derived from FK presence).
	pendingAfter, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom (after): %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Errorf("pending list after apply = %d items, want 0", len(pendingAfter))
	}
}

// buildPhase7Services wires the meterreading + billing services for Phase
// 7 integration anchors. Mirrors cmd/main.go's wiring shape minus the
// HTTP layer.
func buildPhase7Services(t *testing.T, db *gorm.DB) (meterreading.MeterReadingService, billing.BillingService, uuid.UUID) {
	t.Helper()
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billConfigRepo := billingconfig.NewBillingConfigRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)
	billSvc := billing.NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, billConfigRepo, nil, txMgr)
	return meterSvc, billSvc, uuid.Nil
}
