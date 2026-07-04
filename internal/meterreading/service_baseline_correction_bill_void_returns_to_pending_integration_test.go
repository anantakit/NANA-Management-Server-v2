//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestBaselineCorrection_BillVoidReturnsToPending is the Phase 7 B8
// anchor: when a bill carrying an applied ADJUSTMENT line is VOIDed via
// the correction flow, the recovery row returns to PENDING and can be
// re-applied to the new DRAFT bill.
//
// Doctrine: feedback_reading_recovery_doctrine.md (Phase 7 doctrine line
// 91 — "APPLIED iff EXISTS bill_line_item WHERE bill.status != VOID";
// VOID flips the derivation back to PENDING). Locked 2026-06-25.
// Plan:     /Users/anantakit/.claude/plans/smooth-coalescing-flute.md (B8).
func TestBaselineCorrection_BillVoidReturnsToPending(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B8-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billSvc, _ := buildPhase7Services(t, db)

	// Source + correction.
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
	elecRecorded := 500 // over-record: recorded 500 > physical 380
	correction, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:     &source.ID,
		ElectricityCurrent:  380,
		WaterCurrent:        65,
		ElectricityRecorded: &elecRecorded,
		AnchorNote:          "B8 — correction",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// First DRAFT bill with line items so it can be finalized (the correction
	// flow only runs on FINALIZED bills) + apply the correction.
	currentMonth := time.Now().Format("2006-01")
	firstDraft := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: currentMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
		LineItems: []billing.BillLineItem{{
			LineType:    billing.LineItemRoomRent,
			Source:      billing.LineItemSourceAuto,
			Description: "ค่าห้อง",
			Amount:      300_000,
			Quantity:    1,
			UnitPrice:   300_000,
			SortOrder:   1,
		}},
		TotalAmount: 300_000,
	}
	if err := db.Create(firstDraft).Error; err != nil {
		t.Fatalf("seed first DRAFT: %v", err)
	}
	if _, err := billSvc.UpdateMonthlyDraft(ctx, firstDraft.ID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{{
			RecoveryReadingID: correction.ID.String(),
			Utility:           "ELECTRICITY",
			Decision:          "ACCEPT",
			AdjustmentNote:    "B8 — first application",
		}},
	}, nil); err != nil {
		t.Fatalf("UpdateMonthlyDraft (first apply): %v", err)
	}

	// Confirm pending list is empty after apply.
	pending, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after apply = %d, want 0", len(pending))
	}

	// Finalize the first bill so we can run the correction flow.
	if _, err := billSvc.FinalizeBill(ctx, firstDraft.ID, nil); err != nil {
		t.Fatalf("FinalizeBill (first): %v", err)
	}

	// Trigger the void+recreate correction flow on the FINALIZED bill.
	newBill, err := billSvc.CorrectBill(ctx, firstDraft.ID, billing.CorrectBillRequest{
		CorrectionReason: "B8 — correction triggers void; recovery returns to pending",
	}, nil)
	if err != nil {
		t.Fatalf("CorrectBill: %v", err)
	}

	// VOIDed bill no longer counts as applied → recovery returns to PENDING.
	pendingAfter, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom (after void): %v", err)
	}
	if len(pendingAfter) != 1 || pendingAfter[0].RecoveryID != correction.ID {
		t.Fatalf("pending after void = %+v, want [%v]", pendingAfter, correction.ID)
	}

	// Re-apply on the new DRAFT (CorrectBill returned the replacement bill).
	if _, err := billSvc.UpdateMonthlyDraft(ctx, newBill.ID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{{
			RecoveryReadingID: correction.ID.String(),
			Utility:           "ELECTRICITY",
			Decision:          "ACCEPT",
			AdjustmentNote:    "B8 — re-applied on the new bill",
		}},
	}, nil); err != nil {
		t.Fatalf("UpdateMonthlyDraft (re-apply): %v", err)
	}

	pendingFinal, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom (after re-apply): %v", err)
	}
	if len(pendingFinal) != 0 {
		t.Errorf("pending after re-apply = %d, want 0", len(pendingFinal))
	}
}
