//go:build integration

package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/domain"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// service_correction_settlement_integration_test.go is the load-bearing spec
// for SETTLEMENT correction. Unlike MONTHLY's correction (which is pure
// document replacement), SETTLEMENT correction is a workflow rewind that
// touches notice state, settlement attachment, absorbed monthly bills, and
// payment metadata — all inside one transaction. These tests pin each piece
// against real Postgres so future refactors that re-order or split the
// rewind break here loudly.
//
// 8 scenarios per the design lock (project_settlement_correction_design_lock):
//   1. HappyPath_FromPendingPayment — full state flip
//   2. HappyPath_FromReadyToClose   — both allowed statuses re-enter workflow
//   3. AbsorbedMonthlyRoundTrip     — restored → re-absorbed exactly once
//   4. PaidBlocked                  — billing CanCorrect rejects, no mutations
//   5. CompletedNoticeBlocked       — workflow guard rejects, no mutations
//   6. ConcurrentRace               — row-lock serializes (1 winner / 1 fail)
//   7. AuditFailureRollsBack        — notice + bills + absorbed all revert
//   8. AuditEventShape              — SUPERSEDE + CREATE_FROM_CORRECTION
//
// Mock-based service tests in moveout/service_test.go cover orchestration
// call order. THIS file is the only place real-Postgres TX invariants
// (DEFERRABLE FK, row lock, partial-unique on absorbed monthlies, audit
// rollback through cross-feature tx) are exercised.

// ============================================================
// Test scaffolding
// ============================================================

// correctionFailingAuditRepo wraps the real BillAuditRepository but fails
// Create for a configurable subset of actions. Lets the rollback test pass
// real audits during workflow setup (CREATE_DRAFT / FINALIZE) and then
// fail on SUPERSEDE during the correction call itself.
type correctionFailingAuditRepo struct {
	wrapped BillAuditRepository
	failFor map[BillAuditAction]bool
}

func (f *correctionFailingAuditRepo) Create(ctx context.Context, log *BillAuditLog) error {
	if f.failFor[log.Action] {
		return fmt.Errorf("audit failure injected for action %s", log.Action)
	}
	return f.wrapped.Create(ctx, log)
}

func (f *correctionFailingAuditRepo) EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return f.wrapped.EditedBillIDs(ctx, billIDs)
}

func (f *correctionFailingAuditRepo) FindLatestSupersedeReason(ctx context.Context, billID uuid.UUID) (string, error) {
	return f.wrapped.FindLatestSupersedeReason(ctx, billID)
}

// correctionEnv holds the DB-wired scaffolding for one settlement correction
// scenario. The constructor seeds an apartment / contract / room / configs /
// EXIT meter / notice and walks the production move-out workflow up to the
// requested targetStatus (PENDING_PAYMENT, READY_TO_CLOSE, or COMPLETED).
type correctionEnv struct {
	db          *gorm.DB
	moveOutSvc  moveout.MoveOutService
	billSvc     BillingService
	ctx         context.Context

	apartmentID      uuid.UUID
	contractID       uuid.UUID
	roomID           uuid.UUID
	noticeID         uuid.UUID
	settlementBillID uuid.UUID // the FINALIZED settlement before correction
	moveOutDate      time.Time

	// Populated only when envOptions.withAbsorbedMonthly = true
	absorbedMonthlyBillID uuid.UUID
	absorbedMonthlyMonth  string
}

type envOptions struct {
	targetStatus        moveout.MoveOutStatus
	withAbsorbedMonthly bool
	// auditOverride lets the rollback test inject a failing audit. nil →
	// use the real BillAuditRepository.
	auditOverride BillAuditRepository
}

// newCorrectionEnv seeds + walks the production move-out workflow to the
// requested target status. Use this as the standard fixture for every
// correction integration test — it mirrors what an admin sees in the UI
// immediately before clicking "ออกใบสรุปยอดใหม่".
func newCorrectionEnv(t *testing.T, opts envOptions) *correctionEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "S-101")
	tn := fixtures.SeedTenant(t, db)
	// Contract 8 months ago — comfortably past min_months for deposit return.
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000) // ฿100/day

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -5)

	// Optional: prior unpaid monthly bill from M-3, BEFORE M-1, so absorption
	// is FULL (creates OUTSTANDING_BILL line item on the settlement).
	env := &correctionEnv{
		db:          db,
		ctx:         context.Background(),
		apartmentID: apt.ID,
		contractID:  c.ID,
		roomID:      rm.ID,
		moveOutDate: moveOutDate,
	}
	if opts.withAbsorbedMonthly {
		env.absorbedMonthlyMonth = moveOutDate.AddDate(0, -3, 0).Format("2006-01")
		env.absorbedMonthlyBillID = seedFinalizedMonthly(t, db, c.ID, env.absorbedMonthlyMonth)
	}

	// EXIT meter + PENDING_SETTLEMENT notice — fixture helpers, same shape
	// as the settlement integration test family.
	fixtures.SeedExitMeter(t, db, rm.ID.String(), moveOutDate, 100, 20)
	notice := fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)
	env.noticeID = notice.ID

	// ── Wire the real service graph (with optional audit override) ──
	env.billSvc = buildRealBillingServiceWithAudit(t, db, opts.auditOverride)
	env.moveOutSvc = buildRealMoveOutService(t, db, env.billSvc)

	// ── Walk to the target status via production paths ──
	walkToTargetStatus(t, env, opts.targetStatus)

	return env
}

