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

// TestExitTriggeredOverRead_Reachability is a forensic reachability audit (Epic B
// loose thread): the settlement MATH for an over-read is proven correct
// elsewhere, but can an operator actually reach that end-state through the real
// meter/move-out entry methods when the over-read is discovered AT move-out (same
// month)? This drives the real service methods — no seeding of the terminal state
// the settlement math tests used.
//
// Verdict (see the three subtests): the same-month exit-triggered over-read is
// UNREACHABLE — both operator orderings hit a proven invariant collision. The
// only reachable path is a recovery caught in an EARLIER cycle, then a move-out
// in a LATER month (which is exactly what the Epic B resolver handles).

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

func TestExitTriggeredOverRead_Reachability(t *testing.T) {
	// PATH A — operator enters the low physical reading directly as the EXIT.
	// The last recorded reading is the mis-read high value, so current < previous
	// → the domain rejects it (forward breakage). Blocked.
	t.Run("SameMonth_ExitFirst_ForwardBreakageRejected", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-A")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		// Latest reading = the mis-read high value (elec 300 / water 80).
		seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -1, 0).Format("2006-01"))

		svc := buildMeterSvc(t, db)
		// Operator reads the true (lower) physical at move-out: 250 < 300.
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 250, 70)
		if err == nil {
			t.Fatal("expected forward-breakage rejection recording exit 250 under previous 300, got nil")
		}
		if exitCount(t, db, rm.ID.String()) != 0 {
			t.Fatal("no EXIT reading should have been created")
		}
	})

	// PATH B — operator first fixes the reading via a Reading Recovery (re-anchor
	// to physical), then records the exit. The recovery is a MONTHLY row with
	// billing_month = now (Lock E), and CreateExitForMoveOut rejects when a
	// MONTHLY row already exists for the exit's month (HasMonthlyByRoomAndMonth
	// counts anchor rows). Same month → collision. Blocked.
	t.Run("SameMonth_RecoveryFirst_HasMonthlyCollision", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-B")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -1, 0).Format("2006-01"))

		svc := buildMeterSvc(t, db)
		// Re-anchor to physical (250) — recovery lands as MONTHLY, billing_month = now.
		if _, err := svc.CreateBaselineCorrection(ctx, recoveryInput(source.ID)); err != nil {
			t.Fatalf("CreateBaselineCorrection (re-anchor): %v", err)
		}
		// Now record the exit in the same month → HasMonthly collision.
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 250, 70)
		if err == nil {
			t.Fatal("expected same-month HasMonthly collision after recovery, got nil")
		}
		if exitCount(t, db, rm.ID.String()) != 0 {
			t.Fatal("no EXIT reading should have been created")
		}
	})

	// REACHABLE CONTRAST — the over-read was caught in an EARLIER cycle (recovery
	// row for a prior month), and the move-out happens in a LATER month. The exit
	// month is after the recovery's month, so HasMonthly does not collide and the
	// exit chains off the corrected baseline. This is the path Epic B's resolver
	// refunds at settlement.
	t.Run("PriorCycleRecovery_ExitLater_Reachable", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REACH-C")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)

		// A recovery caught in the PRIOR cycle: re-anchored to physical 250,
		// records the mis-read 300. (Seeded directly to model a past cycle — Lock E
		// forces a live recovery's billing_month to now.)
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
		// Move-out this month: exit 260 chains off the re-anchor (250) → valid.
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 260, 80); err != nil {
			t.Fatalf("exit in a later month should be reachable, got: %v", err)
		}
		if exitCount(t, db, rm.ID.String()) != 1 {
			t.Fatal("EXIT reading should have been created")
		}
	})
}
