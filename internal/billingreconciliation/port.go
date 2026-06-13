package billingreconciliation

import (
	"context"

	"nana/internal/meterreading"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// MeterReadingQuerier is the minimal read port billingreconciliation needs
// from meterreading — scoped to the one method used by Reconcile + guardWritable.
// Narrower than meterreading.MeterReadingRepository (breaks the import cycle:
// meterreading imports nothing from here; billing imports billingreconciliation).
type MeterReadingQuerier interface {
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]*meterreading.MeterReading, error)
}

// MoveOutQuerier is the minimal read port billingreconciliation needs from
// moveout — scoped to the one method used by Reconcile + guardWritable.
// Returns map[uuid.UUID]bool (room_id → hasPending) so moveout is not
// imported here (no moveout domain type crosses the port).
type MoveOutQuerier interface {
	FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
}

// Port-level sentinel errors returned by BillsCommander.
// Classification responsibility lives on the billing adapter side — it
// translates billing's internal sentinels (ErrBillAlreadyExists,
// ErrMeterNotFound, ErrContractNotActive, ...) into one of these two
// outcomes. Keeping the mapping at the adapter avoids forcing
// billingreconciliation to import billing for sentinel comparison
// (which would re-introduce the cycle this package's port direction
// was set up to prevent).
//
// The reconciliation service consumes these via errors.Is and maps each
// to the per-row GenerateSkipReason on the workspace's Generate result.
var (
	// ErrAlreadyBilled — a non-VOID monthly bill exists for the
	// (contract, billing_month) the commander tried to commit. Race with
	// another caller between Reconcile preview and Generate commit.
	// Maps to GenerateSkipAlreadyBilled.
	ErrAlreadyBilled = respond.New(
		"ALREADY_BILLED_BY_OTHER", 409,
		"ห้องนี้มีบิลเดือนนี้แล้ว",
	)

	// ErrLostReady — the room is no longer in a billable state at commit
	// time (meter went missing, contract status changed, etc.).
	// Catch-all "state changed between preview and commit" outcome.
	// Maps to GenerateSkipLostReady.
	ErrLostReady = respond.New(
		"LOST_READY_BETWEEN_PREVIEW_AND_COMMIT", 409,
		"ห้องไม่อยู่ในสถานะพร้อมออกบิลแล้ว",
	)
)

// BillsQuerier reads bill evidence for reconciliation rows. Pure read; no
// transaction context. Mirrors the existing display-read pattern — returns
// a flat projection (BillSnapshot) instead of *billing.Bill so this package
// no longer imports billing directly (cycle prevention: billing implements
// the writer port below, so it imports billingreconciliation).
//
// Implemented by billing.BillingService (methods in service_reconciliation_ports.go),
// wired in cmd/main.go.
type BillsQuerier interface {
	FindExistingBillsByContractsAndMonth(
		ctx context.Context,
		contractIDs []uuid.UUID,
		billingMonth string,
	) (map[uuid.UUID]*BillSnapshot, error)
}

// CreateMonthlyBillForReconciliationRequest is the per-row commit input
// from the reconciliation workspace's ออกบิล flow. Shape is intentionally
// minimal — contract_id + billing_month are the only operator-supplied
// identifiers; meter lookup is the billing adapter's responsibility.
//
// Name is long-but-honest by design (see feedback_no_rewalk_during_implementation.md).
// Shorter framings ("CreateFromBatch", "CommitRoom", "GenerateBill",
// "RunReconciliation") were rejected — each smuggles in a doctrine-rejected
// object (Batch, Commit, Run) at the API surface.
type CreateMonthlyBillForReconciliationRequest struct {
	ContractID   uuid.UUID
	BillingMonth string
}

// CreatedBill is the slim return shape for a single-bill commit. Only the
// new bill's ID crosses the port — the reconciliation service does not
// re-render bill details; the workspace re-fetches current truth on the
// next Reconcile call (workspace = current truth doctrine).
type CreatedBill struct {
	ID uuid.UUID
}

// BillsCommander creates a monthly bill for one (contract, billing_month)
// pair on behalf of the reconciliation workspace's per-row ออกบิล commit.
// Single-row semantics: one call = one bill, one TX.
//
// Transaction ownership: the implementation owns its TX (delegates to
// billing.Service.CreateMonthlyBill, which runs its own RunInTx).
// Callers MUST NOT wrap this in a parent transaction — explicit contrast
// with moveout.BillingCommander methods, which require caller-provided
// txCtx. Per-call TX is intentional: each row commits or skips
// independently, matching the per-item result semantics in
// project_reconciliation_phase1d_scenario1_locks.md (Q1 Contract A).
// Batch fan-out lives at the reconciliation service layer (BE #2).
//
// Anti-promotion: there is no batch/session/run object across this port —
// fan-out is a service-level loop that calls this method N times.
//
// Error contract: the adapter classifies billing-side sentinels (and the
// PG unique-violation on idx_bills_unique_monthly) into the two port-level
// SKIPPED outcomes — `ErrAlreadyBilled` and `ErrLostReady` defined above.
// System errors and unrecognized AppErrors propagate unchanged for the
// reconciliation service to surface as FAILED rows. Classification lives
// on the adapter so the consumer doesn't need to import billing.
//
// Implemented by billing.BillingService (methods in service_reconciliation_ports.go),
// wired in cmd/main.go.
type BillsCommander interface {
	CreateMonthlyBillForReconciliation(
		ctx context.Context,
		req CreateMonthlyBillForReconciliationRequest,
		actor *uuid.UUID,
	) (*CreatedBill, error)
}
