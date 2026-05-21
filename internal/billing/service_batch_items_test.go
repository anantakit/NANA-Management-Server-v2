package billing

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// GetBatchItems must stamp IsEdited per item by issuing exactly ONE batched
// EditedBillIDs query for all committed bills in the response. Uncommitted
// items (BillID == nil) keep IsEdited=false trivially.
func TestGetBatchItems_BatchedIsEditedQuery(t *testing.T) {
	batchID := uuid.New()
	billA, billB, billC := uuid.New(), uuid.New(), uuid.New()
	items := []BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: &billA, ResultType: ResultCreated},
		{ID: uuid.New(), BatchID: batchID, BillID: &billB, ResultType: ResultCreated},
		// Uncommitted item — no bill yet, IsEdited must default false without
		// being included in the EditedBillIDs lookup.
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: ResultSkipped},
		{ID: uuid.New(), BatchID: batchID, BillID: &billC, ResultType: ResultCreated},
	}
	repo := &mockBillingRepo{createdBatchItems: items}
	audit := &mockBillAuditRepo{}
	// A and C have edit-class audit history; B has only lifecycle events.
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: billA, Action: AuditUpdateOverride},
		{ID: uuid.New(), BillID: billB, Action: AuditCreateDraft}, // lifecycle, not edit
		{ID: uuid.New(), BillID: billC, Action: AuditAddManualItem},
	}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	got, err := svc.GetBatchItems(context.Background(), batchID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(got))
	}

	expect := map[uuid.UUID]bool{billA: true, billB: false, billC: true}
	for _, it := range got {
		if it.BillID == nil {
			if it.IsEdited {
				t.Errorf("uncommitted item must default IsEdited=false, got true")
			}
			continue
		}
		if got, want := it.IsEdited, expect[*it.BillID]; got != want {
			t.Errorf("bill %s IsEdited = %v, want %v", *it.BillID, got, want)
		}
	}

	// N+1 prevention — exactly one EditedBillIDs call regardless of item count.
	if audit.editedQueryCalls != 1 {
		t.Errorf("EditedBillIDs call count = %d, want 1 (single batched query per request)", audit.editedQueryCalls)
	}
}

// All items uncommitted → no audit query issued (zero-bill optimization).
// Avoids a round-trip on pre-commit batch detail views.
func TestGetBatchItems_AllUncommitted_NoAuditQuery(t *testing.T) {
	batchID := uuid.New()
	items := []BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: ResultCreated},
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: ResultSkipped},
	}
	repo := &mockBillingRepo{createdBatchItems: items}
	audit := &mockBillAuditRepo{}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	got, err := svc.GetBatchItems(context.Background(), batchID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, it := range got {
		if it.IsEdited {
			t.Errorf("uncommitted item leaked IsEdited=true")
		}
	}
	if audit.editedQueryCalls != 0 {
		t.Errorf("EditedBillIDs called %d times for all-uncommitted batch, want 0", audit.editedQueryCalls)
	}
}

// Audit-store failure on the IsEdited lookup propagates as a wrapped error
// — never silently render is_edited=false. Mirrors the locked semantics
// of List / GetByID is_edited population.
func TestGetBatchItems_AuditFailure_Propagates(t *testing.T) {
	batchID := uuid.New()
	billID := uuid.New()
	items := []BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: &billID, ResultType: ResultCreated},
	}
	repo := &mockBillingRepo{createdBatchItems: items}
	audit := &mockBillAuditRepo{editedErr: errBatchEditedAuditDown}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	_, err := svc.GetBatchItems(context.Background(), batchID)
	if err == nil {
		t.Fatal("expected error when EditedBillIDs fails, got nil")
	}
}

var errBatchEditedAuditDown = &simpleErr{msg: "audit store unreachable"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
