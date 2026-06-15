package billing

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// PaymentAdapter satisfies payment.BillingPort with a deliberately narrow
// dependency surface: only BillingRepository + BillAuditRepository.
//
// Contrast with service_moveout_ports.go and service_reconciliation_ports.go,
// where billingService implements the consumer port directly (methods on the
// struct). That pattern makes sense when the consumer needs the full billing
// orchestration layer. Payment needs none of that — it owns the transaction,
// calls repo + audit directly, and must not drag in the planner, config
// resolver, or reconciliation wiring.
//
// All methods must be called with a txCtx from the payment service's RunInTx
// so their DB writes join the same transaction.
type PaymentAdapter struct {
	repo  BillingRepository
	audit BillAuditRepository
}

func NewPaymentAdapter(repo BillingRepository, audit BillAuditRepository) *PaymentAdapter {
	return &PaymentAdapter{repo: repo, audit: audit}
}

func (a *PaymentAdapter) FindBillForPayment(ctx context.Context, id uuid.UUID) (*Bill, error) {
	return a.repo.FindByID(ctx, id)
}

func (a *PaymentAdapter) LockBillForPayment(ctx context.Context, id uuid.UUID) (*Bill, error) {
	return a.repo.LockBillForPayment(ctx, id)
}

func (a *PaymentAdapter) PersistBillPaid(ctx context.Context, b *Bill) error {
	return a.repo.Update(ctx, b)
}

func (a *PaymentAdapter) EmitPaymentAudit(ctx context.Context, billID uuid.UUID, actorID *uuid.UUID, amount int64, method string) error {
	p, err := MarshalAuditPayload(AuditRecordPaymentPayload{Amount: amount, Method: method})
	if err != nil {
		return fmt.Errorf("marshal payment audit: %w", err)
	}
	return a.audit.Create(ctx, &BillAuditLog{
		BillID:  billID,
		Action:  AuditRecordPayment,
		ActorID: actorID,
		Payload: p,
	})
}
