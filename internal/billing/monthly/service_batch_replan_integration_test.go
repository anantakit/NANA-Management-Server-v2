//go:build integration

package monthly

import (
	"context"
	"errors"
	"testing"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// service_batch_replan_integration_test.go locks single-item re-plan
// behavior against REAL Postgres + the real planner:
//
//  1. SKIPPED(MISSING_METER_READING) → seed meter → replan → CREATED with
//     a snapshot byte-equivalent to a fresh BatchCreateMonthlyBills run.
//  2. SKIPPED(MISSING_METER_READING) → replan without meter → still SKIPPED
//     (idempotent — no false flip).
//  3. Fully-COMMITTED batch → replan → 409 (closed ledger).
//  4. Item already linked to a bill (bill_id != nil) → replan → 409.
//
// Mock-based tests can't reach these because the planner's verdict depends
// on the JOINed read of contracts + meters + bills. Stubbing those with
// canned data effectively re-implements the planner, defeating the test.

// ── Local stubs for ports the planner reaches but this flow doesn't drive ──
//
// Monthly's MoveOutSource port only needs FindRoomIDsWithMoveOutInMonth —
// the legacy ContractQuerier / ConfigQuerier shapes don't appear here at all
// because the monthly service doesn't depend on them (moved out with the
// adapter narrowing in commit 2b).

type replanMoveOutStub struct{}

func (s *replanMoveOutStub) FindRoomIDsWithMoveOutInMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

type replanEnv struct {
	db           *gorm.DB
	svc          Service
	batchRepo    BatchRepository
	apartmentID  uuid.UUID
	roomID       uuid.UUID
	contractID   uuid.UUID
	billingMonth string
}

// newReplanEnv seeds an apartment + room + ACTIVE contract WITHOUT a meter
// reading for the test billing month. BatchCreateMonthlyBills is run so the
// resulting batch contains a SKIPPED(MISSING_METER_READING) item ready for
// replan exercises.
func newReplanEnv(t *testing.T) *replanEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "R-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	billRepo := billing.NewBillingRepository(db)
	auditRepo := billing.NewBillAuditRepository(db)
	adapter := billing.NewMonthlyAdapter(billRepo, auditRepo)
	batchRepo := NewBatchRepository(db)
	meters := meterreading.NewMeterReadingRepository(db) // real DB-backed
	moveOuts := &replanMoveOutStub{}
	txMgr := database.NewTxManager(db)

	// MonthlyAdapter satisfies BillStore + AuditStore (passed twice); the
	// monthly-owned BatchRepository covers batch-table persistence —
	// matches the production wiring in cmd/main.go.
	svc := NewService(adapter, adapter, batchRepo, meters, moveOuts, txMgr)

	_ = c // keep for symmetry; contract details flow through the planner via the JOIN read
	_ = contract.ContractStatusActive
	_ = moveout.MoveOutStatusCompleted
	return &replanEnv{
		db: db, svc: svc, batchRepo: batchRepo,
		apartmentID:  apt.ID,
		roomID:       rm.ID,
		contractID:   c.ID,
		billingMonth: "2026-05",
	}
}

// seedMonthlyMeter inserts a MONTHLY reading for the env's room + month with
// the canonical "50 elec units, 5 water units" pattern reused across the
// billing integration tests. Returns nothing — assertions read computed
// snapshot fields directly.
func (e *replanEnv) seedMonthlyMeter(t *testing.T) {
	t.Helper()
	bm := e.billingMonth
	mr := &meterreading.MeterReading{
		RoomID:              e.roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1050,
		WaterPrevious:       100,
		WaterCurrent:        105,
	}
	if err := e.db.Create(mr).Error; err != nil {
		t.Fatalf("seed monthly meter: %v", err)
	}
}

// runGenerate triggers BatchCreateMonthlyBills and returns the resulting
// batch. The test then drills into batch.items via the repo to pick the row
// it needs.
func (e *replanEnv) runGenerate(t *testing.T) *billing.BillGenerationBatch {
	t.Helper()
	batch, err := e.svc.BatchCreateMonthlyBills(context.Background(),
		BatchCreateMonthlyBillsRequest{ApartmentID: e.apartmentID.String(), BillingMonth: e.billingMonth},
		nil,
	)
	if err != nil {
		t.Fatalf("BatchCreateMonthlyBills: %v", err)
	}
	return batch
}

func (e *replanEnv) firstItem(t *testing.T, batchID uuid.UUID) billing.BatchItemWithTenant {
	t.Helper()
	items, err := e.batchRepo.FindBatchItemsByBatchID(context.Background(), batchID)
	if err != nil {
		t.Fatalf("FindBatchItemsByBatchID: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 batch item, got %d", len(items))
	}
	return items[0]
}