// walkToTargetStatus moves the notice from PENDING_SETTLEMENT (initial fixture
// state) up to the requested status by calling the same service methods the
// UI would call. Failing here means a regression in the workflow itself.
func walkToTargetStatus(t *testing.T, env *correctionEnv, target moveout.MoveOutStatus) {
	t.Helper()

	// PENDING_SETTLEMENT → DRAFT attached (status stays)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	// PENDING_SETTLEMENT → PENDING_PAYMENT (DRAFT becomes FINALIZED)
	if _, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID); err != nil {
		t.Fatalf("FinalizeSettlement: %v", err)
	}

	// Capture the FINALIZED settlement bill ID — this is what correction
	// will void in every test scenario.
	var notice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&notice).Error; err != nil {
		t.Fatalf("re-fetch notice after finalize: %v", err)
	}
	if notice.SettlementBillID == nil {
		t.Fatalf("settlement_bill_id nil after FinalizeSettlement — workflow broken?")
	}
	env.settlementBillID = *notice.SettlementBillID

	if target == moveout.MoveOutStatusPendingPayment {
		return
	}

	// → READY_TO_CLOSE (records payment outcome)
	if _, err := env.moveOutSvc.RecordPaymentOutcome(env.ctx, env.noticeID, moveout.RecordPaymentOutcomeRequest{
		PaymentOutcome: string(moveout.PaymentOutcomePaidExtra),
		PaymentMethod:  string(domain.PaymentMethodCash),
		PaymentNote:    "เก็บเงินสดแล้ว (จะถูกล้างเมื่อ correct)",
	}); err != nil {
		t.Fatalf("RecordPaymentOutcome: %v", err)
	}
	if target == moveout.MoveOutStatusReadyToClose {
		return
	}

	// → COMPLETED (closes the move-out — only used by the blocked test)
	if _, err := env.moveOutSvc.CloseMoveOut(env.ctx, env.noticeID); err != nil {
		t.Fatalf("CloseMoveOut: %v", err)
	}
	if target == moveout.MoveOutStatusCompleted {
		return
	}

	t.Fatalf("unsupported target status %q", target)
}

// buildRealBillingServiceWithAudit mirrors buildRealBillingService from the
// settlement integration test, with an optional audit override for the
// rollback scenario.
func buildRealBillingServiceWithAudit(t *testing.T, db *gorm.DB, override BillAuditRepository) BillingService {
	t.Helper()
	txMgr := database.NewTxManager(db)
	billRepo := NewBillingRepository(db)
	billAuditRepo := BillAuditRepository(NewBillAuditRepository(db))
	if override != nil {
		billAuditRepo = override
	}
	contractRepo := contract.NewContractRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	bcRepo := billingconfig.NewBillingConfigRepository(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	return NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, bcRepo, moveOutRepo, txMgr)
}

// buildRealMoveOutService wires the moveout service end-to-end against the
// test DB. Mirrors cmd/main.go's DI graph.
func buildRealMoveOutService(t *testing.T, db *gorm.DB, billSvc BillingService) moveout.MoveOutService {
	t.Helper()
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txMgr)
	return moveout.NewMoveOutService(moveOutRepo, contractRepo, contractRepo, roomRepo, meterSvc, billSvc, billSvc, txMgr)
}

// seedFinalizedMonthly inserts a FINALIZED monthly bill for the given
// contract+month, ready to be absorbed by a subsequent settlement. Returns
// the bill ID for later assertions.
func seedFinalizedMonthly(t *testing.T, db *gorm.DB, contractID uuid.UUID, billingMonth string) uuid.UUID {
	t.Helper()
	bill := Bill{
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  416000, // 4,160 baht — matches existing settlement integration test
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("seed finalized monthly: %v", err)
	}
	items := []BillLineItem{
		{BillID: bill.ID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าเช่า", Amount: 300000, SortOrder: 1},
		{BillID: bill.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ 20 หน่วย", Amount: 36000, Quantity: 20, UnitPrice: 1800, SortOrder: 2},
		{BillID: bill.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า 100 หน่วย", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 3},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("seed finalized monthly line items: %v", err)
	}
	return bill.ID
}

// ============================================================
// 1. HappyPath_FromPendingPayment
//
// PENDING_PAYMENT notice with FINALIZED settlement attached. Correction
// must: void old → create new DRAFT → rebind notice → downgrade to
// PENDING_SETTLEMENT → emit audit pair. Payment fields are nil before
// (PENDING_PAYMENT never has them set) — assertions verify they stay nil.
// ============================================================

func TestSettlementCorrection_HappyPath_FromPendingPayment(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusPendingPayment})

	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "ค่าไฟผิด คำนวณใหม่"}, nil)
	if err != nil {
		t.Fatalf("CorrectSettlement: %v", err)
	}

	assertOldSettlementVoided(t, env)
	assertNewDraftAttached(t, env, "PendingPayment")
	assertPaymentMetadataCleared(t, env)
	// PENDING_PAYMENT case — payment fields were nil before, still nil after.
	// (READY_TO_CLOSE case below tests the actual clearing transition.)
}

