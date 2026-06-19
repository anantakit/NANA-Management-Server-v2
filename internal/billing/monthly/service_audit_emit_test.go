package monthly

import (
	"context"
	"encoding/json"
	"testing"

	"nana/internal/billing"
)

// commitOneItem (batch path) must emit CREATE_DRAFT per bill with actor=nil
// (system-triggered) and payload that includes batch_id / room_id /
// billing_month so the audit timeline links back to the batch run without
// joining batch_items.
//
// Extracted from internal/billing/service_audit_emit_test.go when the batch
// commit pipeline moved into the monthly package (commit 2b). The remaining
// audit-emit tests (FINALIZE / VOID) stay in billing because they exercise
// billingService.FinalizeBill / VoidBill, which are not part of the monthly
// workflow.
func TestCommitBatch_EmitsCreateDraftAuditWithBatchContext(t *testing.T) {
	batch, items := newTestBatch(2)
	store := &mockStore{createdBatch: batch, createdBatchItems: items}
	svc := NewService(store, store, store, &mockMeterQuerier{}, &mockMoveOutQuerier{}, &mockTxManager{})

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if len(store.logs) != 2 {
		t.Fatalf("audit.logs = %d, want 2 (one CREATE_DRAFT per bill)", len(store.logs))
	}
	for i, l := range store.logs {
		if l.Action != billing.AuditCreateDraft {
			t.Errorf("log[%d].action = %s, want CREATE_DRAFT", i, l.Action)
		}
		if l.ActorID != nil {
			t.Errorf("log[%d].actor_id = %v, want nil (batch is system-triggered)", i, l.ActorID)
		}
		var p billing.AuditCreateDraftPayload
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
