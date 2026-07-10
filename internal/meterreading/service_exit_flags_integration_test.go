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

// TestExitCreate_MeterHardwareFlags is the F1 parity proof: the move-out
// exit-CREATE path can now express the same meter-hardware events as monthly
// entry (rollover / replacement), so a legitimate current < previous at move-out
// is recordable instead of being force-rejected.
func TestExitCreate_MeterHardwareFlags(t *testing.T) {
	// helper: latest reading = a prior-month consumption with a high current, so
	// the exit's previous is high (and HasConsumptionMonthly(now) stays false).
	seedHighPrior := func(t *testing.T, db *gorm.DB, roomID uuid.UUID, elec, water int) {
		t.Helper()
		m := time.Now().AddDate(0, -1, 0).Format("2006-01")
		r := &meterreading.MeterReading{
			RoomID: roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &m,
			ElectricityPrevious: elec - 100, ElectricityCurrent: elec,
			WaterPrevious: water - 10, WaterCurrent: water,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed prior reading: %v", err)
		}
	}
	loadExit := func(t *testing.T, db *gorm.DB, roomID uuid.UUID) *meterreading.MeterReading {
		t.Helper()
		var m meterreading.MeterReading
		if err := db.Where("room_id = ? AND reading_type = ?", roomID, meterreading.ReadingTypeExit).First(&m).Error; err != nil {
			t.Fatalf("load exit: %v", err)
		}
		return &m
	}

	// Rollover: current (500) < previous (99000) is accepted when the rollover
	// flag is set (the meter wrapped past its max).
	t.Run("Rollover_AcceptsCurrentBelowPrevious", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "FLAG-1")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedHighPrior(t, db, rm.ID, 99000, 100)

		svc := buildMeterSvc(t, db)
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 500, 150, false, false, true, false); err != nil {
			t.Fatalf("rollover exit (current < previous) should be accepted, got: %v", err)
		}
		exit := loadExit(t, db, rm.ID)
		if !exit.IsRolloverElectricity {
			t.Error("exit should carry the electricity rollover flag")
		}
	})

	// Replacement: previous is zeroed, so current (30) after a swap is accepted and
	// usage is measured from 0.
	t.Run("Replaced_ZeroesPrevious", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "FLAG-2")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedHighPrior(t, db, rm.ID, 5000, 100)

		svc := buildMeterSvc(t, db)
		if err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 30, 150, true, false, false, false); err != nil {
			t.Fatalf("replaced exit should be accepted with previous zeroed, got: %v", err)
		}
		exit := loadExit(t, db, rm.ID)
		if exit.ElectricityPrevious != 0 {
			t.Errorf("replaced electricity previous = %d, want 0", exit.ElectricityPrevious)
		}
		if exit.ElectricityUsed() != 30 {
			t.Errorf("replaced electricity usage = %d, want 30 (measured from 0)", exit.ElectricityUsed())
		}
	})

	// Mutual exclusion: rollover + replaced on the same meter is rejected (the
	// model's ErrRolloverAndReplacedConflict, surfaced through the move-out path).
	t.Run("RolloverAndReplaced_Conflict_Rejected", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()
		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "FLAG-3")
		tn := fixtures.SeedTenant(t, db)
		_ = seedActiveContract(t, db, tn.ID, rm.ID, 6)
		seedHighPrior(t, db, rm.ID, 5000, 100)

		svc := buildMeterSvc(t, db)
		err := svc.CreateExitForMoveOut(ctx, rm.ID, time.Now().UTC(), 30, 150, true, false, true, false)
		if err == nil {
			t.Fatal("rollover + replaced on the same meter must be rejected")
		}
	})
}