// ============================================================
// 2. HappyPath_FromReadyToClose
//
// Identical to #1 BUT starts from READY_TO_CLOSE with payment_outcome set.
// Proves the second allowed status re-enters the workflow correctly AND
// proves the clearing transition (was set → now nil).
// ============================================================

func TestSettlementCorrection_HappyPath_FromReadyToClose(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusReadyToClose})

	// Sanity: payment fields ARE set before correction.
	var before moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&before).Error; err != nil {
		t.Fatalf("re-fetch notice before correction: %v", err)
	}
	if before.PaymentOutcome == nil {
		t.Fatalf("READY_TO_CLOSE notice should have payment_outcome set as a precondition")
	}

	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "ลูกค้าทักท้วงยอดมิเตอร์"}, nil)
	if err != nil {
		t.Fatalf("CorrectSettlement: %v", err)
	}

	assertOldSettlementVoided(t, env)
	assertNewDraftAttached(t, env, "ReadyToClose")
	assertPaymentMetadataCleared(t, env) // transition: was set → now nil
}

// ============================================================
// 3. AbsorbedMonthlyRoundTrip
//
// The most subtle invariant. A prior unpaid monthly is absorbed by the
// initial settlement (status=VOID, void_reason=ABSORBED_BY_SETTLEMENT).
// Correction MUST restore it (→ FINALIZED), let prepareSettlementPlan
// re-absorb it (→ VOID/ABSORBED), all inside one tx — with the absorbed
// bill keeping its identity (same UUID, no duplicate rows, no ghost).
// ============================================================

func TestSettlementCorrection_AbsorbedMonthlyRoundTrip(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{
		targetStatus:        moveout.MoveOutStatusPendingPayment,
		withAbsorbedMonthly: true,
	})

	// Precondition: the seeded monthly is absorbed by the FIRST settlement
	// (workflow walk in env setup already did GenerateSettlement which
	// triggers absorption). Verify before driving correction.
	var beforeMonthly Bill
	if err := env.db.Where("id = ?", env.absorbedMonthlyBillID).First(&beforeMonthly).Error; err != nil {
		t.Fatalf("re-fetch absorbed monthly (before): %v", err)
	}
	if !beforeMonthly.IsAbsorbedBySettlement() {
		t.Fatalf("absorbed monthly precondition: want VOID(ABSORBED), got status=%s reason=%v",
			beforeMonthly.Status, beforeMonthly.VoidReason)
	}

	// Drive correction.
	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "ตรวจสอบใหม่ ออกใบใหม่"}, nil)
	if err != nil {
		t.Fatalf("CorrectSettlement: %v", err)
	}

	// ── Same monthly bill ID — no duplicate, no replacement ──
	var afterMonthly Bill
	if err := env.db.Where("id = ?", env.absorbedMonthlyBillID).First(&afterMonthly).Error; err != nil {
		t.Fatalf("re-fetch absorbed monthly (after): %v", err)
	}
	if !afterMonthly.IsAbsorbedBySettlement() {
		t.Errorf("monthly after round-trip: status=%s reason=%v, want VOID(ABSORBED) — re-absorbed by new settlement",
			afterMonthly.Status, afterMonthly.VoidReason)
	}

	// ── Exactly ONE absorbed monthly for this contract (no duplicates,
	//     no ghosts from a half-applied restore) ──
	var absorbedCount int64
	if err := env.db.Model(&Bill{}).
		Where("contract_id = ? AND bill_type = ? AND status = ? AND void_reason = ?",
			env.contractID, BillTypeMonthly, BillStatusVoid, "ABSORBED_BY_SETTLEMENT").
		Count(&absorbedCount).Error; err != nil {
		t.Fatalf("count absorbed monthlies: %v", err)
	}
	if absorbedCount != 1 {
		t.Errorf("absorbed monthly count = %d, want 1 (round-trip must end at exactly one — same bill)", absorbedCount)
	}

	// ── Total monthly bills for this contract still 1 (no duplicate row
	//     created during the restore/re-absorb cycle) ──
	var monthlyTotal int64
	if err := env.db.Unscoped().Model(&Bill{}).
		Where("contract_id = ? AND bill_type = ?", env.contractID, BillTypeMonthly).
		Count(&monthlyTotal).Error; err != nil {
		t.Fatalf("count monthly bills: %v", err)
	}
	if monthlyTotal != 1 {
		t.Errorf("monthly bill count for contract = %d, want 1 (no ghost rows from round-trip)", monthlyTotal)
	}

	// ── New settlement has EXACTLY ONE OUTSTANDING_BILL line item with
	//     amount matching the absorbed monthly's total. Tighter than
	//     "≥ 1" so a double-absorption regression (re-absorbed twice,
	//     summing to 832000) or a phantom OUTSTANDING_BILL line would
	//     trip the test. ──
	var newNotice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&newNotice).Error; err != nil {
		t.Fatalf("re-fetch notice: %v", err)
	}
	if newNotice.SettlementBillID == nil {
		t.Fatal("notice has no settlement bill after correction")
	}
	var outstandingLines []BillLineItem
	if err := env.db.
		Where("bill_id = ? AND line_type = ?", *newNotice.SettlementBillID, LineItemOutstandingBill).
		Find(&outstandingLines).Error; err != nil {
		t.Fatalf("fetch OUTSTANDING_BILL on new settlement: %v", err)
	}
	if len(outstandingLines) != 1 {
		t.Fatalf("OUTSTANDING_BILL count = %d, want exactly 1", len(outstandingLines))
	}
	// 416000 satang = the seeded absorbed monthly's TotalAmount in
	// seedFinalizedMonthly. M-3 is strictly before M-1, so absorption
	// is FULL (vs M-1 which absorbs utility-only).
	const expectedAbsorbedAmount int64 = 416000
	if got := outstandingLines[0].Amount; got != expectedAbsorbedAmount {
		t.Errorf("OUTSTANDING_BILL.amount = %d, want %d (full absorbed monthly total — drift means double-absorption or wrong amount)",
			got, expectedAbsorbedAmount)
	}
}

