package payment

import (
	"context"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// BillingPort exposes low-level billing operations for use within the
// payment transaction. All methods must be called with txCtx so they
// participate in the same DB transaction as the payment record insert.
//
// Implemented by billing.PaymentAdapter — not billing.billingService —
// so no nested transaction is started.
type BillingPort interface {
	// FindBillForPayment loads a bill outside the TX (pre-TX fast reject).
	FindBillForPayment(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	// LockBillForPayment SELECT FOR UPDATEs inside the TX.
	LockBillForPayment(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	// PersistBillPaid saves the PAID status transition inside the TX.
	PersistBillPaid(ctx context.Context, b *billing.Bill) error
	// EmitPaymentAudit appends RECORD_PAYMENT to bill_audit_log inside the TX.
	EmitPaymentAudit(ctx context.Context, billID uuid.UUID, actorID *uuid.UUID, amount int64, method string) error
}
