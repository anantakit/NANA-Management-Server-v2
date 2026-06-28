//go:build integration

package meterreading_test

import (
	"context"
	"strings"
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

// TestRecovery_RejectsExitSource locks the D2-B cross-row invariant
// (Lock A's cousin): recovery's source must be a regular MONTHLY reading,
// not an EXIT reading.
//
// Doctrine: feedback_reading_recovery_doctrine.md.
// Plan:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (D2-B).
//
// EXIT readings are partial-period readings tied to move-out workflow;
// recovering against them would attempt to re-anchor a closed-out
// meter chain. Phase 5 rejects at the service layer pre-tx.
func TestRecovery_RejectsExitSource(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "D2B-101")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)

	// Seed an EXIT reading on the room.
	exitDate := time.Now().AddDate(0, -1, 0)
	exitMR := fixtures.SeedExitMeter(t, db, rm.ID.String(), exitDate, 100, 30)

	// Count baselines BEFORE the call (for no-side-effects assertion).
	mrCountBefore := countTable(t, db, "meter_readings", "deleted_at IS NULL")
	liCountBefore := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
	auditCountBefore := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")

	// Attempt recovery against the EXIT row.
	_, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    exitMR.ID,
		ElectricityCurrent: 200,
		WaterCurrent:       40,
		AnchorNote:         "trying exit as source",
	})
	if err == nil {
		t.Fatalf("CreateBaselineCorrection on EXIT source = nil err, want rejection")
	}
	if !strings.Contains(err.Error(), "MONTHLY") {
		t.Errorf("error message=%q, want to mention MONTHLY", err.Error())
	}

	// No side effects.
	mrCountAfter := countTable(t, db, "meter_readings", "deleted_at IS NULL")
	if mrCountAfter != mrCountBefore {
		t.Errorf("meter_readings count changed: before=%d after=%d (recovery must NOT persist)", mrCountBefore, mrCountAfter)
	}
	liCountAfter := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
	if liCountAfter != liCountBefore {
		t.Errorf("ADJUSTMENT bill_line_items count changed: before=%d after=%d", liCountBefore, liCountAfter)
	}
	auditCountAfter := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")
	if auditCountAfter != auditCountBefore {
		t.Errorf("APPLY_RECOVERY_ADJUSTMENT audit count changed: before=%d after=%d", auditCountBefore, auditCountAfter)
	}
}

// countTable returns the row count for the table with an optional WHERE
// clause. Helper colocated with Phase 5 integration tests; factor into
// testutil/fixtures if a third caller appears.
func countTable(t *testing.T, db *gorm.DB, table, where string) int64 {
	t.Helper()
	var n int64
	q := db.Table(table)
	if where != "" {
		q = q.Where(where)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count %s WHERE %s: %v", table, where, err)
	}
	return n
}