// Note on what the round-trip test does NOT pin: the monthly bill's
// transient FINALIZED state mid-tx (between restoreAbsorbedBills and
// commitSettlementPlan.MarkAbsorbedBySettlement). That transition isn't
// externally observable — no audit hook on the monthly's status flip,
// and the test reads post-tx-commit. Current invariants (same UUID +
// total count == 1 + ends absorbed + OUTSTANDING_BILL matches absorbed
// amount) are the strongest the model allows without adding monthly-
// level audit infrastructure (out of scope).

// ============================================================
// 4. PaidBlocked
//
// Mark the FINALIZED settlement as PAID, then drive correction. Billing's
// CanCorrect must reject; no state changes.
// ============================================================

func TestSettlementCorrection_PaidBlocked(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusPendingPayment})

	// Mark the settlement bill as PAID directly (skip the payment workflow —
	// we just need the bill in PAID status to exercise the billing guard).
	if err := env.db.Model(&Bill{}).
		Where("id = ?", env.settlementBillID).
		Update("status", BillStatusPaid).Error; err != nil {
		t.Fatalf("mark settlement PAID: %v", err)
	}

	snapshot := snapshotNoticeAndBills(t, env)

	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "ลองแก้ทั้งที่ชำระแล้ว"}, nil)
	if err == nil {
		t.Fatal("expected error from PAID guard, got nil")
	}
	if !strings.Contains(err.Error(), ErrAlreadyPaid.Error()) {
		t.Errorf("err = %q, want to contain %q", err.Error(), ErrAlreadyPaid.Error())
	}

	// No state changes — snapshot must be byte-identical.
	assertSnapshotUnchanged(t, env, snapshot)
}

// ============================================================
// 5. CompletedNoticeBlocked
//
// Walk the notice to COMPLETED, then drive correction. Workflow guard
// (CanDowngradeToPendingSettlement) must reject; no state changes,
// no billing call.
// ============================================================

func TestSettlementCorrection_CompletedNoticeBlocked(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusCompleted})

	snapshot := snapshotNoticeAndBills(t, env)

	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "ลองแก้ COMPLETED"}, nil)
	if err == nil {
		t.Fatal("expected error from COMPLETED guard, got nil")
	}
	if !strings.Contains(err.Error(), moveout.ErrCannotDowngradeToPendingSettlement.Error()) {
		t.Errorf("err = %q, want to contain %q",
			err.Error(), moveout.ErrCannotDowngradeToPendingSettlement.Error())
	}

	assertSnapshotUnchanged(t, env, snapshot)
}

// ============================================================
// 6. ConcurrentRace
//
// Two goroutines call CorrectSettlement on the same notice. The row lock
// (FindByIDForUpdate) serializes them: winner commits PENDING_SETTLEMENT,
// loser unblocks AFTER winner's commit, reads the now-PENDING_SETTLEMENT
// notice, fails CanDowngradeToPendingSettlement (which only allows
// PENDING_PAYMENT / READY_TO_CLOSE), returns a clean 400.
//
// Looped over 3 iterations for scheduler-luck robustness (matches the
// MONTHLY correction race-test pattern).
// ============================================================

