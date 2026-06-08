package billingreconciliation

import (
	"context"
	"errors"
	"strings"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Generate fans the reviewed room_ids[] into per-row monthly-bill creates.
//
// Flow:
//  1. Validate request shape (billing_month format, non-empty room_ids).
//  2. Reconcile the whole apartment for the month — single source of
//     current truth shared across all per-row decisions. Cheaper than
//     N per-room re-classifications, and re-uses the exact bucket rule
//     the workspace already showed the operator.
//  3. Verify every requested room belongs to the apartment. Cross-
//     apartment IDs reject the whole call (request integrity, not a
//     per-row state change).
//  4. For each room_id, in request order, classify-and-commit:
//     • bill already exists           → SKIPPED ALREADY_BILLED_BY_OTHER
//     • bucket != READY               → SKIPPED LOST_READY (no longer billable)
//     • commander returns ErrAlreadyBilled (race) → SKIPPED ALREADY_BILLED
//     • commander returns ErrLostReady (state changed) → SKIPPED LOST_READY
//     • commander returns other error → FAILED with code+message
//     • commander returns CreatedBill → SUCCESS
//
// Per-row failure does NOT abort the loop (Option A pattern). The reviewed
// CTA count is contractual: len(req.RoomIDs) == len(result.Items).
//
// Anti-promotion guard: no batch / session / run / completion object —
// fan-out is a single in-memory loop returning a typed result. See
// feedback_object_restraint_doctrine.md.
func (s *service) Generate(ctx context.Context, req GenerateRequest, actor *uuid.UUID) (*GenerateResult, error) {
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	if len(req.RoomIDs) == 0 {
		return nil, respond.ErrBadRequest.WithMessage("ต้องเลือกห้องอย่างน้อย 1 ห้อง")
	}

	report, err := s.Reconcile(ctx, ReconciliationQuery{
		ApartmentID:  req.ApartmentID.String(),
		BillingMonth: req.BillingMonth,
	})
	if err != nil {
		return nil, err
	}

	rowByID := make(map[uuid.UUID]RoomClassification, len(report.Rooms))
	for _, row := range report.Rooms {
		rowByID[row.Room.RoomID] = row
	}

	var stray []string
	for _, rid := range req.RoomIDs {
		if _, ok := rowByID[rid]; !ok {
			stray = append(stray, rid.String())
		}
	}
	if len(stray) > 0 {
		return nil, respond.ErrBadRequest.WithMessage(
			"ห้องบางห้องไม่อยู่ในอาคารนี้: " + strings.Join(stray, ", "),
		)
	}

	result := &GenerateResult{
		BillingMonth: req.BillingMonth,
		Items:        make([]GenerateItemResult, 0, len(req.RoomIDs)),
	}
	for _, rid := range req.RoomIDs {
		item := s.commitOneRow(ctx, rowByID[rid], req.BillingMonth, actor)
		switch item.ResultType {
		case GenerateItemSuccess:
			result.SuccessCount++
		case GenerateItemSkipped:
			result.SkippedCount++
		case GenerateItemFailed:
			result.FailedCount++
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

// commitOneRow runs the per-row classify-and-commit step in isolation.
// Pure-ish helper: no orchestration, all dependencies are read from s
// (commander) or passed in. Errors from the commander are translated
// into GenerateItemResult — never returned, so Generate's fan-out loop
// stays a clean for-range.
func (s *service) commitOneRow(
	ctx context.Context,
	row RoomClassification,
	billingMonth string,
	actor *uuid.UUID,
) GenerateItemResult {
	item := GenerateItemResult{RoomID: row.Room.RoomID}

	// Pre-pass 1: bill evidence on the row means someone already billed
	// this contract+month. Skip without touching the commander.
	if row.Bill != nil {
		item.ResultType = GenerateItemSkipped
		item.SkipReason = GenerateSkipAlreadyBilled
		return item
	}

	// Pre-pass 2: bucket must be READY for the commit to make sense.
	// Anything else (AR / PD / NB) = the room moved out of "พร้อมออกบิล"
	// between operator preview and this commit.
	//
	// ContractID nil check is defensive — READY rooms always have an
	// active contract, but a race could in theory leave the bucket
	// stale; treat missing contract as LOST_READY rather than crash.
	if row.Bucket != BucketReady || row.Room.ContractID == nil {
		item.ResultType = GenerateItemSkipped
		item.SkipReason = GenerateSkipLostReady
		return item
	}

	created, err := s.commander.CreateMonthlyBillForReconciliation(ctx,
		CreateMonthlyBillForReconciliationRequest{
			ContractID:   *row.Room.ContractID,
			BillingMonth: billingMonth,
		}, actor)
	if err != nil {
		return mapCommitErrorToItem(item, err)
	}

	item.ResultType = GenerateItemSuccess
	item.BillID = &created.ID
	return item
}

// mapCommitErrorToItem translates an error from BillsCommander into a
// GenerateItemResult. Port-level sentinels (ErrAlreadyBilled / ErrLostReady)
// map to SKIPPED with the matching reason; AppErrors surface as FAILED
// with the AppError's code+message so the FE can localize / log; plain
// errors fall through to a generic INFRA_ERROR.
func mapCommitErrorToItem(item GenerateItemResult, err error) GenerateItemResult {
	switch {
	case errors.Is(err, ErrAlreadyBilled):
		item.ResultType = GenerateItemSkipped
		item.SkipReason = GenerateSkipAlreadyBilled
	case errors.Is(err, ErrLostReady):
		item.ResultType = GenerateItemSkipped
		item.SkipReason = GenerateSkipLostReady
	default:
		item.ResultType = GenerateItemFailed
		if appErr, ok := respond.Is(err); ok {
			item.ErrorCode = appErr.Code
			item.ErrorMessage = appErr.Message
		} else {
			item.ErrorCode = "INFRA_ERROR"
			item.ErrorMessage = "เกิดข้อผิดพลาดของระบบ กรุณาลองใหม่อีกครั้ง"
		}
	}
	return item
}

