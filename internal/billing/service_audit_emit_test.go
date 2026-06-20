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
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, nil, &mockTxManager{})

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
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, nil, &mockTxManager{})

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

// TestCommitBatch_EmitsCreateDraftAuditWithBatchContext was moved to
// internal/billing/monthly/service_audit_emit_test.go when the batch commit
// pipeline relocated to the monthly package (commit 2b). The remaining
// audit-emit tests above stay here because they exercise billingService's
// own FinalizeBill / VoidBill methods, which are not part of the monthly
// workflow.