func TestSettlementCorrection_ConcurrentRace(t *testing.T) {
	const iterations = 3
	for iter := 0; iter < iterations; iter++ {
		iter := iter
		t.Run(fmt.Sprintf("iter-%d", iter), func(t *testing.T) {
			env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusPendingPayment})

			var (
				wg          sync.WaitGroup
				results     = make([]error, 2)
				startSignal = make(chan struct{})
			)
			wg.Add(2)
			for i := 0; i < 2; i++ {
				i := i
				go func() {
					defer wg.Done()
					<-startSignal
					_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
						moveout.CorrectSettlementRequest{CorrectionReason: "race attempt"}, nil)
					results[i] = err
				}()
			}
			close(startSignal)
			wg.Wait()

			// Exactly 1 success + 1 fail.
			successCount := 0
			failCount := 0
			for _, err := range results {
				if err == nil {
					successCount++
				} else {
					failCount++
				}
			}
			if successCount != 1 || failCount != 1 {
				t.Fatalf("expected 1 success + 1 fail, got %d success / %d fail; errors=%v",
					successCount, failCount, results)
			}

			// Loser's error must be the workflow-guard sentinel — the lock
			// serializes them, so by the time the loser's tx reads the
			// notice it's already PENDING_SETTLEMENT (winner committed),
			// which fails CanDowngradeToPendingSettlement.
			for _, err := range results {
				if err == nil {
					continue
				}
				if !strings.Contains(err.Error(), moveout.ErrCannotDowngradeToPendingSettlement.Error()) {
					t.Errorf("loser error = %q, want %q sentinel",
						err.Error(), moveout.ErrCannotDowngradeToPendingSettlement.Error())
				}
			}

			// Exactly ONE new DRAFT settlement exists for this contract
			// (winner's). No half-applied state from the loser.
			var draftCount int64
			if err := env.db.Model(&Bill{}).
				Where("contract_id = ? AND bill_type = ? AND status = ?",
					env.contractID, BillTypeSettlement, BillStatusDraft).
				Count(&draftCount).Error; err != nil {
				t.Fatalf("count draft settlements: %v", err)
			}
			if draftCount != 1 {
				t.Errorf("draft settlement count = %d, want 1 (loser's tx must roll back)", draftCount)
			}

			// Audit pair exists exactly once for this scenario (winner's).
			var auditCount int64
			if err := env.db.Model(&BillAuditLog{}).
				Where("bill_id = ? AND action = ?", env.settlementBillID, AuditSupersede).
				Count(&auditCount).Error; err != nil {
				t.Fatalf("count SUPERSEDE audits: %v", err)
			}
			if auditCount != 1 {
				t.Errorf("SUPERSEDE audit count = %d, want 1 (only winner emits)", auditCount)
			}
		})
	}
}

// ============================================================
// 7. AuditFailureRollsBack
//
// Inject a failing audit and prove the entire correction tx rolls back —
// notice fields, old settlement, new settlement (none), absorbed monthly
// bills, audit log all revert to pre-correction state.
//
// Two sub-tests cover BOTH audit emission points:
//
//   fail_on_supersede — the FIRST audit (old bill, status flip already
//     written, absorbed bills already restored mid-tx but not re-absorbed
//     yet — short path)
//
//   fail_on_create_from_correction — the SECOND audit (old voided +
//     superseded link set + new DRAFT inserted + absorbed monthlies
//     re-absorbed all already happened mid-tx). This is the LONG path
//     where a swallowed-error regression would leak the most state if
//     rollback didn't fence everything.
//
// Workflow setup uses the REAL audit (so CREATE_DRAFT + FINALIZE persist
// correctly during env build); only the targeted correction-audit fires
// with the failure.
// ============================================================

func TestSettlementCorrection_AuditFailureRollsBack(t *testing.T) {
	cases := []struct {
		name    string
		failFor BillAuditAction
	}{
		// Fails before new bill is inserted — short path.
		{"fail_on_supersede", AuditSupersede},
		// Fails AFTER new bill + re-absorbed monthlies are written —
		// longest path; biggest blast radius if rollback isn't tight.
		{"fail_on_create_from_correction", AuditCreateFromCorrection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCorrectionEnv(t, envOptions{
				targetStatus:        moveout.MoveOutStatusPendingPayment,
				withAbsorbedMonthly: true,
			})

			// Snapshot before correction — must be byte-identical after.
			snapshot := snapshotNoticeAndBills(t, env)

			// Swap in a failing audit specifically for the targeted action.
			// Rebuild billing + moveout services so the failing audit is
			// used inside the correction tx (the env's workflow walk
			// already used real audit — those CREATE_DRAFT + FINALIZE rows
			// persist).
			failingAudit := &correctionFailingAuditRepo{
				wrapped: NewBillAuditRepository(env.db),
				failFor: map[BillAuditAction]bool{tc.failFor: true},
			}
			failingBill := buildRealBillingServiceWithAudit(t, env.db, failingAudit)
			failingMoveOut := buildRealMoveOutService(t, env.db, failingBill)

			_, err := failingMoveOut.CorrectSettlement(env.ctx, env.noticeID,
				moveout.CorrectSettlementRequest{CorrectionReason: "rollback probe"}, nil)
			if err == nil {
				t.Fatal("expected error from failing audit, got nil")
			}

			// ── Notice, old settlement, absorbed monthly, totals: ALL
			//     byte-identical. The strongest invariant the test pins:
			//     no matter WHICH audit fires, tx rolls everything back. ──
			assertSnapshotUnchanged(t, env, snapshot)

			// ── No new DRAFT settlement leaked into the DB. ──
			//     (In the fail_on_create_from_correction case this is the
			//     critical assertion — the INSERT already ran inside the
			//     tx; only ROLLBACK unwinds it.)
			var draftCount int64
			if err := env.db.Model(&Bill{}).
				Where("contract_id = ? AND bill_type = ? AND status = ?",
					env.contractID, BillTypeSettlement, BillStatusDraft).
				Count(&draftCount).Error; err != nil {
				t.Fatalf("count drafts: %v", err)
			}
			if draftCount != 0 {
				t.Errorf("draft settlement count after rollback = %d, want 0 (no new bill must leak)", draftCount)
			}

			// ── No SUPERSEDE or CREATE_FROM_CORRECTION audit rows. The
			//     SUPERSEDE row was already written by recordAudit BEFORE
			//     the CREATE_FROM_CORRECTION failure in the long path —
			//     tx rollback must wipe it. ──
			var correctionAuditCount int64
			if err := env.db.Model(&BillAuditLog{}).
				Where("action IN ?", []BillAuditAction{AuditSupersede, AuditCreateFromCorrection}).
				Count(&correctionAuditCount).Error; err != nil {
				t.Fatalf("count correction audits: %v", err)
			}
			if correctionAuditCount != 0 {
				t.Errorf("correction audit rows after rollback = %d, want 0", correctionAuditCount)
			}
		})
	}
}

