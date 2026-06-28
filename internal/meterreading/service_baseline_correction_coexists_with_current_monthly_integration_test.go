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

// TestRecovery_CoexistsWithCurrentMonthlyReading (B6) pins the migration
// 00041 doctrine fix caught by Phase 6 browser smoke (2026-06-24).
//
// Production flow:
//  1. Monthly batch creates a DRAFT bill for the current cycle AND requires a
//     current-month MONTHLY reading to compute the snapshot.
//  2. Operator later notices a past misread → triggers recovery.
//  3. Service inserts recovery row at billing_month = current_month — which
//     collides with the existing MONTHLY reading under the original
//     (room, billing_month) WHERE reading_type='MONTHLY' unique index from
//     migration 00013.
//
// Phase 5 integration tests missed this because they seeded a DRAFT bill
// WITHOUT a current-month MONTHLY reading — a shortcut around the real
// monthly-batch ordering. Browser smoke caught it; migration 00041 relaxes
// the uniqueness predicate to exclude anchor rows. The two rows now coexist:
//   - consumption row: anchor_reason IS NULL — covered by primary unique index
//   - recovery row:    anchor_reason='READING_RECOVERY' — covered by anchor
//                      unique index (one recovery per (room, month))
//
// See feedback_reading_recovery_doctrine.md line 36-37
// ("The recovery month is NOT a consumption month — it is a re-anchor event").
func TestRecovery_CoexistsWithCurrentMonthlyReading(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B6-101")
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

	// Source month (the suspect past misread).
	srcMonth := time.Now().AddDate(0, -2, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  500,
		WaterPrevious:       40,
		WaterCurrent:        80,
	}
	if err := meterRepo.Create(ctx, source); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// Current-month consumption reading (what monthly batch + meter entry
	// produces in real life). THIS is the row that used to collide with
	// recovery under migration 00013's index.
	currentMonth := time.Now().Format("2006-01")
	consumption := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &currentMonth,
		ElectricityPrevious: 500,
		ElectricityCurrent:  620,
		WaterPrevious:       80,
		WaterCurrent:        92,
	}
	if err := meterRepo.Create(ctx, consumption); err != nil {
		t.Fatalf("seed current-month consumption: %v", err)
	}

	// Phase 7: no DRAFT bill required at recovery commit time (the bill side
	// becomes the Adjustment Application surface, not the recovery side).

	// The load-bearing call: recovery must succeed despite the consumption
	// row already occupying (room, current_month, MONTHLY).
	recovery, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 620, // operator's confirmed re-anchor value
		WaterCurrent:       92,
		AnchorNote:         "พบจดเกินจริง — re-anchor at consumption value",
	})
	_ = con
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (coexistence): %v", err)
	}

	// Assert both rows survive in the same (room, current_month, MONTHLY) slot.
	var rows []meterreading.MeterReading
	if err := db.Where("room_id = ? AND billing_month = ? AND reading_type = ?",
		rm.ID, currentMonth, meterreading.ReadingTypeMonthly).
		Find(&rows).Error; err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows in (room, current_month, MONTHLY), got %d", len(rows))
	}

	var anchorCount, consumptionCount int
	for _, r := range rows {
		if r.AnchorReason != nil && *r.AnchorReason == meterreading.AnchorReasonReadingRecovery {
			anchorCount++
			if r.ID != recovery.ID {
				t.Errorf("anchor row ID mismatch: got %v, want %v", r.ID, recovery.ID)
			}
		} else if r.AnchorReason == nil {
			consumptionCount++
			if r.ID != consumption.ID {
				t.Errorf("consumption row ID mismatch: got %v, want %v", r.ID, consumption.ID)
			}
		}
	}
	if anchorCount != 1 {
		t.Errorf("expected 1 RECOVERY anchor row, got %d", anchorCount)
	}
	if consumptionCount != 1 {
		t.Errorf("expected 1 consumption row, got %d", consumptionCount)
	}

	// Operator-double-click guard: a SECOND recovery against the same source
	// (or any second anchor on the same cycle) MUST fail via the new partial
	// unique index `idx_meter_readings_room_billing_month_anchor`. This is
	// the BE capability layer that backstops the Phase 6 FE Watch 2 UX guard.
	_, err = meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 620,
		WaterCurrent:       92,
		AnchorNote:         "duplicate recovery attempt",
	})
	if err == nil {
		t.Fatalf("expected second recovery to fail (anchor-uniqueness), got nil")
	}
}
