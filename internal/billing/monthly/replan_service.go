package monthly

import (
	"context"
	"fmt"

	"nana/internal/billing"
	"nana/internal/shared/database"

	"github.com/google/uuid"
)

// RePlanBatchItem re-evaluates one batch item against the current state and
// rewrites its snapshot + classification in place. Used after operator
// changes that affect classification mid-batch (recording a missing meter,
// retroactively creating/cancelling a move-out notice).
//
// Pre-conditions:
//   - batch not fully COMMITTED (otherwise the commit ledger is closed)
//   - item belongs to this batch and has no bill_id yet
//
// Flow (no tx — single-row update + read-only classifier load):
//  1. load batch, guard CommitStatus
//  2. load item, guard BatchID match + BillID == nil
//  3. run the same planner pair used by BatchCreateMonthlyBills +
//     PreflightMonthly (loadBatchInputs + classifyContractForBatch) so the
//     verdict is byte-equivalent to a fresh generate
//  4. write back result_type / reason_code / reason_text / bill_id /
//     computed_snapshot
//  5. re-read with tenant + bill_status join for the response payload
//
// Race posture: no row lock. Concurrent commit attempts on this batch
// won't see the updated classification until the next ListCommitPendingItems
// read, which is the same eventual-consistency window the batch already
// accepts between admin actions.
func (s *service) RePlanBatchItem(ctx context.Context, batchID, itemID uuid.UUID) (*billing.BatchItemWithTenant, error) {
	batch, err := s.batches.FindBatchByID(ctx, batchID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, billing.ErrBatchNotFound
		}
		return nil, fmt.Errorf("find batch: %w", err)
	}
	if batch.CommitStatus != nil && *batch.CommitStatus == billing.CommitStatusCommitted {
		// Fully committed → the batch ledger is closed. PARTIALLY_COMMITTED /
		// FAILED / nil all allow replan because rows that didn't land are still
		// candidates for a follow-up commit attempt.
		return nil, errBatchAlreadyCommittedForReplan
	}

	item, err := s.batches.FindBatchItemByID(ctx, itemID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, errBatchItemMismatch
		}
		return nil, fmt.Errorf("find batch item: %w", err)
	}
	if item.BatchID != batchID {
		return nil, errBatchItemMismatch
	}
	if item.BillID != nil {
		return nil, errBatchItemAlreadyCommitted
	}

	in, err := s.loadBatchInputs(ctx, batch.ApartmentID, batch.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("load batch inputs: %w", err)
	}

	var contract *billing.ContractWithRoom
	for i := range in.contracts {
		if in.contracts[i].ContractID == item.ContractID {
			contract = &in.contracts[i]
			break
		}
	}
	if contract == nil {
		return nil, errBatchItemContractMissing
	}

	cls := classifyContractForBatch(
		*contract, in.startOfMonth, in.endOfMonth,
		in.pendingMoveOuts, in.meterMap, in.existingMap,
	)
	var snapshot billing.ComputedSnapshot
	if cls.ResultType == billing.ResultCreated {
		// Utility-scoped recovery overlay (owner lock 2026-07-18): bill the
		// unaffected utility's real usage from the coexisting consumption row when
		// a recovery anchor governs the month. No-op otherwise.
		reading := billing.ProjectRecoveryUsageOverlay(in.meterMap[contract.RoomID], in.consumptionMap[contract.RoomID])
		recon, rErr := billing.ResolveRecoveryReconciliation(ctx, reading, contract.ContractID, s.bills)
		if rErr != nil {
			return nil, fmt.Errorf("resolve recovery reconciliation: %w", rErr)
		}
		snapshot = billing.ComputeMonthlyBillSnapshot(
			batch.BillingMonth,
			contract.MonthlyRent,
			contract.ElectricityRatePerUnit,
			contract.WaterRatePerUnit,
			reading,
			recon,
		)
	}

	if err := s.batches.UpdateBatchItemPlan(
		ctx, item.ID, cls.ResultType, cls.ReasonCode, cls.ReasonText, cls.BillID, snapshot,
	); err != nil {
		return nil, fmt.Errorf("update batch item plan: %w", err)
	}

	updated, err := s.batches.FindBatchItemByIDWithTenant(ctx, item.ID)
	if err != nil {
		return nil, fmt.Errorf("re-read batch item: %w", err)
	}
	return updated, nil
}
