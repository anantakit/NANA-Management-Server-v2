package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// finalizeFixture builds a DRAFT monthly bill with one AUTO line item.
// status / line items are overridable per-test for the various paths
// (FINALIZED, VOID, empty line items, etc).
func finalizeFixture(billID uuid.UUID) Bill {
	return Bill{
		ID:           billID,
		ContractID:   uuid.New(),
		BillingMonth: "2026-05",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		TotalAmount:  500000,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 500000, SortOrder: 1},
		},
	}
}

// newBatchFinalizeService wires a service whose repo returns the given bills
// from ListBillsByBatchID and whose FindByID matches by id. Returns the audit
// repo so tests can assert exact emission counts (lock #10).
func newBatchFinalizeService(bills []Bill) (BillingService, *mockBillingRepo, *mockBillAuditRepo) {
	byID := make(map[uuid.UUID]*Bill, len(bills))
	for i := range bills {
		byID[bills[i].ID] = &bills[i]
	}
	repo := &mockBillingRepo{}
	repo.listBillsByBatchIDFn = func(_ uuid.UUID) ([]Bill, error) { return bills, nil }
	repo.findByIDFn = func(_ context.Context, id uuid.UUID) (*Bill, error) {
		if b, ok := byID[id]; ok {
			return b, nil
		}
		return nil, errBillFinalizeNotFound
	}
	audit := &mockBillAuditRepo{}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})
	return svc, repo, audit
}

// errBillFinalizeNotFound returns gorm.ErrRecordNotFound semantics without
// importing gorm into the test file — finalizeBillInTx already maps any
// "find bill" error from FindByID into ErrBillNotFound via gorm.ErrRecordNotFound,
// but this fixture sticks to the simpler "bill exists in batch" happy case.
var errBillFinalizeNotFound = errors.New("bill not found in fixture")

// Happy path: 3 DRAFT monthly bills → 3 FINALIZED + exactly 3 FINALIZE
// audit rows. Locks #10: audit count == success_count, no extra rows.
func TestBatchFinalizeAll_HappyPath_FinalizesAllDraftBillsWithAudit(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	bills := []Bill{finalizeFixture(ids[0]), finalizeFixture(ids[1]), finalizeFixture(ids[2])}
	svc, repo, audit := newBatchFinalizeService(bills)

	actor := uuid.New()
	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), &actor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 3 {
		t.Errorf("SuccessCount = %d, want 3", result.SuccessCount)
	}
	if result.FailCount != 0 {
		t.Errorf("FailCount = %d, want 0", result.FailCount)
	}
	if len(result.Failures) != 0 {
		t.Errorf("Failures len = %d, want 0", len(result.Failures))
	}

	// Lock #10 — exactly one FINALIZE audit row per success, nothing more.
	finalizeCount := 0
	for _, log := range audit.logs {
		if log.Action == AuditFinalize {
			finalizeCount++
		}
		if log.ActorID == nil || *log.ActorID != actor {
			t.Errorf("audit actor_id = %v, want %s (bulk finalize is admin-triggered)", log.ActorID, actor)
		}
	}
	if finalizeCount != result.SuccessCount {
		t.Errorf("FINALIZE audit count = %d, want %d (success_count) — lock #10 violated", finalizeCount, result.SuccessCount)
	}

	// All 3 bills passed to Update.
	if len(repo.updatedBills) != 3 {
		t.Errorf("Update call count = %d, want 3", len(repo.updatedBills))
	}
	for _, b := range repo.updatedBills {
		if b.Status != BillStatusFinalized {
			t.Errorf("bill %s saved status = %s, want FINALIZED", b.ID, b.Status)
		}
	}
}

// Partial failure: one DRAFT has no line items (ErrNoLineItems), the other
// two succeed. Loop must continue past the failure, success_count = 2,
// fail_count = 1 with code NO_LINE_ITEMS.
func TestBatchFinalizeAll_PartialFailureContinues(t *testing.T) {
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	emptyDraft := finalizeFixture(id2)
	emptyDraft.LineItems = nil // triggers ErrNoLineItems on Finalize()

	bills := []Bill{finalizeFixture(id1), emptyDraft, finalizeFixture(id3)}
	svc, _, audit := newBatchFinalizeService(bills)

	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if result.FailCount != 1 {
		t.Fatalf("FailCount = %d, want 1", result.FailCount)
	}
	got := result.Failures[0]
	if got.BillID != id2 {
		t.Errorf("failure bill_id = %s, want %s", got.BillID, id2)
	}
	if got.Code != FailureCodeNoLineItems {
		t.Errorf("failure code = %s, want NO_LINE_ITEMS", got.Code)
	}

	// Lock #10 — only 2 FINALIZE audit rows (not 3, the failed bill emits none).
	finalizeCount := 0
	for _, log := range audit.logs {
		if log.Action == AuditFinalize {
			finalizeCount++
		}
	}
	if finalizeCount != 2 {
		t.Errorf("FINALIZE audit count = %d, want 2 (failed bill must not emit audit)", finalizeCount)
	}
}

