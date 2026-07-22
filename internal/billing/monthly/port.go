package monthly

import (
	"context"

	"nana/internal/billing"
	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// All ports here are CONSUMER-DEFINED (monthly is the consumer; billing
// root + meterreading + moveout are the providers). Provider adapters live
// in the respective provider packages and are wired together at cmd/main.go.
//
// Returns reference billing.* types directly per Option A (locked
// 2026-06-19): monthly is an internal sub-workflow of the billing domain,
// not a separate bounded context, so translation DTOs would add boilerplate
// without reducing real risk at this scale.
//
// Batch entities (BillGenerationBatch, BillGenerationBatchItem, BatchStatus,
// CommitStatus, ResultType, Reason* constants, BatchItemWithTenant,
// CommitBatchResult, BatchListParams) remain in the billing root package
// even after the batch-repo split — they're shared shapes that handler
// DTOs + the service interface reference, and moving them would multiply
// rename churn without changing the persistence-ownership win. Monthly
// owns batch persistence via monthly.BatchRepository (repository.go);
// these types are reference-shared, not workflow-private.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE WIDENING THESE PORTS
// ===========================================================================
//
// Port surface scope: 2 ports onto billing root (BillStore + AuditStore) +
// 2 cross-feature read ports (MeterReadingSource + MoveOutSource). The
// previous BatchStore port was retired when batch persistence moved into
// monthly.BatchRepository (see repository.go) — monthly now talks to its
// own batch tables directly without going through billing.MonthlyAdapter
// for those calls.
//
// Port naming uses MONTHLY-LOCAL INTENT (Store / Source) rather than the
// generic Reader / Commander Q/C split. A single read-only port over the
// bills table is no longer "narrow" enough that Reader vs Commander
// carries meaningful design intent — what matters to a monthly reader is
// "this is how monthly reaches bills" (BillStore). Locked 2026-06-19. The
// Q/C split doctrine in cross-feature-patterns.md remains the default for
// cross-feature ports; monthly is an intentional deviation because it is
// a sub-workflow of billing, not a separate bounded context.
//
// Stewardship rules:
//
//  1. Adding a new port method is OK ONLY if it's a thin read/write that
//     billing.MonthlyAdapter can satisfy with a single line of repo/audit
//     passthrough. If you find yourself wanting business logic inside the
//     adapter to satisfy a new port method, the primitive belongs in the
//     billing root package (extract it there first, then add the port).
//
//  2. Do NOT re-split these ports into Reader / Commander pairs to
//     "comply" with the cross-feature doctrine. The deviation is
//     deliberate — see above. Re-splitting would multiply the port count
//     without adding design value.
//
//  3. New batch-table persistence belongs on monthly.BatchRepository, not
//     on these ports. Cross the BillStore / AuditStore boundary only for
//     reads/writes against the shared bills / bill_audit_log tables.
// ===========================================================================

// --- Bill table port ---

// BillStore is the monthly workflow's combined read+write surface over the
// shared `bills` table. Wraps both query methods (FindByID, listings) and
// write methods (CreateBill, UpdateBill, FinalizeBillInTx). Callers MUST
// pass a txCtx for mutating methods when atomicity with audit emission
// matters (standard pattern across commit + finalize).
type BillStore interface {
	// --- queries ---

	FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType billing.BillType) (*billing.Bill, error)

	// FindRecoverySourceRefundRates resolves the Q1.6 forward-credit S0 gate +
	// rate for a recovery reading (ontology lock 2026-07-08): the source month's
	// FINALIZED/PAID bill line unit_price, or (nil, nil) when the source was
	// never billed. Consumed by billing.ResolveRecoveryReconciliation so the
	// batch/replan snapshot emits F only for a real, source-billed over-record.
	FindRecoverySourceRefundRates(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*billing.SourceRefundRates, error)

	// ListByBatchID returns every bill linked to the given batch_id.
	// Used by BatchFinalizeAll. Settlement bills are excluded at SQL.
	ListByBatchID(ctx context.Context, batchID uuid.UUID) ([]billing.Bill, error)

	// ListMonthlyByApartmentMonth returns every non-VOID MONTHLY bill scoped
	// to (apartment, billing_month). Powers FinalizeAllByMonth — the per-month
	// sibling of the batch-scoped finalize for bills created via the
	// reconciliation Generate path (which has no Batch wrapper).
	ListMonthlyByApartmentMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string) ([]billing.Bill, error)

	// FindActiveContractsByApartmentID is a JOIN read returning the active
	// contracts for an apartment with the room fields the batch planner
	// needs. Lives on the bill repo (display-read pattern, level 1).
	FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billing.ContractWithRoom, error)

	// FindExistingByContractsAndMonth returns a map of contractID → existing
	// non-VOID bill for the given month. Used by the batch classifier to
	// flag ALREADY_EXISTS rows.
	FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]*billing.Bill, error)

	// --- commands ---

	// CreateBill inserts one bill. Caller is responsible for pre-populating
	// the ID when the audit row needs to reference it inside the same TX.
	CreateBill(ctx context.Context, b *billing.Bill) error
	UpdateBill(ctx context.Context, b *billing.Bill) error

	// FinalizeBillInTx is the shared DRAFT → FINALIZED transition: load,
	// guard via the Bill.Finalize() domain method, persist, emit
	// AuditFinalize — all on the provided txCtx. Returns billing's raw
	// domain sentinel errors (ErrBillNotFound / ErrNotDraft / ErrNoLineItems)
	// so callers can classify via errors.Is. Infra errors propagate wrapped.
	FinalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error
}

// --- Audit table port ---

// AuditStore is the monthly workflow's combined read+write surface over
// the shared `bill_audit_log` table.
type AuditStore interface {
	// EditedBillIDs returns the subset of input bill IDs that have at least
	// one edit-class audit event. Used by GetBatchItems to surface the
	// "แก้ไขแล้ว" badge per batch row without per-row queries.
	EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error)

	// RecordAudit marshals the typed payload and writes one row keyed to
	// the bill. Must be called with a txCtx if the audit row must roll
	// back alongside the bill mutation (standard contract).
	RecordAudit(ctx context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error
}

// --- Cross-feature read ports ---

// MeterReadingSource is monthly's consumer-defined port onto meterreading.
// Mirrors billing.MeterReadingQuerier's FindMonthlyByRoomsAndMonth — kept
// separate so monthly doesn't transitively depend on billing's full port
// surface. "Source" suffix per monthly's local intent naming convention.
type MeterReadingSource interface {
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
	// FindConsumptionMonthlyByRoomsAndMonth returns the real (non-anchor) MONTHLY
	// rows so the batch/replan snapshot can bill the unaffected utility's real
	// usage when a recovery anchor governs the month (utility-scoped overlay).
	FindConsumptionMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
	// FindReplacementAnchorsByRoomsAndMonth bulk-fetches PHYSICAL_REPLACEMENT
	// events per room (oldest-first) so batch generation aggregates their tails
	// into canonical period usage. Replace Meter.
	FindReplacementAnchorsByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID][]*meterreading.MeterReading, error)
}

// MoveOutSource is monthly's consumer-defined port onto moveout. "Source"
// suffix per monthly's local intent naming convention.
type MoveOutSource interface {
	FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
}
