package monthly

import (
	"context"
	"time"

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
// because the repository that owns their SQL stays there (Option B-extended,
// "no repository split in this commit"). Monthly owns the workflow logic
// over those types — generation, classification, commit, finalize, replan
// — without owning the data structures themselves. A future PR may move
// types + repo together when the broader monthly migration completes.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE WIDENING THESE PORTS
// ===========================================================================
//
// This port surface is INTENTIONALLY WIDE in this stage (5 ports total: 3
// onto billing root + 2 cross-feature). The width is a consequence of
// the deferred repository split — monthly currently reaches every batch-
// table operation through ports that billing.MonthlyAdapter satisfies as
// pure delegation. The width is load-bearing here, not a smell.
//
// Port naming uses MONTHLY-LOCAL INTENT (Store / Source) rather than the
// generic Reader / Commander Q/C split. After widening, a single read-only
// port over the bills table is no longer "narrow" enough that Reader vs
// Commander carries meaningful design intent — what matters to a monthly
// reader is "this is how monthly reaches bills" (BillStore). Locked
// 2026-06-19. The Q/C split doctrine in cross-feature-patterns.md remains
// the default for cross-feature ports; monthly is an intentional deviation
// because it is a sub-workflow of billing, not a separate bounded context.
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
//  3. When the repository split eventually lands, this port surface
//     shrinks — batch methods migrate to a monthly-owned repository, and
//     the corresponding BillStore / BatchStore methods disappear from
//     here. Until then, growth is permitted but every addition must
//     satisfy rule 1.
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

// --- Batch tables port ---
//
// SQL for these methods lives on billing.BillingRepository (no repo split
// in commit 2b). The port surface uses billing.* types verbatim because
// the entity definitions also live in billing root for the same reason.

// BatchStore is the monthly workflow's combined read+write surface over
// the bill_generation_batches + bill_generation_batch_items tables.
// LockBatchForCommit is a locking read (SELECT FOR UPDATE) — kept here
// because it has a TX requirement and is used together with the writes.
type BatchStore interface {
	// --- queries ---

	FindBatchByID(ctx context.Context, id uuid.UUID) (*billing.BillGenerationBatch, error)
	FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]billing.BatchItemWithTenant, error)
	FindBatchItemByID(ctx context.Context, itemID uuid.UUID) (*billing.BillGenerationBatchItem, error)
	FindBatchItemByIDWithTenant(ctx context.Context, itemID uuid.UUID) (*billing.BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error)

	// ListCommitPendingItems returns batch items that have not yet produced
	// a bill. Used by the commit loop; idempotent rerun semantics depend
	// on this query.
	ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]billing.BillGenerationBatchItem, error)

	// --- commands ---

	CreateBatch(ctx context.Context, batch *billing.BillGenerationBatch, items []billing.BillGenerationBatchItem) error
	LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*billing.BillGenerationBatch, error)
	UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error
	UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status billing.CommitStatus, committedAt *time.Time) error
	UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error
	UpdateBatchItemPlan(
		ctx context.Context,
		itemID uuid.UUID,
		resultType billing.ResultType,
		reasonCode, reasonText string,
		billID *uuid.UUID,
		snapshot billing.ComputedSnapshot,
	) error
}

// --- Cross-feature read ports ---

// MeterReadingSource is monthly's consumer-defined port onto meterreading.
// Mirrors billing.MeterReadingQuerier's FindMonthlyByRoomsAndMonth — kept
// separate so monthly doesn't transitively depend on billing's full port
// surface. "Source" suffix per monthly's local intent naming convention.
type MeterReadingSource interface {
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
}

// MoveOutSource is monthly's consumer-defined port onto moveout. "Source"
// suffix per monthly's local intent naming convention.
type MoveOutSource interface {
	FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
}
