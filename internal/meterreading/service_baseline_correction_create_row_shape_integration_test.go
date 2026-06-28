//go:build integration

package meterreading_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_CreateBaselineCorrectionRowShape is the B1 lineage anchor for the
// Phase 5 recovery commit triple-guard (Lock A).
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (line 33, locked 2026-06-22)
//	PLAN:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (Lock A)
//
// Locks three layers of the prev=curr triple-guard:
//
//  1. Service-layer arm — the positive assertions verify that CreateBaselineCorrection
//     produces a recovery row with `previous = current`, anchor metadata
//     correctly set, and same-room derivation from source (Lock C).
//  2. DB-layer arm — the two negative sub-tests bypass the service and call
//     db.Create() with prev != curr; DB CHECK constraints from migration 00040
//     must reject with the expected constraint name in the error.
//  3. Domain-layer arm — covered by D1 unit tests in model_test.go
//     (TestMeterReading_ValidateAnchor_RecoveryRequiresPrevEqualsCurrent).
func TestRecovery_CreateBaselineCorrectionRowShape(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "B1-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// Wire real services + adapter (end-to-end Phase 5 path).
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)

	// Source: a past MONTHLY reading (3 months ago).
	srcMonth := time.Now().AddDate(0, -3, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  300, // claimed
		WaterPrevious:       50,
		WaterCurrent:        80,
	}
	if err := meterRepo.Create(ctx, source); err != nil {
		t.Fatalf("seed source meter: %v", err)
	}

	// DRAFT MONTHLY bill on the current month for the contract.
	recoveryMonth := time.Now().Format("2006-01")
	draft := billing.Bill{
		ContractID:   con.ID,
		BillingMonth: recoveryMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("seed draft bill: %v", err)
	}

	// ── Positive: commit a recovery ──
	out, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 250,
		WaterCurrent:       70,
		AnchorNote:         "พบว่าเดือนต้นทางจดเกินจริง",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Assert 1+2: prev=curr (service-layer arm of Lock A).
	if out.ElectricityPrevious != out.ElectricityCurrent {
		t.Errorf("elec prev=%d curr=%d, want equal", out.ElectricityPrevious, out.ElectricityCurrent)
	}
	if out.ElectricityCurrent != 250 {
		t.Errorf("elec current=%d, want 250 (operator input)", out.ElectricityCurrent)
	}
	if out.WaterPrevious != out.WaterCurrent {
		t.Errorf("water prev=%d curr=%d, want equal", out.WaterPrevious, out.WaterCurrent)
	}
	if out.WaterCurrent != 70 {
		t.Errorf("water current=%d, want 70 (operator input)", out.WaterCurrent)
	}

	// Assert 3: anchor metadata set.
	if out.AnchorReason == nil || *out.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("AnchorReason=%v, want READING_RECOVERY", out.AnchorReason)
	}
	if out.AnchorNote == nil || *out.AnchorNote != "พบว่าเดือนต้นทางจดเกินจริง" {
		t.Errorf("AnchorNote=%v, want operator note", out.AnchorNote)
	}
	if out.RecoverySourceReadingID == nil || *out.RecoverySourceReadingID != source.ID {
		t.Errorf("RecoverySourceReadingID=%v, want %v", out.RecoverySourceReadingID, source.ID)
	}

	// Assert 4: BillingMonth = server-derived current month (Lock E).
	if out.BillingMonth == nil || *out.BillingMonth != recoveryMonth {
		t.Errorf("BillingMonth=%v, want %s (Lock E server clock)", out.BillingMonth, recoveryMonth)
	}

	// Assert 5 (Lock C): RoomID derived from source.RoomID, not from input.
	if out.RoomID != source.RoomID {
		t.Errorf("RoomID=%v, want source.RoomID=%v (Lock C — service derives)", out.RoomID, source.RoomID)
	}

	// ── DB CHECK negatives: bypass service via raw db.Create() ──
	t.Run("DB CHECK rejects elec prev != curr", func(t *testing.T) {
		recoveryReason := meterreading.AnchorReasonReadingRecovery
		note := "negative test"
		bad := &meterreading.MeterReading{
			RoomID:                  rm.ID,
			ReadingType:             meterreading.ReadingTypeMonthly,
			BillingMonth:            ptrString("2099-01"),
			ElectricityPrevious:     199,
			ElectricityCurrent:      200,
			WaterPrevious:           50,
			WaterCurrent:            50,
			AnchorReason:            &recoveryReason,
			AnchorNote:              &note,
			RecoverySourceReadingID: &source.ID,
		}
		err := db.Create(bad).Error
		if err == nil {
			t.Fatalf("expected DB CHECK rejection, got nil")
		}
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "check constraint") {
			t.Fatalf("expected CHECK violation, got: %v", err)
		}
		if !strings.Contains(lower, "meter_readings_recovery_elec_prev_eq_curr") {
			t.Fatalf("expected constraint name meter_readings_recovery_elec_prev_eq_curr in error, got: %v", err)
		}
	})

	t.Run("DB CHECK rejects water prev != curr", func(t *testing.T) {
		recoveryReason := meterreading.AnchorReasonReadingRecovery
		note := "negative test water"
		bad := &meterreading.MeterReading{
			RoomID:                  rm.ID,
			ReadingType:             meterreading.ReadingTypeMonthly,
			BillingMonth:            ptrString("2099-02"),
			ElectricityPrevious:     200,
			ElectricityCurrent:      200,
			WaterPrevious:           49,
			WaterCurrent:            50,
			AnchorReason:            &recoveryReason,
			AnchorNote:              &note,
			RecoverySourceReadingID: &source.ID,
		}
		err := db.Create(bad).Error
		if err == nil {
			t.Fatalf("expected DB CHECK rejection, got nil")
		}
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "check constraint") {
			t.Fatalf("expected CHECK violation, got: %v", err)
		}
		if !strings.Contains(lower, "meter_readings_recovery_water_prev_eq_curr") {
			t.Fatalf("expected constraint name meter_readings_recovery_water_prev_eq_curr in error, got: %v", err)
		}
	})

	_ = uuid.UUID{} // satisfies the uuid import when negative subtests get inlined further
}

func ptrString(s string) *string { return &s }
