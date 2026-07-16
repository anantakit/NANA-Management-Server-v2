//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"gorm.io/gorm"
)

// TestExitTriggeredOverRead_Reachability tracks the exit-triggered over-read
// reachability across the F2 fix. The original audit proved the same-month
// Recovery+Exit collision; F2 narrowed the exit-uniqueness guard to CONSUMPTION
// rows (HasConsumptionMonthlyByRoomAndMonth, aligned with migration 00041), so a
// recovery anchor no longer blocks a same-month exit. This test now pins the
// POST-F2 behavior:
//   - exit-first (no flags) is still rejected — forward breakage; you must
//     re-anchor first (unchanged by F2; a legitimate rollover/replacement flag is
//     F1's concern and does not model an over-read anyway);
//   - recovery-first → exit in the SAME month now COEXISTS (F2);
//   - the cross-cycle path (recovery earlier, move-out later) remains reachable.
func TestExitTriggeredOverRead_Reachability(t *testing.T) {
	// PATH A — operator enters the low physical reading directly as the EXIT with
	// no meter-hardware flag. current < previous → the default validation rejects
	// it. The operator must re-anchor (recovery) first. (F1 adds rollover/replaced
	// capability, but neither models a mis-record — that is a recovery.)
	t.Run("SameMonth_ExitFirst_ForwardBreakageRejected", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-A")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -1, 0).Format("2006-01"))

		svc := buildMeterSvc(t, db)
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 250, 70, false, false, false, false, false, false)
		if err == nil {
			t.Fatal("expected forward-breakage rejection recording exit 250 under previous 300, got nil")
		}
		if exitCount(t, db, rm.ID.String()) != 0 {
			t.Fatal("no EXIT reading should have been created")
		}
	})

	// PATH B (POST-F2) — operator re-anchors via a Reading Recovery (MONTHLY
	// anchor, billing_month = now), then records the exit in the same month. The
	// exit-uniqueness guard now counts CONSUMPTION rows only, so the recovery
	// anchor no longer collides — the exit COEXISTS with it, chaining off the
	// corrected baseline.
	t.Run("SameMonth_RecoveryFirst_NowCoexists", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-B")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -1, 0).Format("2006-01"))

		svc := buildMeterSvc(t, db)
		if _, err := svc.CreateBaselineCorrection(ctx, recoveryInput(source.ID)); err != nil {
			t.Fatalf("CreateBaselineCorrection (re-anchor): %v", err)
		}
		// Exit re-anchors to physical (250 == recovery current) → usage 0, valid.
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 250, 70, false, false, false, false, false, false); err != nil {
			t.Fatalf("same-month exit after recovery should now coexist (F2), got: %v", err)
		}
		if exitCount(t, db, rm.ID.String()) != 1 {
			t.Fatal("EXIT reading should have been created alongside the recovery anchor")
		}
	})

	// CROSS-CYCLE — recovery caught in an earlier cycle, move-out in a later month.
	// Always reachable; unchanged by F2.
	t.Run("PriorCycleRecovery_ExitLater_Reachable", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-C")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)

		priorMonth := time.Now().AddDate(0, -1, 0).Format("2006-01")
		reason := meterreading.AnchorReasonReadingRecovery
		note := "recovery caught last cycle"
		recorded := 300
		rec := &meterreading.MeterReading{
			RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &priorMonth,
			ElectricityPrevious: 250, ElectricityCurrent: 250, WaterPrevious: 70, WaterCurrent: 70,
			ElectricityRecorded: &recorded, AnchorReason: &reason, AnchorNote: &note,
		}
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("seed prior-cycle recovery: %v", err)
		}

		svc := buildMeterSvc(t, db)
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 260, 80, false, false, false, false, false, false); err != nil {
			t.Fatalf("exit in a later month should be reachable, got: %v", err)
		}
		if exitCount(t, db, rm.ID.String()) != 1 {
			t.Fatal("EXIT reading should have been created")
		}
	})
}

func exitCount(t *testing.T, db *gorm.DB, roomID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND reading_type = ? AND deleted_at IS NULL", roomID, meterreading.ReadingTypeExit).
		Count(&n).Error; err != nil {
		t.Fatalf("count exit: %v", err)
	}
	return n
}
