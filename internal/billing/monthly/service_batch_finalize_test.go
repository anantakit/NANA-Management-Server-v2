package monthly

import (
	"context"
	"errors"
	"testing"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// finalizeFixture builds a DRAFT monthly bill with one AUTO line item.
// status / line items are overridable per-test for the various paths
// (FINALIZED, VOID, empty line items, etc).
func finalizeFixture(billID uuid.UUID) billing.Bill {
	return billing.Bill{
		ID:           billID,
		ContractID:   uuid.New(),
		BillingMonth: "2026-05",
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
		TotalAmount:  500000,
		LineItems: []billing.BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Amount: 500000, SortOrder: 1},
		},
	}
}

// newBatchFinalizeService wires a service whose store returns the given bills
// from ListByBatchID and whose FindByID matches by id. Returns the store so
// tests can assert exact emission counts (lock #10).
func newBatchFinalizeService(bills []billing.Bill) (Service, *mockStore) {
	byID := make(map[uuid.UUID]*billing.Bill, len(bills))
	for i := range bills {
		byID[bills[i].ID] = &bills[i]
	}
	store := &mockStore{}
	store.listByBatchIDFn = func(_ uuid.UUID) ([]billing.Bill, error) { return bills, nil }
	store.findByIDFn = func(_ context.Context, id uuid.UUID) (*billing.Bill, error) {
		if b, ok := byID[id]; ok {
			return b, nil
		}
		return nil, errBillFinalizeNotFound
	}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})
	return svc, store
}

// errBillFinalizeNotFound returns a sentinel from FindByID for bills not in
// the fixture. The migrated tests only exercise the "bill is in the slice"
// happy case so this never fires; kept for parity with the pre-migration
// shape.
var errBillFinalizeNotFound = errors.New("bill not found in fixture")

// Happy path: 3 DRAFT monthly bills → 3 FINALIZED + exactly 3 FINALIZE
// audit rows. Locks #10: audit count == success_count, no extra rows.
func TestBatchFinalizeAll_HappyPath_FinalizesAllDraftBillsWithAudit(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	bills := []billing.Bill{finalizeFixture(ids[0]), finalizeFixture(ids[1]), finalizeFixture(ids[2])}
	svc, store := newBatchFinalizeService(bills)

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
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
			finalizeCount++
		}
		if log.ActorID == nil || *log.ActorID != actor {
			t.Errorf("audit actor_id = %v, want %s (bulk finalize is admin-triggered)", log.ActorID, actor)
		}
	}
	if finalizeCount != result.SuccessCount {
		t.Errorf("FINALIZE audit count = %d, want %d (success_count) — lock #10 violated", finalizeCount, result.SuccessCount)
	}

	// All 3 bills passed to Update (tracked by FinalizeBillInTx via updatedBills).
	if len(store.updatedBills) != 3 {
		t.Errorf("Update call count = %d, want 3", len(store.updatedBills))
	}
	for _, b := range store.updatedBills {
		if b.Status != billing.BillStatusFinalized {
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

	bills := []billing.Bill{finalizeFixture(id1), emptyDraft, finalizeFixture(id3)}
	svc, store := newBatchFinalizeService(bills)

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
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
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
	alreadyFinalized.Status = billing.BillStatusFinalized

	bills := []billing.Bill{finalizeFixture(id1), alreadyFinalized, finalizeFixture(id3)}
	svc, store := newBatchFinalizeService(bills)

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
	if len(store.updatedBills) != 2 {
		t.Errorf("Update call count = %d, want 2", len(store.updatedBills))
	}

	// Lock #10 — 2 audit rows, none for the silent-skipped bill.
	finalizeCount := 0
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
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
	voided.Status = billing.BillStatusVoid
	reason := "ออกผิด"
	voided.VoidReason = &reason

	bills := []billing.Bill{finalizeFixture(id1), voided}
	svc, store := newBatchFinalizeService(bills)

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
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
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
	bills := []billing.Bill{finalizeFixture(idA), finalizeFixture(idB), finalizeFixture(idC)}
	svc, store := newBatchFinalizeService(bills)
	store.createErrByBillID = map[uuid.UUID]error{
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
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
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
	svc, _ := newBatchFinalizeService([]billing.Bill{})
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

// --- FinalizeAllByMonth ---
//
// Sibling of BatchFinalizeAll for reconciliation-path bills (no Batch wrapper).
// Core loop semantics are identical; only the scope query differs.

// newFinalizeByMonthService wires a service whose store returns the given bills
// from ListMonthlyByApartmentMonth. Uses the same FindByID + audit mocks as
// newBatchFinalizeService.
func newFinalizeByMonthService(bills []billing.Bill) (Service, *mockStore) {
	byID := make(map[uuid.UUID]*billing.Bill, len(bills))
	for i := range bills {
		byID[bills[i].ID] = &bills[i]
	}
	store := &mockStore{}
	store.listMonthlyByApartmentMonthFn = func(_ uuid.UUID, _ string) ([]billing.Bill, error) {
		return bills, nil
	}
	store.findByIDFn = func(_ context.Context, id uuid.UUID) (*billing.Bill, error) {
		if b, ok := byID[id]; ok {
			return b, nil
		}
		return nil, errBillFinalizeNotFound
	}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})
	return svc, store
}

func TestFinalizeAllByMonth_HappyPath_FinalizesAllDraftBillsWithAudit(t *testing.T) {
	id1, id2 := uuid.New(), uuid.New()
	bills := []billing.Bill{finalizeFixture(id1), finalizeFixture(id2)}
	svc, store := newFinalizeByMonthService(bills)

	actor := uuid.New()
	result, err := svc.FinalizeAllByMonth(context.Background(), uuid.New(), "2026-06", &actor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if result.FailCount != 0 {
		t.Errorf("FailCount = %d, want 0", result.FailCount)
	}
	if len(store.updatedBills) != 2 {
		t.Errorf("Update call count = %d, want 2", len(store.updatedBills))
	}
	finalizeAuditCount := 0
	for _, log := range store.logs {
		if log.Action == billing.AuditFinalize {
			finalizeAuditCount++
		}
	}
	if finalizeAuditCount != 2 {
		t.Errorf("FINALIZE audit count = %d, want 2 (lock #10)", finalizeAuditCount)
	}
}

func TestFinalizeAllByMonth_TotalCount_IncludesSilentlySkippedBills(t *testing.T) {
	draftID := uuid.New()
	finalizedFixture := finalizeFixture(uuid.New())
	finalizedFixture.Status = billing.BillStatusFinalized
	paidFixture := finalizeFixture(uuid.New())
	paidFixture.Status = billing.BillStatusPaid

	bills := []billing.Bill{finalizeFixture(draftID), finalizedFixture, paidFixture}
	svc, _ := newFinalizeByMonthService(bills)

	result, err := svc.FinalizeAllByMonth(context.Background(), uuid.New(), "2026-06", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3 (includes finalized+paid skips)", result.TotalCount)
	}
	if result.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1 (only the draft)", result.SuccessCount)
	}
}

func TestFinalizeAllByMonth_InvalidMonth_Returns400(t *testing.T) {
	svc, _ := newFinalizeByMonthService(nil)
	_, err := svc.FinalizeAllByMonth(context.Background(), uuid.New(), "bad-month", nil)
	if err == nil {
		t.Fatal("expected error for invalid billing_month, got nil")
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
		{"ErrNoLineItems", billing.ErrNoLineItems, FailureCodeNoLineItems},
		{"ErrNotDraft", billing.ErrNotDraft, FailureCodeNotDraft},
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