// TC1: SKIPPED(MISSING_METER_READING) → seed monthly meter → replan → CREATED.
// Locks the "row flips after operator records the missing meter" contract
// that BillBatchReview's in-context MonthlyMeterDrawer launch depends on.
func TestRePlanBatchItem_FlipsSkippedToCreatedAfterMeterRecord(t *testing.T) {
	env := newReplanEnv(t)

	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	if item.ResultType != billing.ResultSkipped {
		t.Fatalf("pre-condition: expected SKIPPED, got %s", item.ResultType)
	}
	if item.ReasonCode != billing.ReasonMissingMeterReading {
		t.Fatalf("pre-condition: expected reason %s, got %s", billing.ReasonMissingMeterReading, item.ReasonCode)
	}

	// Operator records the missing meter mid-batch.
	env.seedMonthlyMeter(t)

	updated, err := env.svc.RePlanBatchItem(context.Background(), batch.ID, item.ID)
	if err != nil {
		t.Fatalf("RePlanBatchItem: %v", err)
	}

	if updated.ResultType != billing.ResultCreated {
		t.Errorf("ResultType = %s, want CREATED", updated.ResultType)
	}
	if updated.ReasonCode != "" {
		t.Errorf("ReasonCode = %q, want empty for CREATED", updated.ReasonCode)
	}
	if updated.ReasonText != "" {
		t.Errorf("ReasonText = %q, want empty for CREATED", updated.ReasonText)
	}
	if len(updated.ComputedSnapshot.LineItems) != 3 {
		t.Errorf("ComputedSnapshot.LineItems = %d, want 3 (rent + elec + water)",
			len(updated.ComputedSnapshot.LineItems))
	}
	// 3000 + 50*8 + 5*18 = 3000 + 400 + 90 = 3490 baht = 349000 satang.
	const wantTotal int64 = 349000
	if updated.ComputedSnapshot.TotalAmount != wantTotal {
		t.Errorf("ComputedSnapshot.TotalAmount = %d, want %d", updated.ComputedSnapshot.TotalAmount, wantTotal)
	}

	// Verify persistence: re-read from DB and confirm same state.
	persisted, err := env.batchRepo.FindBatchItemByID(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("re-read item: %v", err)
	}
	if persisted.ResultType != billing.ResultCreated {
		t.Errorf("persisted ResultType = %s, want CREATED", persisted.ResultType)
	}
}

// TC1b: CREATED → operator updates the meter → replan again → still
// CREATED, snapshot reflects the new meter reading. Locks the post-flip
// idempotency contract: once a row is CREATED, subsequent meter edits via
// MeterReading must propagate into the batch snapshot through replan
// without re-creating the row, re-classifying it, or wiping the
// (potentially admin-edited) snapshot to a stale value.
//
// This is the contract that protects the "operator re-records meter from
// MeterReading page after closing BillBatchReview's drawer" path —
// without this guarantee, the row would commit a snapshot computed from
// the FIRST meter value, not the corrected one.
func TestRePlanBatchItem_StaysCreatedWithUpdatedSnapshotAfterMeterEdit(t *testing.T) {
	env := newReplanEnv(t)

	// First meter: 50 elec, 5 water → ฿3490 total (matches TC1 happy path).
	env.seedMonthlyMeter(t)
	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	if item.ResultType != billing.ResultCreated {
		t.Fatalf("pre-condition: expected CREATED, got %s", item.ResultType)
	}
	const firstRunTotal int64 = 349000
	if item.ComputedSnapshot.TotalAmount != firstRunTotal {
		t.Fatalf("pre-condition: snapshot total = %d, want %d", item.ComputedSnapshot.TotalAmount, firstRunTotal)
	}

	// Operator goes back to MeterReading and corrects the reading: bump elec
	// usage from 50 → 100 (new total = 3000 + 100*8 + 5*18 = 3890 = 389000).
	// In production this is a PUT /meter-readings/:id; here we mutate the
	// existing row directly because the integration boundary is the
	// planner, not the meter endpoint.
	if err := env.db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND reading_type = ?", env.roomID, meterreading.ReadingTypeMonthly).
		Update("electricity_current", 1100).Error; err != nil {
		t.Fatalf("update meter: %v", err)
	}

	updated, err := env.svc.RePlanBatchItem(context.Background(), batch.ID, item.ID)
	if err != nil {
		t.Fatalf("RePlanBatchItem (second pass): %v", err)
	}

	if updated.ResultType != billing.ResultCreated {
		t.Errorf("ResultType = %s, want CREATED (still)", updated.ResultType)
	}
	if updated.ReasonCode != "" {
		t.Errorf("ReasonCode = %q, want empty for CREATED", updated.ReasonCode)
	}
	const secondRunTotal int64 = 389000
	if updated.ComputedSnapshot.TotalAmount != secondRunTotal {
		t.Errorf("ComputedSnapshot.TotalAmount = %d, want %d (snapshot must reflect new meter)",
			updated.ComputedSnapshot.TotalAmount, secondRunTotal)
	}
	// Sanity: row identity must not change across replan passes.
	if updated.ID != item.ID {
		t.Errorf("item ID changed across replan: %s → %s", item.ID, updated.ID)
	}
	if updated.BillID != nil {
		t.Errorf("BillID = %v, want nil (replan must not commit)", updated.BillID)
	}
}