// ============================================================
// 8. AuditEventShape
//
// Pin the audit-row shape: SUPERSEDE on old bill + CREATE_FROM_CORRECTION
// on new bill, both with correctly-typed payloads pointing at each other.
// This is the forensic contract the audit feed depends on.
// ============================================================

func TestSettlementCorrection_AuditEventShape(t *testing.T) {
	env := newCorrectionEnv(t, envOptions{targetStatus: moveout.MoveOutStatusPendingPayment})

	reason := "ผู้เช่าทักท้วง ขอตรวจสอบ"
	_, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: reason}, nil)
	if err != nil {
		t.Fatalf("CorrectSettlement: %v", err)
	}

	// Find the new bill via notice rebind.
	var notice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&notice).Error; err != nil {
		t.Fatalf("re-fetch notice: %v", err)
	}
	if notice.SettlementBillID == nil {
		t.Fatal("notice has no settlement bill after correction")
	}
	newBillID := *notice.SettlementBillID

	// ── Fetch + classify audit rows for the correction event pair ──
	var rows []BillAuditLog
	if err := env.db.Where("bill_id IN ? AND action IN ?",
		[]uuid.UUID{env.settlementBillID, newBillID},
		[]BillAuditAction{AuditSupersede, AuditCreateFromCorrection}).
		Order("action ASC").Find(&rows).Error; err != nil {
		t.Fatalf("fetch audit rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("correction audit row count = %d, want 2 (SUPERSEDE + CREATE_FROM_CORRECTION)", len(rows))
	}

	var supersede, createFrom *BillAuditLog
	for i := range rows {
		switch rows[i].Action {
		case AuditSupersede:
			supersede = &rows[i]
		case AuditCreateFromCorrection:
			createFrom = &rows[i]
		}
	}
	if supersede == nil {
		t.Fatal("missing SUPERSEDE audit row")
	}
	if createFrom == nil {
		t.Fatal("missing CREATE_FROM_CORRECTION audit row")
	}

	// ── SUPERSEDE attaches to OLD bill, payload points at NEW ──
	if supersede.BillID != env.settlementBillID {
		t.Errorf("SUPERSEDE.bill_id = %v, want %v (old)", supersede.BillID, env.settlementBillID)
	}
	var supPayload AuditSupersedePayload
	if err := json.Unmarshal([]byte(supersede.Payload), &supPayload); err != nil {
		t.Fatalf("unmarshal SUPERSEDE payload: %v", err)
	}
	if supPayload.PreviousStatus != string(BillStatusFinalized) {
		t.Errorf("SUPERSEDE.previous_status = %q, want FINALIZED", supPayload.PreviousStatus)
	}
	if supPayload.NewBillID != newBillID {
		t.Errorf("SUPERSEDE.new_bill_id = %v, want %v", supPayload.NewBillID, newBillID)
	}
	if supPayload.CorrectionReason != reason {
		t.Errorf("SUPERSEDE.correction_reason = %q, want %q", supPayload.CorrectionReason, reason)
	}

	// ── CREATE_FROM_CORRECTION attaches to NEW bill, payload points at OLD ──
	if createFrom.BillID != newBillID {
		t.Errorf("CREATE_FROM_CORRECTION.bill_id = %v, want %v (new)", createFrom.BillID, newBillID)
	}
	var cfcPayload AuditCreateFromCorrectionPayload
	if err := json.Unmarshal([]byte(createFrom.Payload), &cfcPayload); err != nil {
		t.Fatalf("unmarshal CREATE_FROM_CORRECTION payload: %v", err)
	}
	if cfcPayload.SupersededBillID != env.settlementBillID {
		t.Errorf("CREATE_FROM_CORRECTION.superseded_bill_id = %v, want %v",
			cfcPayload.SupersededBillID, env.settlementBillID)
	}
	if cfcPayload.CorrectionReason != reason {
		t.Errorf("CREATE_FROM_CORRECTION.correction_reason = %q, want %q",
			cfcPayload.CorrectionReason, reason)
	}

	// ── actor_id is nil (test passes nil actor — exercises the
	//     system-event path; UI-driven calls pass a real user ID) ──
	if supersede.ActorID != nil {
		t.Errorf("SUPERSEDE.actor_id = %v, want nil (test passed nil actor)", supersede.ActorID)
	}
	if createFrom.ActorID != nil {
		t.Errorf("CREATE_FROM_CORRECTION.actor_id = %v, want nil", createFrom.ActorID)
	}
}

// ============================================================
// Shared assertion helpers
// ============================================================

