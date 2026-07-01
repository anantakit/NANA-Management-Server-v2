//go:build integration

package meterreading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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

// TestRecovery_SettlementBoundary locks Lock D's settlement-boundary
// doctrine: a completed move-out on the source's contract closes the
// recovery chain — regardless of whether the same tenant later returns.
//
// Doctrine: feedback_reading_recovery_doctrine.md.
// Plan:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (Lock D).
//
// Four scenarios, one shared helper:
//   - RejectsCrossTenant — Contract A (tenant X) settled via COMPLETED
//     move-out, Contract B (tenant Y) active. Recovery against source on
//     A must REJECT (the boundary closed when X left).
//   - AcceptsLeaseRenewal — Contract A (tenant X) ended without move-out
//     notice (pure lease renewal), Contract B (still tenant X) active.
//     Recovery succeeds — the financial relationship continued.
//   - RejectsRoomVacant — Contract A settled via COMPLETED move-out, room
//     currently vacant. REJECT (no destination + closed matter).
//   - RejectsSameTenantReturn — Contract A (tenant X) settled, X returns
//     later under Contract C. REJECT — the intervening settlement closed
//     the chain; X's return is a new story.
func TestRecovery_SettlementBoundary(t *testing.T) {
	t.Run("RejectsCrossTenant", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "D2C-A-101")

		// Contract A — tenant X, source month, then COMPLETED move-out.
		tnX := fixtures.SeedTenant(t, db)
		conA := seedActiveContract(t, db, tnX.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -3, 0).Format("2006-01"))
		endContract(t, db, conA.ID, time.Now().AddDate(0, -2, 0))
		seedCompletedMoveOut(t, db, conA.ID, time.Now().AddDate(0, -2, 0))

		// Contract B — different tenant Y, active, with current-month DRAFT.
		tnY := fixtures.SeedTenant(t, db)
		conB := seedActiveContract(t, db, tnY.ID, rm.ID, 1)
		seedDraftBill(t, db, conB.ID, time.Now().Format("2006-01"))

		meterSvc := buildMeterSvc(t, db)
		mrBefore := countTable(t, db, "meter_readings", "deleted_at IS NULL")
		liBefore := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
		auditBefore := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")

		_, err := meterSvc.CreateBaselineCorrection(ctx, recoveryInput(source.ID))
		if !errors.Is(err, meterreading.ErrBaselineCorrectionSettlementBoundaryCrossed) {
			t.Fatalf("err=%v, want ErrBaselineCorrectionSettlementBoundaryCrossed", err)
		}
		assertNoSideEffects(t, db, mrBefore, liBefore, auditBefore)
	})

	t.Run("AcceptsLeaseRenewal", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "D2C-B-101")

		// Contract A — tenant X, source month, ENDED on schedule (NO move-out).
		tnX := fixtures.SeedTenant(t, db)
		conA := seedActiveContract(t, db, tnX.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -3, 0).Format("2006-01"))
		endContract(t, db, conA.ID, time.Now().AddDate(0, -2, 0))
		// No SeedCompletedMoveOut — pure lease renewal.

		// Contract B — SAME tenant X, active.
		// Phase 7 (meter-only commit): no DRAFT bill required for the recovery
		// to land. Adjustment Application happens later at BillEditDrawer.
		_ = seedActiveContract(t, db, tnX.ID, rm.ID, 1)

		meterSvc := buildMeterSvc(t, db)
		recovery, err := meterSvc.CreateBaselineCorrection(ctx, recoveryInput(source.ID))
		if err != nil {
			t.Fatalf("CreateBaselineCorrection on lease renewal = %v, want nil", err)
		}
		if recovery == nil {
			t.Fatalf("recovery row nil")
		}

		// Recovery row landed in meter_readings with anchor metadata.
		if recovery.AnchorReason == nil || *recovery.AnchorReason != meterreading.AnchorReasonReadingRecovery {
			t.Errorf("AnchorReason=%v, want READING_RECOVERY", recovery.AnchorReason)
		}

		// Phase 7: no bill-side effect at recovery commit time.
		liCount := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
		if liCount != 0 {
			t.Errorf("ADJUSTMENT lines = %d, want 0 (Phase 7 — recovery commit has no bill side effect)", liCount)
		}
		auditCount := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")
		if auditCount != 0 {
			t.Errorf("audit rows = %d, want 0 (Phase 7 — recovery commit has no bill side effect)", auditCount)
		}
	})

	t.Run("RejectsRoomVacant", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "D2C-C-101")

		// Contract A — tenant X, COMPLETED move-out. No subsequent contract.
		tnX := fixtures.SeedTenant(t, db)
		conA := seedActiveContract(t, db, tnX.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -3, 0).Format("2006-01"))
		endContract(t, db, conA.ID, time.Now().AddDate(0, -1, 0))
		seedCompletedMoveOut(t, db, conA.ID, time.Now().AddDate(0, -1, 0))

		meterSvc := buildMeterSvc(t, db)
		mrBefore := countTable(t, db, "meter_readings", "deleted_at IS NULL")
		liBefore := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
		auditBefore := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")

		_, err := meterSvc.CreateBaselineCorrection(ctx, recoveryInput(source.ID))
		if !errors.Is(err, meterreading.ErrBaselineCorrectionSettlementBoundaryCrossed) {
			t.Fatalf("err=%v, want ErrBaselineCorrectionSettlementBoundaryCrossed", err)
		}
		assertNoSideEffects(t, db, mrBefore, liBefore, auditBefore)
	})

	t.Run("RejectsSameTenantReturn", func(t *testing.T) {
		db := testdb.Open(t)
		testdb.TruncateAll(t, db)
		ctx := context.Background()

		apt := fixtures.SeedApartment(t, db)
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), "D2C-D-101")

		// Contract A — tenant X, source month, COMPLETED move-out.
		//
		// Source month deliberately picked at (now - 5 months) NOT (now - 6
		// months). Contract A starts at (now - 6 months) preserving today's
		// day-of-month — if source were also at -6 months, first-of-source-
		// month (the 1st of that month) would be BEFORE contract A's
		// start_date (day-of-month preserved). FindContractIDByRoomAndMonth
		// would then miss the lookup and the test would pass for the WRONG
		// reason (NotFound → ErrSettlementBoundaryCrossed), bypassing the
		// HasCompletedMoveOut predicate we actually want to exercise. The
		// -5/-6 gap guarantees the lookup returns Contract A and the test
		// proves the settlement-boundary predicate.
		tnX := fixtures.SeedTenant(t, db)
		conA := seedActiveContract(t, db, tnX.ID, rm.ID, 6)
		source := seedSourceMonthly(t, db, rm.ID, time.Now().AddDate(0, -5, 0).Format("2006-01"))
		endContract(t, db, conA.ID, time.Now().AddDate(0, -3, 0))
		seedCompletedMoveOut(t, db, conA.ID, time.Now().AddDate(0, -3, 0))

		// Contract C — SAME tenant X returns under a new contract.
		conC := seedActiveContract(t, db, tnX.ID, rm.ID, 1)
		_ = seedDraftBill(t, db, conC.ID, time.Now().Format("2006-01"))

		meterSvc := buildMeterSvc(t, db)
		mrBefore := countTable(t, db, "meter_readings", "deleted_at IS NULL")
		liBefore := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'")
		auditBefore := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'")

		_, err := meterSvc.CreateBaselineCorrection(ctx, recoveryInput(source.ID))
		if !errors.Is(err, meterreading.ErrBaselineCorrectionSettlementBoundaryCrossed) {
			t.Fatalf("err=%v, want ErrBaselineCorrectionSettlementBoundaryCrossed (same tenant return; intervening settlement closed the chain)", err)
		}
		assertNoSideEffects(t, db, mrBefore, liBefore, auditBefore)
	})
}

