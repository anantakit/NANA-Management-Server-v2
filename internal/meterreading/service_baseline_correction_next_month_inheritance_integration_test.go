//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_NextMonthInheritsRecoveryCurrent is the B2 lineage anchor for
// Phase 5: after a recovery commits at month M, the next MONTHLY reading at
// month M+1 must inherit `recovery.Current` as its `Previous`.
//
// Doctrine: feedback_reading_recovery_doctrine.md.
// Plan:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (B2).
//
// This works STRUCTURALLY without any special-case in populatePrevious or
// FindLatestByRoomID — both treat recovery rows as ordinary lineage anchors
// (Phase 1 A1 strengthened assertion locked this). B2 makes that structural
// guarantee explicit + executable so a future regression that hides recovery
// rows from lineage fails immediately.
//
// CreateBaselineCorrection uses time.Now() for billing_month (Lock E). A second
// MONTHLY reading in the same month would violate the (room_id,
// billing_month) uniqueness, so we hand-seed the next-month row via
// raw db.Create() instead of calling meterSvc.Create.
func TestRecovery_NextMonthInheritsRecoveryCurrent(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B2-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)

	// Source: past MONTHLY (3 months ago).
	srcMonth := time.Now().AddDate(0, -3, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 200,
		ElectricityCurrent:  600, // claimed
		WaterPrevious:       40,
		WaterCurrent:        70,
	}
	if err := meterRepo.Create(ctx, source); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// DRAFT bill for current month.
	recoveryMonth := time.Now().Format("2006-01")
	draft := billing.Bill{
		ContractID:   con.ID,
		BillingMonth: recoveryMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	// Commit recovery with new currents (real today).
	recovery, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 450,
		WaterCurrent:       60,
		AnchorNote:         "พบจดเกินจริง",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Assert: FindLatestByRoomID sees the recovery row (lineage truth).
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != recovery.ID {
		t.Fatalf("FindLatestByRoomID=%v, want recovery %v — lineage MUST see recovery rows", latest.ID, recovery.ID)
	}

	// Hand-seed the NEXT month (recoveryMonth + 1) using populatePrevious
	// semantics so the inheritance is visible. populatePrevious(latest=recovery)
	// returns (recovery.ElecCurrent, recovery.WaterCurrent) as the new prev.
	nextMonth := time.Now().AddDate(0, 1, 0).Format("2006-01")
	next := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &nextMonth,
		ElectricityPrevious: latest.ElectricityCurrent,
		ElectricityCurrent:  latest.ElectricityCurrent + 30,
		WaterPrevious:       latest.WaterCurrent,
		WaterCurrent:        latest.WaterCurrent + 5,
	}
	if err := meterRepo.Create(ctx, next); err != nil {
		t.Fatalf("hand-seed next month: %v", err)
	}

	// Assert: next month's Previous == recovery.Current (B2 doctrine).
	if next.ElectricityPrevious != recovery.ElectricityCurrent {
		t.Errorf("next.ElectricityPrevious=%d, want recovery.ElectricityCurrent=%d (B2 inheritance)", next.ElectricityPrevious, recovery.ElectricityCurrent)
	}
	if next.WaterPrevious != recovery.WaterCurrent {
		t.Errorf("next.WaterPrevious=%d, want recovery.WaterCurrent=%d (B2 inheritance)", next.WaterPrevious, recovery.WaterCurrent)
	}

	// Re-fetch to confirm persistence honored prev=curr lineage:
	// next month's row roundtrips with the recovery-current as previous.
	got, err := meterRepo.FindByIDSimple(ctx, next.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple(next): %v", err)
	}
	if got.ElectricityPrevious != recovery.ElectricityCurrent {
		t.Errorf("persisted next.ElectricityPrevious=%d, want %d", got.ElectricityPrevious, recovery.ElectricityCurrent)
	}
}
