//go:build integration

package meterreading_test

import (
	"context"
	"errors"
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

// TestRecovery_RollsBackBothOnAttachFailure is the E1 TX-atomicity anchor.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md.
//	PLAN:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (E1).
//
// When the billing adapter's AttachAdjustmentLine fails (e.g. because no
// DRAFT MONTHLY bill exists for the current month), the recovery meter row
// INSERT must roll back along with it. This is the load-bearing assertion
// that Phase 5's cross-feature TX wires correctly through txCtx.
//
// Per testing-strategy.md §6 — "cross-feature side-effect order in tx" is
// real-DB-only signal; a mocked adapter test could not catch a missing
// txCtx propagation. E1 is the canonical integration anchor.
func TestRecovery_RollsBackBothOnAttachFailure(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "E1-101")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billRecoveryAdapter := billing.NewRecoveryAdapter(billRepo, billAuditRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billRecoveryAdapter, txMgr)

	// Source: past MONTHLY reading. Do NOT seed a DRAFT bill for the
	// current month — adapter must surface ErrRecoveryNoDraftBill, which
	// the TX wrapper must propagate as a rollback.
	srcMonth := time.Now().AddDate(0, -3, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  300,
		WaterPrevious:       50,
		WaterCurrent:        80,
	}
	if err := meterRepo.Create(ctx, source); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	mrBefore := countTable(t, db, "meter_readings", "deleted_at IS NULL")
	liBefore := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
	auditBefore := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")

	_, err := meterSvc.CreateRecovery(ctx, meterreading.CreateRecoveryInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 250,
		WaterCurrent:       70,
		Amount:             -25000,
		ReasonCode:         "METER_RECOVERY",
		AnchorNote:         "no draft will exist",
		AdjustmentNote:     "TX rollback test note",
	})
	if !errors.Is(err, billing.ErrRecoveryNoDraftBill) {
		t.Fatalf("err=%v, want billing.ErrRecoveryNoDraftBill", err)
	}

	// Load-bearing: the meter INSERT must NOT persist (TX rolled back).
	mrAfter := countTable(t, db, "meter_readings", "deleted_at IS NULL")
	if mrAfter != mrBefore {
		t.Errorf("meter_readings count changed: before=%d after=%d (recovery row leaked despite TX rollback)", mrBefore, mrAfter)
	}

	// No ADJUSTMENT line was created (the adapter rejects before line construction
	// when no DRAFT bill exists; this assertion guards future regressions where
	// line construction might race ahead of the draft check).
	liAfter := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
	if liAfter != liBefore {
		t.Errorf("ADJUSTMENT line_items count changed: before=%d after=%d", liBefore, liAfter)
	}

	auditAfter := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")
	if auditAfter != auditBefore {
		t.Errorf("audit count changed: before=%d after=%d", auditBefore, auditAfter)
	}
}