// assertOldSettlementVoided verifies the old settlement bill is VOID with
// void_reason=CORRECTION and superseded_by_bill_id pointing at the new bill.
func assertOldSettlementVoided(t *testing.T, env *correctionEnv) {
	t.Helper()
	var old Bill
	if err := env.db.Where("id = ?", env.settlementBillID).First(&old).Error; err != nil {
		t.Fatalf("re-fetch old settlement: %v", err)
	}
	if !old.IsVoid() {
		t.Errorf("old settlement status = %s, want VOID", old.Status)
	}
	if old.VoidReason == nil || *old.VoidReason != "CORRECTION" {
		t.Errorf("old settlement void_reason = %v, want CORRECTION", old.VoidReason)
	}
	if old.SupersededByBillID == nil {
		t.Errorf("old settlement superseded_by_bill_id = nil, want pointer to new bill")
	}
}

// assertNewDraftAttached verifies the notice points at a fresh DRAFT
// settlement (different ID from the old one) and the status downgraded
// to PENDING_SETTLEMENT.
func assertNewDraftAttached(t *testing.T, env *correctionEnv, scenarioLabel string) {
	t.Helper()
	var notice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&notice).Error; err != nil {
		t.Fatalf("[%s] re-fetch notice: %v", scenarioLabel, err)
	}
	if notice.Status != moveout.MoveOutStatusPendingSettlement {
		t.Errorf("[%s] notice.status = %s, want PENDING_SETTLEMENT (workflow rewound)",
			scenarioLabel, notice.Status)
	}
	if notice.SettlementBillID == nil {
		t.Fatalf("[%s] notice.settlement_bill_id = nil after correction", scenarioLabel)
	}
	if *notice.SettlementBillID == env.settlementBillID {
		t.Errorf("[%s] notice still points at OLD bill — rebinding failed", scenarioLabel)
	}
	if notice.NetAmount == nil {
		t.Errorf("[%s] notice.net_amount = nil, want recomputed value", scenarioLabel)
	}
	// New bill is DRAFT
	var newBill Bill
	if err := env.db.Where("id = ?", *notice.SettlementBillID).First(&newBill).Error; err != nil {
		t.Fatalf("[%s] re-fetch new bill: %v", scenarioLabel, err)
	}
	if newBill.Status != BillStatusDraft {
		t.Errorf("[%s] new bill status = %s, want DRAFT", scenarioLabel, newBill.Status)
	}
	if newBill.BillType != BillTypeSettlement {
		t.Errorf("[%s] new bill type = %s, want SETTLEMENT", scenarioLabel, newBill.BillType)
	}
}

// assertPaymentMetadataCleared verifies all three payment fields are nil
// (PaymentOutcome, PaymentMethod) or empty (PaymentNote).
func assertPaymentMetadataCleared(t *testing.T, env *correctionEnv) {
	t.Helper()
	var notice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&notice).Error; err != nil {
		t.Fatalf("re-fetch notice: %v", err)
	}
	if notice.PaymentOutcome != nil {
		t.Errorf("payment_outcome = %v, want nil", notice.PaymentOutcome)
	}
	if notice.PaymentMethod != nil {
		t.Errorf("payment_method = %v, want nil", notice.PaymentMethod)
	}
	if notice.PaymentNote != "" {
		t.Errorf("payment_note = %q, want \"\"", notice.PaymentNote)
	}
}

// stateSnapshot captures every field a rollback test needs to compare.
// Line-item counts + Overrides are captured alongside Bill row state so a
// regression that touches related rows without flipping the parent Bill
// (e.g., a buggy "restore" that delete+re-inserts line items on rollback)
// still trips the assertSnapshotUnchanged check.
type stateSnapshot struct {
	notice                  moveout.MoveOutNotice
	oldSettlement           Bill
	oldSettlementLineCount  int64 // line-items on old settlement (catches partial INSERT/DELETE leaks)
	absorbedMonthly         *Bill // nil if env has no absorbed bill
	absorbedMonthlyLineCount int64
	billsTotal              int64
	billAuditTotal          int64
}

func snapshotNoticeAndBills(t *testing.T, env *correctionEnv) stateSnapshot {
	t.Helper()
	var s stateSnapshot
	if err := env.db.Where("id = ?", env.noticeID).First(&s.notice).Error; err != nil {
		t.Fatalf("snapshot notice: %v", err)
	}
	if err := env.db.Where("id = ?", env.settlementBillID).First(&s.oldSettlement).Error; err != nil {
		t.Fatalf("snapshot old settlement: %v", err)
	}
	if err := env.db.Model(&BillLineItem{}).
		Where("bill_id = ?", env.settlementBillID).
		Count(&s.oldSettlementLineCount).Error; err != nil {
		t.Fatalf("snapshot old settlement line items: %v", err)
	}
	if env.absorbedMonthlyBillID != uuid.Nil {
		var m Bill
		if err := env.db.Where("id = ?", env.absorbedMonthlyBillID).First(&m).Error; err != nil {
			t.Fatalf("snapshot absorbed monthly: %v", err)
		}
		s.absorbedMonthly = &m
		if err := env.db.Model(&BillLineItem{}).
			Where("bill_id = ?", env.absorbedMonthlyBillID).
			Count(&s.absorbedMonthlyLineCount).Error; err != nil {
			t.Fatalf("snapshot absorbed monthly line items: %v", err)
		}
	}
	// Both totals use Unscoped so soft-deleted rows count too — symmetry
	// guards against a regression that soft-deletes mid-tx without
	// rolling back, on either bills or audit_log.
	if err := env.db.Unscoped().Model(&Bill{}).Count(&s.billsTotal).Error; err != nil {
		t.Fatalf("snapshot bills count: %v", err)
	}
	if err := env.db.Unscoped().Model(&BillAuditLog{}).Count(&s.billAuditTotal).Error; err != nil {
		t.Fatalf("snapshot audit count: %v", err)
	}
	return s
}

