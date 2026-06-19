package billing

import (
	"context"
	"fmt"
	"time"

	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// GenerateSettlement creates a DRAFT settlement bill for the given contract
// and move-out date. Called by the move-out service within its transaction
// context — must NOT start its own transaction.
//
// Delegates to prepareSettlementPlan (read-only computation) then
// commitSettlementPlan (persistence). Same path as CreateSettlementBill.
// Emits CREATE_DRAFT audit with actor=nil because the cross-feature port
// does not currently thread admin userID through.
func (s *billingService) GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error) {
	opts := DefaultSettlementOptions()
	if rentMode != "" {
		opts.RentMode = SettlementRentMode(rentMode)
	}
	plan, err := s.prepareSettlementPlan(ctx, contractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}
	// Snapshot payment destination before commit (same pattern as CreateSettlementBill).
	if c, cErr := s.contracts.FindByIDSimple(ctx, contractID); cErr == nil {
		aptID, roomNum, _ := s.repo.FindRoomApartmentInfo(ctx, c.RoomID)
		applyPaymentSnapshot(&plan.Bill, s.tryResolvePaymentDestination(ctx, aptID, roomNum))
	}
	result, err := s.commitSettlementPlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := s.recordAudit(ctx, result.BillID, AuditCreateDraft, nil, AuditCreateDraftPayload{
		LineItemCount: len(plan.Bill.LineItems),
		TotalAmount:   plan.Bill.TotalAmount,
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// CorrectSettlement implements moveout.BillingCommander — see that interface
// for the contract differentiation (lock + CanCorrect, supersede link via
// DEFERRABLE FK, no carryover). Called by the move-out orchestrator within
// its transaction context — must NOT start its own transaction.
//
// Persist order is load-bearing (per fix 9e86e73 lesson): void old →
// restore absorbed monthlies → prepare fresh plan → commit new. The new
// bill's ID is pre-generated and threaded through plan.Bill.ID so the
// supersede link resolves at COMMIT.
func (s *billingService) CorrectSettlement(ctx context.Context, in moveout.CorrectSettlementInput) (*moveout.SettlementBillResult, error) {
	old, err := s.repo.LockBillForCorrection(ctx, in.ExistingBillID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("lock settlement for correction: %w", err)
	}

	if err := old.CanCorrect(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	// Defense-in-depth: this port is settlement-only. The moveout
	// orchestrator already enforces this via notice.SettlementBillID
	// pointing at a settlement bill, but guard here so a future caller
	// can't quietly misuse the port for monthly bills.
	if !old.IsSettlement() {
		return nil, respond.ErrBadRequest.WithMessage("CorrectSettlement: bill is not a settlement bill")
	}

	previousStatus := string(old.Status)

	// Pre-generate the new bill's ID so old.superseded_by_bill_id can
	// reference it before commitSettlementPlan inserts the new row. The
	// DEFERRABLE FK fires at COMMIT, by which point the new bill exists.
	newBillID := uuid.New()

	// Phase 1: mark the old bill superseded + flush. The status flip to
	// VOID also removes it from any future partial-unique scope on
	// settlement bills (Phase 2.1F backlog adds idx_bills_unique_settlement).
	if err := old.MarkSupersededByCorrection(newBillID); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, old); err != nil {
		return nil, fmt.Errorf("supersede old settlement: %w", err)
	}

	// Phase 2: restore previously-absorbed monthly bills so the fresh plan
	// can re-absorb them (set may differ if the move-out date changed
	// since the original settlement — that's the correct behavior).
	if err := s.restoreAbsorbedBills(ctx, in.ContractID); err != nil {
		return nil, fmt.Errorf("restore absorbed bills: %w", err)
	}

	// Phase 3: fresh settlement plan. Rent mode defaults to the old bill's
	// setting unless the caller overrides — correction is for fixing
	// numbers, not changing policy.
	opts := SettlementOptions{RentMode: old.SettlementRentMode}
	if in.RentMode != "" {
		opts.RentMode = SettlementRentMode(in.RentMode)
	}
	plan, err := s.prepareSettlementPlan(ctx, in.ContractID, in.MoveOutDate, opts)
	if err != nil {
		return nil, err
	}
	// Override the auto-assigned ID with the pre-generated one so the
	// supersede link resolves. Bill.BeforeCreate respects a non-Nil ID.
	plan.Bill.ID = newBillID

	// Re-snapshot payment destination from current rules (correction = new document).
	// Propagate all errors — correction must not silently produce a null-destination
	// document when routing is configured but any lookup or resolver call fails.
	if s.paymentRouting != nil {
		c, cErr := s.contracts.FindByIDSimple(ctx, in.ContractID)
		if cErr != nil {
			return nil, fmt.Errorf("find contract for routing (settlement correction): %w", cErr)
		}
		aptID, roomNum, rErr := s.repo.FindRoomApartmentInfo(ctx, c.RoomID)
		if rErr != nil {
			return nil, fmt.Errorf("find room info for routing (settlement correction): %w", rErr)
		}
		dest, destErr := s.paymentRouting.ResolveDestination(ctx, aptID, roomNum)
		if destErr != nil {
			return nil, fmt.Errorf("resolve payment destination for settlement correction: %w", destErr)
		}
		applyPaymentSnapshot(&plan.Bill, dest)
	}

	result, err := s.commitSettlementPlan(ctx, plan)
	if err != nil {
		return nil, err
	}

	// Phase 4: emit the correction audit pair (mirrors MONTHLY).
	if err := s.recordAudit(ctx, old.ID, AuditSupersede, in.Actor, AuditSupersedePayload{
		PreviousStatus:   previousStatus,
		NewBillID:        newBillID,
		CorrectionReason: in.CorrectionReason,
	}); err != nil {
		return nil, err
	}
	if err := s.recordAudit(ctx, newBillID, AuditCreateFromCorrection, in.Actor, AuditCreateFromCorrectionPayload{
		SupersededBillID: old.ID,
		CorrectionReason: in.CorrectionReason,
	}); err != nil {
		return nil, err
	}

	return result, nil
}

// VoidSettlement marks a settlement bill as VOIDED with the given reason.
// Also restores any monthly bills that were absorbed by this settlement,
// so they can be re-absorbed by a new settlement or collected normally.
// Called by the move-out service within its transaction context.
// Emits VOID audit with actor=nil (cross-feature port).
func (s *billingService) VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error {
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if database.IsNotFound(err) {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill for void: %w", err)
	}
	previousStatus := string(b.Status)
	if err := b.Void(reason); err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, b); err != nil {
		return fmt.Errorf("void settlement: %w", err)
	}
	if err := s.recordAudit(ctx, b.ID, AuditVoid, nil, AuditVoidPayload{
		PreviousStatus: previousStatus,
		Reason:         reason,
	}); err != nil {
		return err
	}
	// Restore absorbed bills so they can be re-collected or re-absorbed
	return s.restoreAbsorbedBills(ctx, b.ContractID)
}

// PreviewSettlementForNotice satisfies moveout.BillingQuerier.
// Delegates to the existing PreviewSettlement and maps to the moveout result type.
func (s *billingService) PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode moveout.RentMode) (*moveout.SettlementPreviewResult, error) {
	input := PreviewSettlementInput{ContractID: contractID}
	if rentMode != "" {
		input.RentMode = SettlementRentMode(rentMode)
	}

	preview, err := s.PreviewSettlement(ctx, input)
	if err != nil {
		return nil, err
	}

	plan := preview.Plan

	items := make([]moveout.SettlementPreviewLineItem, len(plan.Bill.LineItems))
	for i, li := range plan.Bill.LineItems {
		items[i] = moveout.SettlementPreviewLineItem{
			LineType:    string(li.LineType),
			Description: li.Description,
			Amount:      li.Amount,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			SortOrder:   li.SortOrder,
		}
	}

	absorbed := make([]moveout.SettlementPreviewAbsorbedBill, len(plan.BillsToAbsorb))
	for i, b := range plan.BillsToAbsorb {
		absorbed[i] = moveout.SettlementPreviewAbsorbedBill{
			BillID:       b.ID,
			BillingMonth: b.BillingMonth,
			TotalAmount:  b.TotalAmount,
		}
	}

	d := plan.Deposit
	var outcome string
	switch {
	case d.RefundAmount > 0:
		outcome = "REFUND"
	case d.AmountDue > 0:
		outcome = "PAY_MORE"
	default:
		outcome = "ZERO_BALANCE"
	}

	// Derive data quality state from the computed plan.
	dataState := moveout.DataStateComplete
	var warnings []string

	if len(plan.Bill.LineItems) == 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "ไม่มีรายการค่าใช้จ่าย — อาจยังไม่มีข้อมูลมิเตอร์หรือค่าเช่า")
	}

	allZero := len(plan.Bill.LineItems) > 0
	for _, li := range plan.Bill.LineItems {
		if li.Amount != 0 {
			allZero = false
			break
		}
	}
	if allZero && len(plan.Bill.LineItems) > 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "รายการค่าใช้จ่ายทั้งหมดเป็น ฿0 — อาจใช้ค่า default ในการคำนวณ")
	}

	// Only flag zero deposit when the contract actually has deposit but the
	// computed value is 0 — a contract with no deposit is business-valid.
	if plan.Bill.DepositAmount > 0 && d.OriginalAmount == 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "สัญญามีเงินประกันแต่ไม่พบข้อมูลในการคำนวณ")
	}

	return &moveout.SettlementPreviewResult{
		BillingMonth:         plan.Bill.BillingMonth,
		ActualMoveOutDate:    preview.MoveOutDate,
		EffectiveMoveOutDate: preview.EffectiveMoveOutDate,
		RentMode:             string(preview.RentMode),
		RentPaid:             plan.Bill.RentPaid,
		MinMonths:            preview.MinMonths,
		DepositReturnable:    preview.Returnable,
		LineItems:            items,
		TotalAmount:          plan.Bill.TotalAmount,
		Deposit: moveout.SettlementPreviewDeposit{
			Original:  d.OriginalAmount,
			Forfeited: d.ForfeitedAmount,
			Applied:   d.AppliedAmount,
			Refund:    d.RefundAmount,
			Due:       d.AmountDue,
		},
		AbsorbedBills: absorbed,
		Outcome:       outcome,
		DataState:     dataState,
		Warnings:      warnings,
	}, nil
}