// --- Shared Phase 5 test helpers ---
//
// Each helper hand-constructs the row via db.Create — the fixtures package
// doesn't have a SeedDraftBill / SeedCompletedMoveOut yet, and per Phase 2's
// "third-caller threshold" we keep these inline until a third Phase 5 file
// needs them. For now, B1+B2+D2-B+E1+E2+E3 each have their own bespoke
// setup; D2-C is the only file that needs the multi-scenario builders.

func buildMeterSvc(t *testing.T, db *gorm.DB) meterreading.MeterReadingService {
	t.Helper()
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	_ = billAuditRepo
	return meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)
}

func seedActiveContract(t *testing.T, db *gorm.DB, tenantID, roomID uuid.UUID, monthsAgo int) *contract.Contract {
	t.Helper()
	c := &contract.Contract{
		TenantID:               tenantID,
		RoomID:                 roomID,
		StartDate:              time.Now().AddDate(0, -monthsAgo, 0),
		MinMonths:              6,
		MonthlyRent:            300_000,
		DepositAmount:          300_000,
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
		DepositStatus:          contract.DepositStatusCollected,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	return c
}

func endContract(t *testing.T, db *gorm.DB, contractID uuid.UUID, endDate time.Time) {
	t.Helper()
	if err := db.Model(&contract.Contract{}).Where("id = ?", contractID).Updates(map[string]any{
		"end_date": endDate,
		"status":   contract.ContractStatusEnded,
	}).Error; err != nil {
		t.Fatalf("end contract %v: %v", contractID, err)
	}
}

func seedSourceMonthly(t *testing.T, db *gorm.DB, roomID uuid.UUID, billingMonth string) *meterreading.MeterReading {
	t.Helper()
	mr := &meterreading.MeterReading{
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &billingMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  300,
		WaterPrevious:       50,
		WaterCurrent:        80,
	}
	if err := db.Create(mr).Error; err != nil {
		t.Fatalf("seed source meter: %v", err)
	}
	return mr
}

func seedDraftBill(t *testing.T, db *gorm.DB, contractID uuid.UUID, billingMonth string) *billing.Bill {
	t.Helper()
	b := &billing.Bill{
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("seed draft bill: %v", err)
	}
	return b
}

func seedCompletedMoveOut(t *testing.T, db *gorm.DB, contractID uuid.UUID, moveOutDate time.Time) *moveout.MoveOutNotice {
	t.Helper()
	closedAt := moveOutDate
	notice := &moveout.MoveOutNotice{
		ContractID:           contractID,
		NoticeDate:           moveOutDate.AddDate(0, 0, -7),
		ScheduledMoveOutDate: moveOutDate,
		ActualMoveOutDate:    &moveOutDate,
		Status:               moveout.MoveOutStatusCompleted,
		ClosedAt:             &closedAt,
	}
	if err := db.Create(notice).Error; err != nil {
		t.Fatalf("seed completed move-out: %v", err)
	}
	return notice
}

func recoveryInput(sourceID uuid.UUID) meterreading.CreateBaselineCorrectionInput {
	return meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    &sourceID,
		ElectricityCurrent: 250,
		WaterCurrent:       70,
		AnchorNote:         "Phase 7 settlement-boundary test",
	}
}

func assertNoSideEffects(t *testing.T, db *gorm.DB, mrBefore, liBefore, auditBefore int64) {
	t.Helper()
	if got := countTable(t, db, "meter_readings", "deleted_at IS NULL"); got != mrBefore {
		t.Errorf("meter_readings count: before=%d after=%d (recovery must NOT persist)", mrBefore, got)
	}
	if got := countTable(t, db, "bill_line_items", "line_type = 'ADJUSTMENT'"); got != liBefore {
		t.Errorf("ADJUSTMENT line_items count: before=%d after=%d", liBefore, got)
	}
	if got := countTable(t, db, "bill_audit_log", "action = 'APPLY_RECOVERY_ADJUSTMENT'"); got != auditBefore {
		t.Errorf("audit count: before=%d after=%d", auditBefore, got)
	}
}

