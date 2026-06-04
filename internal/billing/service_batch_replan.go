package billing

import (
	"context"
	"fmt"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// errBatchItemContractMissing — defensive: classifier inputs no longer contain
// the contract referenced by the batch item. Means the active-contract set
// has shifted since batch generate (contract ENDED/TERMINATED in between).
// Surfaced as 409 because the underlying state is now incompatible with
// re-classification — caller should regenerate the batch instead.
var errBatchItemContractMissing = respond.ErrConflict.WithMessage(
	"สัญญานี้ไม่อยู่ในรายการที่ใช้งานแล้ว ไม่สามารถ replan ได้ กรุณาสร้างรอบบิลใหม่",
)

var errBatchItemAlreadyCommitted = respond.ErrConflict.WithMessage(
	"แถวนี้สร้างบิลแล้ว ไม่สามารถ replan ได้",
)

var errBatchItemMismatch = respond.ErrNotFound.WithMessage("ไม่พบรายการในรอบบิลนี้")

var errBatchAlreadyCommittedForReplan = respond.ErrConflict.WithMessage(
	"รอบบิลนี้ commit แล้ว ไม่สามารถ replan ได้",
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
func (s *billingService) RePlanBatchItem(ctx context.Context, batchID, itemID uuid.UUID) (*BatchItemWithTenant, error) {
	batch, err := s.repo.FindBatchByID(ctx, batchID)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrBatchNotFound
		}
		return nil, fmt.Errorf("find batch: %w", err)
	}
	if batch.CommitStatus != nil && *batch.CommitStatus == CommitStatusCommitted {
		// Fully committed → the batch ledger is closed. PARTIALLY_COMMITTED /
		// FAILED / nil all allow replan because rows that didn't land are still
		// candidates for a follow-up commit attempt.
		return nil, errBatchAlreadyCommittedForReplan
	}

	item, err := s.repo.FindBatchItemByID(ctx, itemID)
	if err != nil {
		if isNotFound(err) {
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

	var contract *ContractWithRoom
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
	var snapshot ComputedSnapshot
	if cls.ResultType == ResultCreated {
		snapshot = computeMonthlyBillSnapshot(
			batch.BillingMonth,
			contract.MonthlyRent,
			contract.ElectricityRatePerUnit,
			contract.WaterRatePerUnit,
			in.meterMap[contract.RoomID],
		)
	}

	if err := s.repo.UpdateBatchItemPlan(
		ctx, item.ID, cls.ResultType, cls.ReasonCode, cls.ReasonText, cls.BillID, snapshot,
	); err != nil {
		return nil, fmt.Errorf("update batch item plan: %w", err)
	}

	updated, err := s.repo.FindBatchItemByIDWithTenant(ctx, item.ID)
	if err != nil {
		return nil, fmt.Errorf("re-read batch item: %w", err)
	}
	return updated, nil
}
