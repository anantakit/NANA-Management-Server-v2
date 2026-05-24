//go:build integration

package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// service_correction_integration_test.go locks the void+recreate
// correction flow against REAL Postgres invariants that the mock-based
// service_correction_test.go cannot reach:
//
//   1. Partial UNIQUE INDEX idx_bills_unique_monthly — only one non-VOID
//      monthly bill per (contract_id, billing_month). The reorder
//      "void old → create new" satisfies it; the previous order didn't.
//   2. DEFERRABLE INITIALLY DEFERRED FK on superseded_by_bill_id — old
//      links to new BEFORE new is inserted. The FK fires at COMMIT.
//   3. SELECT FOR UPDATE row-lock serializing concurrent corrections on
//      the same bill (the loser sees ErrAlreadyVoided after the winner
//      commits).
//   4. Audit-failure rollback restoring pre-correction state byte-exact.
//
// The DB constraint is the load-bearing validator of the immutable-
// document domain model: the old document stops being active BEFORE the
// replacement becomes active. The unique index encodes that invariant
// in the schema. Any refactor that re-orders the service writes will
// trip these tests instantly.
//
// Mock tests caught NONE of these — mockBillingRepo doesn't model partial
// unique indexes, FK timing, row-locks, or real transactional semantics.
// See post-mortem in project_billing_correction_arch_lock.md.

// ── Integration ports: stubs for contract / config / moveout (correction
//    flow only needs ContractQuerier.FindByIDSimple); real DB-backed
//    meter repo because the regenerate step queries it. ──

type integrationContractStub struct{ c *contract.Contract }

func (s *integrationContractStub) FindByIDSimple(_ context.Context, _ uuid.UUID) (*contract.Contract, error) {
	return s.c, nil
}

type integrationConfigStub struct{}

func (s *integrationConfigStub) FindByApartmentID(_ context.Context, _ uuid.UUID) ([]billingconfig.BillingConfig, error) {
	return nil, nil
}

type integrationMoveOutStub struct{}

func (s *integrationMoveOutStub) FindActiveByContractID(_ context.Context, _ uuid.UUID) (*moveout.MoveOutNotice, error) {
	return nil, gorm.ErrRecordNotFound
}
func (s *integrationMoveOutStub) FindRoomIDsWithMoveOutInMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

// correctionTestEnv is the shared scaffolding for every test in this file.
// Provides a wired-up real BillingService backed by Postgres + a seeded
// FINALIZED MONTHLY bill ready to be corrected.
type correctionTestEnv struct {
	db        *gorm.DB
	svc       BillingService
	billRepo  BillingRepository
	auditRepo BillAuditRepository
	c         *contract.Contract
	roomID    uuid.UUID
	oldBill   *Bill
}

func newCorrectionTestEnv(t *testing.T, audit BillAuditRepository) *correctionTestEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "C-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	// Seed a MONTHLY meter matching what computeMonthlyBillSnapshot will
	// produce: 50 elec units * ฿8 = ฿400, 5 water units * ฿18 = ฿90,
	// rent ฿3000 → total ฿3490 (349000 satang).
	billingMonth := "2026-05"
	bm := billingMonth
	mr := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1050,
		WaterPrevious:       100,
		WaterCurrent:        105,
	}
	if err := db.Create(mr).Error; err != nil {
		t.Fatalf("seed monthly meter: %v", err)
	}

	billRepo := NewBillingRepository(db)
	auditRepo := audit
	if auditRepo == nil {
		auditRepo = NewBillAuditRepository(db)
	}
	contracts := &integrationContractStub{c: c}
	meters := meterreading.NewMeterReadingRepository(db) // real DB-backed
	configs := &integrationConfigStub{}
	moveOuts := &integrationMoveOutStub{}
	txMgr := database.NewTxManager(db)
	svc := NewBillingService(billRepo, auditRepo, contracts, meters, configs, moveOuts, txMgr)

	// Seed the FINALIZED MONTHLY bill that tests will correct.
	// TotalAmount intentionally wrong (999999) so we can prove the new
	// DRAFT is regenerated (349000), not cloned.
	billID := uuid.New()
	old := &Bill{
		ID:           billID,
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  999999,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าห้อง (เดิม ผิด)", Amount: 999999, SortOrder: 1},
		},
	}
	if err := db.Create(old).Error; err != nil {
		t.Fatalf("seed finalized bill: %v", err)
	}

	return &correctionTestEnv{
		db: db, svc: svc, billRepo: billRepo, auditRepo: auditRepo,
		c: c, roomID: rm.ID, oldBill: old,
	}
}

