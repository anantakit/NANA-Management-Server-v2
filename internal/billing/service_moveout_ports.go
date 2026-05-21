package billing

import (
	"context"
	"fmt"
	"time"

	"nana/internal/moveout"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// VoidSettlement marks a settlement bill as VOIDED with the given reason.
// Also restores any monthly bills that were absorbed by this settlement,
// so they can be re-absorbed by a new settlement or collected normally.
// Called by the move-out service within its transaction context.
// Emits VOID audit with actor=nil (cross-feature port).
func (s *billingService) VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error {
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
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
