package monthly

import (
	"context"
	"testing"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// GetBatchItems must stamp IsEdited per item by issuing exactly ONE batched
// EditedBillIDs query for all committed bills in the response. Uncommitted
// items (BillID == nil) keep IsEdited=false trivially.
func TestGetBatchItems_BatchedIsEditedQuery(t *testing.T) {
	batchID := uuid.New()
	billA, billB, billC := uuid.New(), uuid.New(), uuid.New()
	items := []billing.BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: &billA, ResultType: billing.ResultCreated},
		{ID: uuid.New(), BatchID: batchID, BillID: &billB, ResultType: billing.ResultCreated},
		// Uncommitted item — no bill yet, IsEdited must default false without
		// being included in the EditedBillIDs lookup.
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: billing.ResultSkipped},
		{ID: uuid.New(), BatchID: batchID, BillID: &billC, ResultType: billing.ResultCreated},
	}
	store := &mockStore{createdBatchItems: items}
	// A and C have edit-class audit history; B has only lifecycle events.
	store.logs = []billing.BillAuditLog{
		{ID: uuid.New(), BillID: billA, Action: billing.AuditUpdateOverride},
		{ID: uuid.New(), BillID: billB, Action: billing.AuditCreateDraft}, // lifecycle, not edit
		{ID: uuid.New(), BillID: billC, Action: billing.AuditAddManualItem},
	}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

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
	if store.editedQueryCalls != 1 {
		t.Errorf("EditedBillIDs call count = %d, want 1 (single batched query per request)", store.editedQueryCalls)
	}
}

// All items uncommitted → no audit query issued (zero-bill optimization).
// Avoids a round-trip on pre-commit batch detail views.
func TestGetBatchItems_AllUncommitted_NoAuditQuery(t *testing.T) {
	batchID := uuid.New()
	items := []billing.BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: billing.ResultCreated},
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: billing.ResultSkipped},
	}
	store := &mockStore{createdBatchItems: items}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	got, err := svc.GetBatchItems(context.Background(), batchID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, it := range got {
		if it.IsEdited {
			t.Errorf("uncommitted item leaked IsEdited=true")
		}
	}
	if store.editedQueryCalls != 0 {
		t.Errorf("EditedBillIDs called %d times for all-uncommitted batch, want 0", store.editedQueryCalls)
	}
}

// Audit-store failure on the IsEdited lookup propagates as a wrapped error
// — never silently render is_edited=false. Mirrors the locked semantics
// of List / GetByID is_edited population.
func TestGetBatchItems_AuditFailure_Propagates(t *testing.T) {
	batchID := uuid.New()
	billID := uuid.New()
	items := []billing.BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: &billID, ResultType: billing.ResultCreated},
	}
	store := &mockStore{createdBatchItems: items, editedErr: errBatchEditedAuditDown}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	_, err := svc.GetBatchItems(context.Background(), batchID)
	if err == nil {
		t.Fatal("expected error when EditedBillIDs fails, got nil")
	}
}

var errBatchEditedAuditDown = &simpleErr{msg: "audit store unreachable"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// GetBatchItems must pass bill_status through from the repo so the FE can
// derive draftCount / editedCount / finalizedCount from items[] alone (no
// extra query). Mirrors the is_edited population pattern but column-derived
// (LEFT JOIN bills) instead of audit-derived. Uncommitted items keep
// BillStatus = nil regardless of map presence (BillID == nil short-circuit).
func TestGetBatchItems_PropagatesBillStatus(t *testing.T) {
	batchID := uuid.New()
	billDraft, billFinalized := uuid.New(), uuid.New()
	items := []billing.BillGenerationBatchItem{
		{ID: uuid.New(), BatchID: batchID, BillID: &billDraft, ResultType: billing.ResultCreated},
		{ID: uuid.New(), BatchID: batchID, BillID: &billFinalized, ResultType: billing.ResultCreated},
		// Uncommitted — no bill_id, no bill_status mapping should reach the response.
		{ID: uuid.New(), BatchID: batchID, BillID: nil, ResultType: billing.ResultSkipped},
	}
	store := &mockStore{
		createdBatchItems: items,
		batchItemBillStatuses: map[uuid.UUID]billing.BillStatus{
			billDraft:     billing.BillStatusDraft,
			billFinalized: billing.BillStatusFinalized,
		},
	}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	got, err := svc.GetBatchItems(context.Background(), batchID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(got))
	}

	expect := map[uuid.UUID]billing.BillStatus{
		billDraft:     billing.BillStatusDraft,
		billFinalized: billing.BillStatusFinalized,
	}
	for _, it := range got {
		if it.BillID == nil {
			if it.BillStatus != nil {
				t.Errorf("uncommitted item leaked BillStatus = %s, want nil", *it.BillStatus)
			}
			continue
		}
		if it.BillStatus == nil {
			t.Errorf("bill %s missing BillStatus on response", *it.BillID)
			continue
		}
		if got, want := *it.BillStatus, expect[*it.BillID]; got != want {
			t.Errorf("bill %s BillStatus = %s, want %s", *it.BillID, got, want)
		}
	}
}

// DTO mapper test — BatchItemResponse.bill_status string mirrors the
// BillStatus pointer one-for-one. Uncommitted item produces nil → JSON
// omitempty drops the field entirely.
func TestToBatchItemResponse_BillStatusMapping(t *testing.T) {
	draft := billing.BillStatusDraft
	committed := billing.BatchItemWithTenant{
		BillGenerationBatchItem: billing.BillGenerationBatchItem{ID: uuid.New(), BillID: &[]uuid.UUID{uuid.New()}[0]},
		BillStatus:              &draft,
	}
	uncommitted := billing.BatchItemWithTenant{
		BillGenerationBatchItem: billing.BillGenerationBatchItem{ID: uuid.New(), BillID: nil},
		BillStatus:              nil,
	}

	r1 := ToBatchItemResponse(committed)
	if r1.BillStatus == nil || *r1.BillStatus != string(billing.BillStatusDraft) {
		t.Errorf("committed.BillStatus = %v, want pointer to DRAFT", r1.BillStatus)
	}

	r2 := ToBatchItemResponse(uncommitted)
	if r2.BillStatus != nil {
		t.Errorf("uncommitted.BillStatus = %v, want nil", r2.BillStatus)
	}
}
