package billingreconciliation

import (
	"context"

	"github.com/google/uuid"
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
// Errors propagate verbatim from the billing side. Sentinel mapping
// (LOST_READY_BETWEEN_PREVIEW_AND_COMMIT, ALREADY_BILLED_BY_OTHER) is the
// reconciliation service's responsibility (BE #2).
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