// ============================================================
// TC1+2+3+4+5: happy-path covers state transition, unique-index OK,
//              new DRAFT regenerated, deferred FK passes at COMMIT.
//
// These 5 invariants all observe the same successful CorrectBill call;
// splitting them into separate tests would duplicate setup without adding
// signal. Each assert block is labelled with the invariant it pins.
// ============================================================

func TestCorrectBill_Integration_HappyPath(t *testing.T) {
	env := newCorrectionTestEnv(t, nil)
	// Pass nil actor — bill_audit_log.actor_id is nullable (ON DELETE
	// SET NULL) and seeding a real user just to satisfy the FK adds noise
	// without testing anything. The audit recorder accepts nil for
	// system-triggered events; same shape works here.
	var actor *uuid.UUID
	reason := "ค่าไฟผิดเดือนนี้ ออกใหม่"

	newBill, err := env.svc.CorrectBill(context.Background(), env.oldBill.ID,
		CorrectBillRequest{CorrectionReason: reason}, actor)
	if err != nil {
		t.Fatalf("CorrectBill: %v", err)
	}

	// ─── Invariant 4: partial UNIQUE INDEX idx_bills_unique_monthly held.
	//     If the constraint had fired, CorrectBill would return 500 with
	//     "duplicate key value violates unique constraint". The fact that
	//     it returned a new DRAFT proves the void→create order satisfies
	//     the partial index (at every statement, at most one non-VOID
	//     monthly bill per (contract_id, billing_month) exists). ───
	if newBill == nil {
		t.Fatal("newBill is nil — correction silently failed?")
	}

	// ─── Invariant 5: DEFERRABLE FK passed at COMMIT.
	//     old.superseded_by_bill_id was set BEFORE the new row existed.
	//     If the FK weren't DEFERRED, the UPDATE on old would have failed
	//     mid-tx. Reaching this point with a returned new bill proves
	//     the FK check fired at COMMIT (when newBill exists) ───

	// Re-fetch the OLD bill from DB — service mutated it in the same tx.
	var oldAfter Bill
	if err := env.db.Where("id = ?", env.oldBill.ID).First(&oldAfter).Error; err != nil {
		t.Fatalf("re-fetch old: %v", err)
	}

	// ─── Invariant 2: old FINALIZED → VOID(CORRECTION) + superseded_by ───
	if !oldAfter.IsVoid() {
		t.Errorf("old.status = %s, want VOID", oldAfter.Status)
	}
	if oldAfter.VoidReason == nil || *oldAfter.VoidReason != "CORRECTION" {
		t.Errorf("old.void_reason = %v, want CORRECTION", oldAfter.VoidReason)
	}
	if oldAfter.SupersededByBillID == nil || *oldAfter.SupersededByBillID != newBill.ID {
		t.Errorf("old.superseded_by_bill_id = %v, want %v", oldAfter.SupersededByBillID, newBill.ID)
	}

	// ─── Invariant 3: new DRAFT inserted with regenerated totals ───
	if newBill.Status != BillStatusDraft {
		t.Errorf("new.status = %s, want DRAFT", newBill.Status)
	}
	if newBill.BillType != BillTypeMonthly {
		t.Errorf("new.bill_type = %s, want MONTHLY", newBill.BillType)
	}
	if newBill.BatchID != nil {
		t.Errorf("new.batch_id = %v, want nil (correction = manual artifact)", newBill.BatchID)
	}
	// Regenerated from meter: rent 300000 + elec 50*800 + water 5*1800 = 349000
	if newBill.TotalAmount != 349000 {
		t.Errorf("new.total = %d, want 349000 (regenerated, NOT cloned 999999)", newBill.TotalAmount)
	}
	if len(newBill.LineItems) != 3 {
		t.Errorf("new.line_items = %d, want 3 (rent + elec + water)", len(newBill.LineItems))
	}

	// ─── Invariant 1: happy path returns new DRAFT identity ───
	if newBill.ID == env.oldBill.ID {
		t.Errorf("new.id == old.id — correction must produce a distinct bill, not mutate in place")
	}

	// ─── 2 audit events emitted, both in the same TX ───
	var auditRows []BillAuditLog
	if err := env.db.Where("bill_id IN ?", []uuid.UUID{env.oldBill.ID, newBill.ID}).
		Order("created_at ASC, action ASC").Find(&auditRows).Error; err != nil {
		t.Fatalf("fetch audit: %v", err)
	}
	if len(auditRows) != 2 {
		t.Fatalf("audit rows = %d, want 2 (SUPERSEDE + CREATE_FROM_CORRECTION)", len(auditRows))
	}
	gotActions := map[BillAuditAction]uuid.UUID{}
	for _, r := range auditRows {
		gotActions[r.Action] = r.BillID
	}
	if gotActions[AuditSupersede] != env.oldBill.ID {
		t.Errorf("SUPERSEDE bill_id = %v, want old %v", gotActions[AuditSupersede], env.oldBill.ID)
	}
	if gotActions[AuditCreateFromCorrection] != newBill.ID {
		t.Errorf("CREATE_FROM_CORRECTION bill_id = %v, want new %v", gotActions[AuditCreateFromCorrection], newBill.ID)
	}

	// ─── Sanity: both bills coexist in DB (old VOID, new DRAFT) on the
	//     same (contract_id, billing_month) — proves the partial unique
	//     index allows exactly this state (one VOID + one non-VOID). ───
	var coexistCount int64
	if err := env.db.Model(&Bill{}).
		Where("contract_id = ? AND billing_month = ?", env.c.ID, env.oldBill.BillingMonth).
		Count(&coexistCount).Error; err != nil {
		t.Fatalf("count coexist: %v", err)
	}
	if coexistCount != 2 {
		t.Errorf("coexist count = %d, want 2 (old VOID + new DRAFT)", coexistCount)
	}
}

