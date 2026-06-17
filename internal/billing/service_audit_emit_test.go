package billing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// FinalizeBill must emit one FINALIZE audit row inside the same TX as the
// status mutation. Payload carries previous_status + total_amount snapshot.
func TestFinalizeBill_EmitsFinalizeAudit(t *testing.T) {
	billID := uuid.New()
	draft := &Bill{
		ID:          billID,
		ContractID:  uuid.New(),
		BillType:    BillTypeMonthly,
		Status:      BillStatusDraft,
		TotalAmount: 123400,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 123400, SortOrder: 1},
		},
	}
	repo := &mockBillingRepo{}
	repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) { return draft, nil }
	audit := &mockBillAuditRepo{}
	actor := uuid.New()
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, nil, &mockTxManager{})

	if _, err := svc.FinalizeBill(context.Background(), billID, &actor); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(audit.logs) != 1 {
		t.Fatalf("audit.logs = %d, want 1", len(audit.logs))
	}
	got := audit.logs[0]
	if got.Action != AuditFinalize {
		t.Errorf("action = %s, want FINALIZE", got.Action)
	}
	if got.ActorID == nil || *got.ActorID != actor {
		t.Errorf("actor_id = %v, want %s", got.ActorID, actor)
	}
	var p AuditFinalizePayload
	if err := json.Unmarshal([]byte(got.Payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.PreviousStatus != string(BillStatusDraft) {
		t.Errorf("previous_status = %s, want DRAFT", p.PreviousStatus)
	}
	if p.TotalAmount != 123400 {
		t.Errorf("total_amount = %d, want 123400", p.TotalAmount)
	}
}

// VoidBill must emit one VOID audit row with previous_status + reason payload.
// Reason flows through from the request body so the audit row matches the
// bill's void_reason column verbatim.
func TestVoidBill_EmitsVoidAudit(t *testing.T) {
	billID := uuid.New()
	finalized := &Bill{
		ID:          billID,
		ContractID:  uuid.New(),
		BillType:    BillTypeMonthly,
		Status:      BillStatusFinalized,
		TotalAmount: 50000,
	}
	repo := &mockBillingRepo{}
	repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) { return finalized, nil }
	audit := &mockBillAuditRepo{}
	actor := uuid.New()
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, nil, &mockTxManager{})

	reason := "ออกบิลผิดเดือน"
	if _, err := svc.VoidBill(context.Background(), billID, VoidBillRequest{Reason: reason}, &actor); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(audit.logs) != 1 {
		t.Fatalf("audit.logs = %d, want 1", len(audit.logs))
	}
	got := audit.logs[0]
	if got.Action != AuditVoid {
		t.Errorf("action = %s, want VOID", got.Action)
	}
	if got.ActorID == nil || *got.ActorID != actor {
		t.Errorf("actor_id = %v, want %s", got.ActorID, actor)
	}
	var p AuditVoidPayload
	if err := json.Unmarshal([]byte(got.Payload), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.PreviousStatus != string(BillStatusFinalized) {
		t.Errorf("previous_status = %s, want FINALIZED", p.PreviousStatus)
	}
	if p.Reason != reason {
		t.Errorf("reason = %q, want %q", p.Reason, reason)
	}
}

// commitOneItem (batch path) must emit CREATE_DRAFT per bill with actor=nil
// (system-triggered) and payload that includes batch_id / room_id /
// billing_month so the audit timeline links back to the batch run without
// joining batch_items.
func TestCommitBatch_EmitsCreateDraftAuditWithBatchContext(t *testing.T) {
	batch, items := newTestBatch(2)
	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	audit := &mockBillAuditRepo{}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, nil, &mockTxManager{})

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if len(audit.logs) != 2 {
		t.Fatalf("audit.logs = %d, want 2 (one CREATE_DRAFT per bill)", len(audit.logs))
	}
	for i, l := range audit.logs {
		if l.Action != AuditCreateDraft {
			t.Errorf("log[%d].action = %s, want CREATE_DRAFT", i, l.Action)
		}
		if l.ActorID != nil {
			t.Errorf("log[%d].actor_id = %v, want nil (batch is system-triggered)", i, l.ActorID)
		}
		var p AuditCreateDraftPayload
		if err := json.Unmarshal([]byte(l.Payload), &p); err != nil {
			t.Fatalf("log[%d] unmarshal payload: %v", i, err)
		}
		if p.BatchID == nil || *p.BatchID != batch.ID {
			t.Errorf("log[%d].batch_id = %v, want %s", i, p.BatchID, batch.ID)
		}
		if p.RoomID == nil {
			t.Errorf("log[%d].room_id should be set from batch item", i)
		}
		if p.BillingMonth != batch.BillingMonth {
			t.Errorf("log[%d].billing_month = %q, want %q", i, p.BillingMonth, batch.BillingMonth)
		}
		if p.LineItemCount == 0 {
			t.Errorf("log[%d].line_item_count = 0, snapshot should populate", i)
		}
	}
}