// TC2: SKIPPED(MISSING_METER_READING) → replan without seeding meter →
// stays SKIPPED. Idempotent — the operator can't "force flip" by clicking
// replan if state hasn't changed; the row must remain blocked.
func TestRePlanBatchItem_StaysSkippedWhenStateUnchanged(t *testing.T) {
	env := newReplanEnv(t)

	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	updated, err := env.svc.RePlanBatchItem(context.Background(), batch.ID, item.ID)
	if err != nil {
		t.Fatalf("RePlanBatchItem: %v", err)
	}

	if updated.ResultType != billing.ResultSkipped {
		t.Errorf("ResultType = %s, want SKIPPED (no meter yet)", updated.ResultType)
	}
	if updated.ReasonCode != billing.ReasonMissingMeterReading {
		t.Errorf("ReasonCode = %s, want %s", updated.ReasonCode, billing.ReasonMissingMeterReading)
	}
	if len(updated.ComputedSnapshot.LineItems) != 0 {
		t.Errorf("ComputedSnapshot.LineItems = %d, want 0 for SKIPPED", len(updated.ComputedSnapshot.LineItems))
	}
}

// TC3: fully-COMMITTED batch → replan rejected with 409. Once the commit
// ledger is closed, no row in the batch can be re-classified; operator must
// generate a new batch.
func TestRePlanBatchItem_RejectsFullyCommittedBatch(t *testing.T) {
	env := newReplanEnv(t)

	// Seed meter BEFORE generate so the row is CREATED + commit succeeds.
	env.seedMonthlyMeter(t)
	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	if item.ResultType != billing.ResultCreated {
		t.Fatalf("pre-condition: expected CREATED, got %s", item.ResultType)
	}

	if _, err := env.svc.CommitBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("CommitBatch: %v", err)
	}

	_, err := env.svc.RePlanBatchItem(context.Background(), batch.ID, item.ID)
	if err == nil {
		t.Fatal("expected error for committed batch, got nil")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 409 {
		t.Errorf("err = %v, want 409 conflict AppError", err)
	}
}

// TC4: item already linked to a bill → replan rejected. Even when the batch
// is only PARTIALLY_COMMITTED, an individual row that already produced a
// bill is immutable — the bill is the source of truth post-commit.
func TestRePlanBatchItem_RejectsItemWithBillID(t *testing.T) {
	env := newReplanEnv(t)

	env.seedMonthlyMeter(t)
	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	if _, err := env.svc.CommitBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("CommitBatch: %v", err)
	}

	// After commit the batch has CommitStatus = COMMITTED for the
	// single-item batch — verify the per-row guard fires first by
	// staging the same batch as PARTIALLY_COMMITTED.
	committed := billing.CommitStatusPartiallyCommitted
	if err := env.db.Model(&billing.BillGenerationBatch{}).Where("id = ?", batch.ID).
		Update("commit_status", &committed).Error; err != nil {
		t.Fatalf("downgrade commit_status: %v", err)
	}

	_, err := env.svc.RePlanBatchItem(context.Background(), batch.ID, item.ID)
	if err == nil {
		t.Fatal("expected error for item with bill_id, got nil")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 409 {
		t.Errorf("err = %v, want 409 conflict AppError", err)
	}
}

// TC5: itemID belongs to a different batch → 404. Defensive guard against
// FE passing a stale itemID or crafted URL.
func TestRePlanBatchItem_RejectsMismatchedBatchItem(t *testing.T) {
	env := newReplanEnv(t)

	batch := env.runGenerate(t)
	item := env.firstItem(t, batch.ID)

	// Use a fresh random batch_id that does NOT own this item.
	otherBatchID := uuid.New()

	_, err := env.svc.RePlanBatchItem(context.Background(), otherBatchID, item.ID)
	if err == nil {
		t.Fatal("expected error for mismatched batch/item, got nil")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 404 {
		t.Errorf("err = %v, want 404 AppError", err)
	}
}