// ============================================================
// TC6: concurrent second correction fails safely
//
// Two goroutines call CorrectBill on the SAME bill ID simultaneously.
// Row-lock serializes them: the winner succeeds, the loser sees the
// already-voided state via CanCorrect (ErrAlreadyVoided). Neither call
// 500s; the loser surfaces a clean 400 AppError.
// ============================================================

func TestCorrectBill_Integration_ConcurrentSecondFails(t *testing.T) {
	env := newCorrectionTestEnv(t, nil)
	// Pass nil actor — bill_audit_log.actor_id is nullable (ON DELETE
	// SET NULL) and seeding a real user just to satisfy the FK adds noise
	// without testing anything. The audit recorder accepts nil for
	// system-triggered events; same shape works here.
	var actor *uuid.UUID

	var (
		wg          sync.WaitGroup
		results     = make([]error, 2)
		newBills    = make([]*BillWithRelations, 2)
		startSignal = make(chan struct{})
	)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-startSignal // release together to maximize the race
			b, err := env.svc.CorrectBill(context.Background(), env.oldBill.ID,
				CorrectBillRequest{CorrectionReason: "race attempt"}, actor)
			results[i] = err
			newBills[i] = b
		}()
	}
	close(startSignal)
	wg.Wait()

	// Exactly one call must succeed.
	successCount := 0
	failCount := 0
	for i, err := range results {
		if err == nil {
			successCount++
			if newBills[i] == nil {
				t.Errorf("call[%d] returned nil bill with nil error", i)
			}
		} else {
			failCount++
		}
	}
	if successCount != 1 || failCount != 1 {
		t.Fatalf("expected 1 success + 1 fail, got %d success / %d fail; errors=%v",
			successCount, failCount, results)
	}

	// The losing call must surface a CanCorrect-level error (AlreadyVoided
	// or AlreadySuperseded depending on the timing window). Both are
	// 400-class AppErrors with Thai sentinel messages — never 500.
	for _, err := range results {
		if err == nil {
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "ยกเลิก") && !strings.Contains(msg, "แทนที่") {
			t.Errorf("loser error = %q, want CanCorrect sentinel (already voided / superseded)", msg)
		}
	}

	// Exactly ONE new bill exists in the DB (winner's). Audit rows = 2
	// (one SUPERSEDE + one CREATE_FROM_CORRECTION). Loser's tx rolled back
	// cleanly — no half-applied state.
	var newCount int64
	if err := env.db.Model(&Bill{}).
		Where("contract_id = ? AND billing_month = ? AND status = ?",
			env.c.ID, env.oldBill.BillingMonth, BillStatusDraft).
		Count(&newCount).Error; err != nil {
		t.Fatalf("count new draft: %v", err)
	}
	if newCount != 1 {
		t.Errorf("draft count after race = %d, want 1 (loser's tx must roll back)", newCount)
	}

	var auditCount int64
	if err := env.db.Model(&BillAuditLog{}).
		Where("action IN ?", []BillAuditAction{AuditSupersede, AuditCreateFromCorrection}).
		Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 2 {
		t.Errorf("audit count = %d, want 2 (only winner emits)", auditCount)
	}
}