// Idempotent rerun: already-FINALIZED bills are silent-skipped. They don't
// count toward success or failure, and they emit no audit (lock #1 + #10).
func TestBatchFinalizeAll_IdempotentRerun_FinalizedSilentlySkipped(t *testing.T) {
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	alreadyFinalized := finalizeFixture(id2)
	alreadyFinalized.Status = BillStatusFinalized

	bills := []Bill{finalizeFixture(id1), alreadyFinalized, finalizeFixture(id3)}
	svc, repo, audit := newBatchFinalizeService(bills)

	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the two DRAFT bills count as success. The FINALIZED one is invisible.
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2 (already-FINALIZED silent-skipped)", result.SuccessCount)
	}
	if result.FailCount != 0 {
		t.Errorf("FailCount = %d, want 0 (already-FINALIZED must not be a failure)", result.FailCount)
	}
	if len(result.Failures) != 0 {
		t.Errorf("Failures len = %d, want 0", len(result.Failures))
	}

	// Update only invoked for the 2 DRAFT bills.
	if len(repo.updatedBills) != 2 {
		t.Errorf("Update call count = %d, want 2", len(repo.updatedBills))
	}

	// Lock #10 — 2 audit rows, none for the silent-skipped bill.
	finalizeCount := 0
	for _, log := range audit.logs {
		if log.Action == AuditFinalize {
			finalizeCount++
		}
		if log.BillID == id2 {
			t.Errorf("audit row leaked for silent-skipped FINALIZED bill %s", id2)
		}
	}
	if finalizeCount != 2 {
		t.Errorf("FINALIZE audit count = %d, want 2", finalizeCount)
	}
}

// VOID/PAID/other non-DRAFT non-FINALIZED states must surface as NOT_DRAFT
// failure (lock spec point #1: silent-skip applies ONLY to FINALIZED).
func TestBatchFinalizeAll_NonDraftBill_FailsWithNotDraft(t *testing.T) {
	id1, idVoided := uuid.New(), uuid.New()
	voided := finalizeFixture(idVoided)
	voided.Status = BillStatusVoid
	reason := "ออกผิด"
	voided.VoidReason = &reason

	bills := []Bill{finalizeFixture(id1), voided}
	svc, _, audit := newBatchFinalizeService(bills)

	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", result.SuccessCount)
	}
	if result.FailCount != 1 {
		t.Fatalf("FailCount = %d, want 1", result.FailCount)
	}
	got := result.Failures[0]
	if got.BillID != idVoided {
		t.Errorf("failure bill_id = %s, want %s", got.BillID, idVoided)
	}
	if got.Code != FailureCodeNotDraft {
		t.Errorf("failure code = %s, want NOT_DRAFT", got.Code)
	}

	// Lock #10 — single FINALIZE audit row, none for the VOID bill.
	finalizeCount := 0
	for _, log := range audit.logs {
		if log.Action == AuditFinalize {
			finalizeCount++
		}
		if log.BillID == idVoided {
			t.Errorf("audit row leaked for non-DRAFT bill %s", idVoided)
		}
	}
	if finalizeCount != 1 {
		t.Errorf("FINALIZE audit count = %d, want 1", finalizeCount)
	}
}

// Per-item isolation: bill A's audit Create fails → bill A surfaces as
// INFRA_ERROR but bills B and C still finalize cleanly. Proves the
// per-item TX boundary actually isolates failures (lock #3).
func TestBatchFinalizeAll_AuditFailure_OnlyAffectsThatBill(t *testing.T) {
	idA, idB, idC := uuid.New(), uuid.New(), uuid.New()
	bills := []Bill{finalizeFixture(idA), finalizeFixture(idB), finalizeFixture(idC)}
	svc, _, audit := newBatchFinalizeService(bills)
	audit.createErrByBillID = map[uuid.UUID]error{
		idA: errors.New("audit-store unreachable for bill A"),
	}

	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2 (B and C succeed when A's audit fails)", result.SuccessCount)
	}
	if result.FailCount != 1 {
		t.Fatalf("FailCount = %d, want 1", result.FailCount)
	}
	got := result.Failures[0]
	if got.BillID != idA {
		t.Errorf("failure bill_id = %s, want %s (only bill A should fail)", got.BillID, idA)
	}
	if got.Code != FailureCodeInfraError {
		t.Errorf("failure code = %s, want INFRA_ERROR (audit failure is infra)", got.Code)
	}

	// Lock #10 — 2 audit rows persisted (B and C); A's failed Create
	// returned error and never appended to logs.
	finalizeCount := 0
	for _, log := range audit.logs {
		if log.Action == AuditFinalize {
			finalizeCount++
		}
		if log.BillID == idA {
			t.Errorf("audit row leaked for bill A despite Create error")
		}
	}
	if finalizeCount != 2 {
		t.Errorf("FINALIZE audit count = %d, want 2 (= success_count)", finalizeCount)
	}
}

// Empty batch returns a zero-result with explicit `failures: []` so the
// FE deserializes as `[]` not `null`.
func TestBatchFinalizeAll_EmptyBatch_ReturnsZeros(t *testing.T) {
	svc, _, _ := newBatchFinalizeService([]Bill{})
	result, err := svc.BatchFinalizeAll(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 0 || result.FailCount != 0 {
		t.Errorf("zero-bill batch counts = (%d, %d), want (0, 0)", result.SuccessCount, result.FailCount)
	}
	if result.Failures == nil {
		t.Error("Failures slice must be non-nil (FE expects [] not null)")
	}
}

// classifyFinalizeError unit test — locks the sentinel→code mapping so a
// future refactor that drops errors.Is fails loud here.
func TestClassifyFinalizeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want BatchFinalizeFailureCode
	}{
		{"ErrNoLineItems", ErrNoLineItems, FailureCodeNoLineItems},
		{"ErrNotDraft", ErrNotDraft, FailureCodeNotDraft},
		{"wrapped ErrNoLineItems", errors.New("wrap"), FailureCodeInfraError}, // not unwrappable → INFRA
		{"unknown infra", errors.New("db connection lost"), FailureCodeInfraError},
		{"nil", nil, FailureCodeInfraError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := classifyFinalizeError(tc.err)
			if code != tc.want {
				t.Errorf("code = %s, want %s", code, tc.want)
			}
			if msg == "" {
				t.Errorf("message must not be empty (FE renders it directly)")
			}
		})
	}
}
