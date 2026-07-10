//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestExitGuard_ConsumptionVsAnchor is the F2 SAFETY GATE: narrowing the
// exit-uniqueness guard to CONSUMPTION rows (anchor_reason IS NULL) must let ONLY
// recovery anchors coexist with an EXIT — it must NOT weaken the guard for
// genuine consumption readings. Proves, through the real CreateExitForMoveOut:
//   1. a recovery anchor alone → exit ALLOWED;
//   2. a consumption monthly → exit still REJECTED;
//   3. a recovery anchor does NOT mask a same-cycle consumption monthly (both
//      present → still REJECTED) — the anchor exclusion narrows the scope, it
//      does not blind the guard.
func TestExitGuard_ConsumptionVsAnchor(t *testing.T) {
	month := time.Now().Format("2006-01")

	seedConsumption := func(t *testing.T, db *gorm.DB, roomID uuid.UUID) {
		t.Helper()
		m := month
		r := &meterreading.MeterReading{
			RoomID: roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &m,
			ElectricityPrevious: 90, ElectricityCurrent: 100, WaterPrevious: 40, WaterCurrent: 50,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed consumption monthly: %v", err)
		}
	}
	seedRecoveryAnchor := func(t *testing.T, db *gorm.DB, roomID uuid.UUID) {
		t.Helper()
		m := month
		reason := meterreading.AnchorReasonReadingRecovery
		note := "re-anchor event this cycle"
		recorded := 150
		r := &meterreading.MeterReading{
			RoomID: roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &m,
			ElectricityPrevious: 100, ElectricityCurrent: 100, WaterPrevious: 50, WaterCurrent: 50,
			ElectricityRecorded: &recorded, AnchorReason: &reason, AnchorNote: &note,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed recovery anchor: %v", err)
		}
	}

	// (1) recovery anchor alone → exit allowed.
	t.Run("RecoveryAnchorAlone_AllowsExit", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "GUARD-1")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedRecoveryAnchor(t, db, rm.ID)

		svc := buildMeterSvc(t, db)
		// latest = the anchor (current 100) → exit 100 = usage 0, valid; guard sees
		// no CONSUMPTION monthly → allowed.
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 100, 50, false, false, false, false); err != nil {
			t.Fatalf("exit should be allowed to coexist with a recovery anchor, got: %v", err)
		}
	})

	// (2) consumption monthly → exit still rejected.
	t.Run("ConsumptionMonthly_BlocksExit", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "GUARD-2")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedConsumption(t, db, rm.ID)

		svc := buildMeterSvc(t, db)
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 120, 60, false, false, false, false)
		if err == nil {
			t.Fatal("exit must still be rejected when a consumption monthly exists for the cycle")
		}
	})

	// (3) recovery anchor does NOT mask a consumption monthly.
	t.Run("RecoveryAnchor_DoesNotMask_Consumption", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "GUARD-3")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedConsumption(t, db, rm.ID)   // consumption row (anchor NULL)
		seedRecoveryAnchor(t, db, rm.ID) // + recovery anchor (same cycle, allowed by 00041 indexes)

		svc := buildMeterSvc(t, db)
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 120, 60, false, false, false, false)
		if err == nil {
			t.Fatal("recovery anchor must NOT hide the consumption monthly — exit must still be rejected")
		}
	})
}
