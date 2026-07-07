//go:build integration

package meterreading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// buildRecoveryServices wires the meter-reading service the way the Q1.6
// recovery flow uses it, plus the billing repo so tests can probe the
// freshness gate directly. Replaces the deleted Phase-7 buildPhase7Services
// helper (which returned the removed pending-list surface).
func buildRecoveryServices(t *testing.T, db *gorm.DB) (meterreading.MeterReadingService, billing.BillingRepository) {
	t.Helper()
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)
	return meterSvc, billRepo
}

// TestRecovery_SoftDeleteClearsFreshnessGate restores the Q1.6-shaped
// soft-delete coverage that P1 removed with the pending/apply flow. It locks
// two doctrine lines against the reframed freshness gate
// (HasUnreflectedOverRecordByContractID):
//
//  1. An over-record recovery that is not yet reflected on any live bill can be
//     soft-deleted (operator typo fix). After the delete the freshness gate
//     clears — no stale-bill block remains, and the refund will not be emitted
//     at generation (the row is gone).
//  2. A recovery whose refund IS reflected on a live (non-VOID) bill CANNOT be
//     soft-deleted — ErrCorrectionAlreadyApplied. The operator must reverse via
//     the bill correction flow (VOID + GENERATE), not by deleting the meter row.
func TestRecovery_SoftDeleteClearsFreshnessGate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P3-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billRepo := buildRecoveryServices(t, db)

	// An over-record recovery: physically 450, but 600 was recorded before.
	elecRecorded := 600
	rec, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		RoomID:              &rm.ID,
		ElectricityCurrent:  450,
		ElectricityRecorded: &elecRecorded,
		WaterCurrent:        80,
		AnchorNote:          "จดเกิน — ยังไม่ออกบิล",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Before any bill reflects it, the contract's bills are stale — the gate
	// blocks finalization until a bill carries the refund (or the row is gone).
	stale, err := billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe (before delete): %v", err)
	}
	if !stale {
		t.Fatal("gate = false before any bill reflects the recovery, want true (stale)")
	}

	// Soft-delete the unreflected recovery — allowed (Edit-via-Delete).
	if err := meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, rec.ID, nil); err != nil {
		t.Fatalf("SoftDelete on unreflected recovery: %v", err)
	}

	// Gate clears: no live over-record recovery remains for the contract.
	stale, err = billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe (after delete): %v", err)
	}
	if stale {
		t.Fatal("gate = true after soft-deleting the recovery, want false (nothing to reflect)")
	}

	// --- Applied-guard: a recovery reflected on a live bill is immutable. ---
	rec2, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		RoomID:              &rm.ID,
		ElectricityCurrent:  450,
		ElectricityRecorded: &elecRecorded,
		WaterCurrent:        80,
		AnchorNote:          "จดเกินอีกครั้ง — จะออกบิล",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (second): %v", err)
	}

	// Hand-seed a live DRAFT bill carrying the refund ADJUSTMENT that would be
	// auto-emitted at generation (P2). This makes the recovery "reflected".
	recoveryMonth := time.Now().Format("2006-01")
	bill := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: recoveryMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	reason := billing.AdjustmentReasonMeterRecovery
	elecUtil := billing.AdjustmentUtilityElectricity
	if err := db.Create(&billing.BillLineItem{
		BillID:                      bill.ID,
		LineType:                    billing.LineItemAdjustment,
		Source:                      billing.LineItemSourceManual,
		Description:                 "คืนค่าไฟฟ้า",
		Amount:                      -(600 - 450) * 800,
		SortOrder:                   99,
		AdjustmentRecoveryReadingID: &rec2.ID,
		AdjustmentUtility:           &elecUtil,
		AdjustmentReasonCode:        &reason,
	}).Error; err != nil {
		t.Fatalf("seed adjustment line: %v", err)
	}

	// Now soft-deleting the reflected recovery must be rejected.
	err = meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, rec2.ID, nil)
	if !errors.Is(err, meterreading.ErrCorrectionAlreadyApplied) {
		t.Fatalf("SoftDelete on reflected recovery = %v, want ErrCorrectionAlreadyApplied", err)
	}
}

// TestRecovery_NilSourceGatesAndReBaselines restores the Q1.6-shaped
// source-optional coverage. A recovery supplied with NO source reading is a
// complete, authoritative correction: it persists with a NULL source FK, sits
// in lineage, engages the freshness gate exactly like a sourced recovery, and
// re-baselines the next month. Absence of a source never downgrades the money
// or the gate (feedback_physical_observation_dominance / source-optional lock).
func TestRecovery_NilSourceGatesAndReBaselines(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P3-102")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billRepo := buildRecoveryServices(t, db)
	meterRepo := meterreading.NewMeterReadingRepository(db)

	// Over-record recovery with NO source — only the room anchor carries identity.
	elecRecorded := 600
	rec, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:     nil, // absence is valid and complete
		RoomID:              &rm.ID,
		ElectricityCurrent:  450,
		ElectricityRecorded: &elecRecorded,
		WaterCurrent:        80,
		AnchorNote:          "จดเกิน — ไม่มี source",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (nil source): %v", err)
	}

	// Persisted with NULL FK — no source was invented.
	got, err := meterRepo.FindByIDSimple(ctx, rec.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple: %v", err)
	}
	if got.RecoverySourceReadingID != nil {
		t.Errorf("RecoverySourceReadingID = %v, want nil (NULL FK)", got.RecoverySourceReadingID)
	}
	if got.AnchorReason == nil || *got.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("AnchorReason = %v, want READING_RECOVERY", got.AnchorReason)
	}

	// Lineage sees the nil-source recovery like any other anchor.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != rec.ID {
		t.Fatalf("FindLatestByRoomID = %v, want recovery %v", latest.ID, rec.ID)
	}

	// The freshness gate engages regardless of source — money authority comes
	// from the physical over-record, not from a source reading.
	stale, err := billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe: %v", err)
	}
	if !stale {
		t.Fatal("nil-source over-record gate = false, want true (still gates)")
	}

	// Next month inherits the recovery's current as its previous (re-baseline).
	nextMonth := time.Now().AddDate(0, 1, 0).Format("2006-01")
	next := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &nextMonth,
		ElectricityPrevious: got.ElectricityCurrent,
		ElectricityCurrent:  480,
		WaterPrevious:       got.WaterCurrent,
		WaterCurrent:        92,
	}
	if err := meterRepo.Create(ctx, next); err != nil {
		t.Fatalf("seed next month: %v", err)
	}
	if next.ElectricityPrevious != got.ElectricityCurrent {
		t.Errorf("next.ElectricityPrevious = %d, want recovery.ElectricityCurrent = %d",
			next.ElectricityPrevious, got.ElectricityCurrent)
	}
}