// ============================================================
// TC7: audit failure rolls back the entire correction
//
// Inject a failing audit repo so SUPERSEDE write fails. The parent TX
// must roll back, leaving:
//   - old bill still FINALIZED with no superseded_by link
//   - no new bill in the DB
//   - no audit rows
//
// Mirrors TestUpdateMonthlyDraft_AuditFailureRollsBackDBState pattern.
// ============================================================

func TestCorrectBill_Integration_AuditFailureRollsBack(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	// First seed real state via the standard env (so old bill exists).
	realAudit := NewBillAuditRepository(db)
	failingAudit := &failingAuditRepo{
		wrapped: realAudit,
		err:     errors.New("audit-store-down (correction rollback probe)"),
	}
	env := newCorrectionTestEnv(t, failingAudit)
	// Pass nil actor — bill_audit_log.actor_id is nullable (ON DELETE
	// SET NULL) and seeding a real user just to satisfy the FK adds noise
	// without testing anything. The audit recorder accepts nil for
	// system-triggered events; same shape works here.
	var actor *uuid.UUID

	_, err := env.svc.CorrectBill(context.Background(), env.oldBill.ID,
		CorrectBillRequest{CorrectionReason: "rollback probe"}, actor)
	if err == nil {
		t.Fatal("expected error from failing audit, got nil")
	}

	// Old bill must be byte-identical to seeded state.
	var oldAfter Bill
	if err := env.db.Where("id = ?", env.oldBill.ID).First(&oldAfter).Error; err != nil {
		t.Fatalf("re-fetch old: %v", err)
	}
	if oldAfter.Status != BillStatusFinalized {
		t.Errorf("old.status after rollback = %s, want FINALIZED (audit fail must roll back the void)", oldAfter.Status)
	}
	if oldAfter.VoidReason != nil {
		t.Errorf("old.void_reason after rollback = %v, want nil", oldAfter.VoidReason)
	}
	if oldAfter.SupersededByBillID != nil {
		t.Errorf("old.superseded_by_bill_id after rollback = %v, want nil", oldAfter.SupersededByBillID)
	}
	if oldAfter.TotalAmount != 999999 {
		t.Errorf("old.total after rollback = %d, want 999999 (the seeded wrong value, untouched)", oldAfter.TotalAmount)
	}

	// Only the seeded bill exists — no new DRAFT leaked.
	var billCount int64
	if err := env.db.Model(&Bill{}).
		Where("contract_id = ?", env.c.ID).
		Count(&billCount).Error; err != nil {
		t.Fatalf("count bills: %v", err)
	}
	if billCount != 1 {
		t.Errorf("bill count after rollback = %d, want 1 (no new DRAFT must leak)", billCount)
	}

	// No audit rows persisted (the failing repo rejects + the tx rolls back).
	var auditCount int64
	if err := env.db.Model(&BillAuditLog{}).Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 0 {
		t.Errorf("audit count after rollback = %d, want 0", auditCount)
	}
}
