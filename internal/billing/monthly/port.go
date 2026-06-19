package monthly

import (
	"context"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// BillReader provides the read primitives the monthly workflow needs over the
// shared `bills` table. Pure read — no transaction required, safe to cache.
//
// Returns *billing.Bill directly (Option A, locked 2026-06-19): monthly is an
// internal sub-workflow of the billing domain, not a fully separate bounded
// context, so importing billing's domain types is the pragmatic choice. The
// strict-neutrality alternative (Option B, mirror reconciliation_adapter.go)
// would force translation DTOs without reducing real risk at this scale.
type BillReader interface {
	// FindByID returns the bill or a wrapped error. NotFound surfaces as the
	// underlying database.IsNotFound-compatible sentinel from billing.
	FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error)

	// FindByContractAndMonth returns the bill matching (contract, month, type)
	// or NotFound. Used by the commit path to enforce the duplicate-monthly
	// invariant before INSERT.
	FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType billing.BillType) (*billing.Bill, error)
}

// BillCommander provides the write primitives the monthly workflow needs over
// the shared `bills` table. Callers MUST pass a txCtx when atomicity with
// audit emission matters (the standard pattern across batch commit + finalize
// paths).
type BillCommander interface {
	// CreateBill inserts one bill. Caller is responsible for pre-populating
	// the ID when the audit row needs to reference it inside the same TX.
	CreateBill(ctx context.Context, b *billing.Bill) error

	// UpdateBill applies a full-field update (Select("*") semantics).
	UpdateBill(ctx context.Context, b *billing.Bill) error

	// FinalizeBillInTx is the shared DRAFT → FINALIZED transition: load,
	// guard via the Bill.Finalize() domain method, persist, emit AuditFinalize
	// — all on the provided txCtx. Returns the raw billing domain sentinel
	// errors (ErrBillNotFound / ErrNotDraft / ErrNoLineItems) so callers can
	// classify via errors.Is. Infra errors propagate wrapped.
	FinalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error
}

// AuditEmitter is the narrow audit surface the monthly workflow needs.
// Marshals the typed payload and writes one row to bill_audit_log keyed
// to the bill. Must be called with a txCtx if the audit row must roll
// back alongside the bill mutation (the standard contract).
//
// Payload is `any` to match billing's MarshalAuditPayload signature —
// callers are expected to pass the typed payload struct that pairs with
// the action (e.g. AuditCreateDraft + AuditCreateDraftPayload). The
// marshaler does not enforce pairing; callers must use the right struct.
type AuditEmitter interface {
	RecordAudit(ctx context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error
}