func assertSnapshotUnchanged(t *testing.T, env *correctionEnv, before stateSnapshot) {
	t.Helper()
	after := snapshotNoticeAndBills(t, env)

	// Notice fields
	if after.notice.Status != before.notice.Status {
		t.Errorf("notice.status changed: was %s, now %s", before.notice.Status, after.notice.Status)
	}
	if !equalUUIDPtr(after.notice.SettlementBillID, before.notice.SettlementBillID) {
		t.Errorf("notice.settlement_bill_id changed: was %v, now %v",
			before.notice.SettlementBillID, after.notice.SettlementBillID)
	}
	if !equalInt64Ptr(after.notice.NetAmount, before.notice.NetAmount) {
		t.Errorf("notice.net_amount changed: was %v, now %v",
			before.notice.NetAmount, after.notice.NetAmount)
	}
	if !equalOutcomePtr(after.notice.PaymentOutcome, before.notice.PaymentOutcome) {
		t.Errorf("notice.payment_outcome changed: was %v, now %v",
			before.notice.PaymentOutcome, after.notice.PaymentOutcome)
	}
	if !equalMethodPtr(after.notice.PaymentMethod, before.notice.PaymentMethod) {
		t.Errorf("notice.payment_method changed: was %v, now %v",
			before.notice.PaymentMethod, after.notice.PaymentMethod)
	}
	if after.notice.PaymentNote != before.notice.PaymentNote {
		t.Errorf("notice.payment_note changed: was %q, now %q",
			before.notice.PaymentNote, after.notice.PaymentNote)
	}

	// Old settlement
	if after.oldSettlement.Status != before.oldSettlement.Status {
		t.Errorf("old settlement status changed: was %s, now %s",
			before.oldSettlement.Status, after.oldSettlement.Status)
	}
	if !equalStringPtr(after.oldSettlement.VoidReason, before.oldSettlement.VoidReason) {
		t.Errorf("old settlement void_reason changed: was %v, now %v",
			before.oldSettlement.VoidReason, after.oldSettlement.VoidReason)
	}
	if !equalUUIDPtr(after.oldSettlement.SupersededByBillID, before.oldSettlement.SupersededByBillID) {
		t.Errorf("old settlement superseded_by_bill_id changed: was %v, now %v",
			before.oldSettlement.SupersededByBillID, after.oldSettlement.SupersededByBillID)
	}
	if after.oldSettlementLineCount != before.oldSettlementLineCount {
		t.Errorf("old settlement line-item count changed: was %d, now %d (rollback failed to undo INSERT/DELETE)",
			before.oldSettlementLineCount, after.oldSettlementLineCount)
	}

	// Absorbed monthly (if any)
	if before.absorbedMonthly != nil {
		if after.absorbedMonthly == nil {
			t.Fatal("absorbed monthly disappeared from DB")
		}
		if after.absorbedMonthly.Status != before.absorbedMonthly.Status {
			t.Errorf("absorbed monthly status changed: was %s, now %s",
				before.absorbedMonthly.Status, after.absorbedMonthly.Status)
		}
		if !equalStringPtr(after.absorbedMonthly.VoidReason, before.absorbedMonthly.VoidReason) {
			t.Errorf("absorbed monthly void_reason changed: was %v, now %v",
				before.absorbedMonthly.VoidReason, after.absorbedMonthly.VoidReason)
		}
		if after.absorbedMonthlyLineCount != before.absorbedMonthlyLineCount {
			t.Errorf("absorbed monthly line-item count changed: was %d, now %d (rollback failed to undo INSERT/DELETE)",
				before.absorbedMonthlyLineCount, after.absorbedMonthlyLineCount)
		}
	}

	// Totals — catches phantom rows from partial commits. Both Unscoped
	// for symmetry so a soft-delete-mid-tx leak on either table trips here.
	if after.billsTotal != before.billsTotal {
		t.Errorf("total bill rows changed: was %d, now %d (rollback failed to undo INSERT)",
			before.billsTotal, after.billsTotal)
	}
	if after.billAuditTotal != before.billAuditTotal {
		t.Errorf("total audit rows changed: was %d, now %d (rollback failed to undo audit INSERT)",
			before.billAuditTotal, after.billAuditTotal)
	}
}

// --- Pointer equality helpers (Go has no generic equal-with-nil-aware) ---

func equalUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalOutcomePtr(a, b *moveout.PaymentOutcome) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalMethodPtr(a, b *domain.PaymentMethod) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
