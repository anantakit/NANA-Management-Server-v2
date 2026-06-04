package billing

import (
	"context"
	"fmt"
	"time"

	"nana/internal/moveout"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PreviewSettlement computes settlement data without persisting anything.
// Calls prepareSettlementPlan (same path as create) but skips commitSettlementPlan.
func (s *billingService) PreviewSettlement(ctx context.Context, input PreviewSettlementInput) (*SettlementPreview, error) {
	// Resolve move-out date from notice (same as CreateSettlementBill)
	notice, err := s.moveOuts.FindActiveByContractID(ctx, input.ContractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := DefaultSettlementOptions()
	opts.SkipDuplicateGuard = true // preview is read-only — must work even when draft exists
	if input.RentMode != "" {
		opts.RentMode = input.RentMode
	}

	plan, err := s.prepareSettlementPlan(ctx, input.ContractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}

	return &SettlementPreview{
		Plan:                 plan,
		MinMonths:            plan.MinMonths,
		Returnable:           plan.DepositReturnable,
		MoveOutDate:          moveOutDate,
		EffectiveMoveOutDate: effectiveMoveOutDate(moveOutDate, opts.RentMode),
		RentMode:             opts.RentMode,
	}, nil
}

// CreateSettlementBill is the REST endpoint adapter (POST /bills/settlement).
// Thin adapter: resolves actual move-out date from notice, then delegates to
// the shared prepareSettlementPlan → commitSettlementPlan path.
//
// Same settlement semantics as GenerateSettlement (absorption, deposit, duplicate guard).
// Emits CREATE_DRAFT audit inside the same TX as the bill insert.
func (s *billingService) CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}

	// Resolve actual move-out date from notice
	notice, err := s.moveOuts.FindActiveByContractID(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := DefaultSettlementOptions()
	if req.RentMode != "" {
		opts.RentMode = SettlementRentMode(req.RentMode)
	}

	var billID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		plan, pErr := s.prepareSettlementPlan(txCtx, contractID, moveOutDate, opts)
		if pErr != nil {
			return pErr
		}
		result, cErr := s.commitSettlementPlan(txCtx, plan)
		if cErr != nil {
			return cErr
		}
		billID = result.BillID
		return s.recordAudit(txCtx, billID, AuditCreateDraft, actor, AuditCreateDraftPayload{
			LineItemCount: len(plan.Bill.LineItems),
			TotalAmount:   plan.Bill.TotalAmount,
		})
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, billID)
}

// UpdateSettlementDraft replaces all MANUAL line items and updates the note
// on a DRAFT settlement bill. AUTO items are untouched.
//
// Emits diff-based audit events (UPDATE_OVERRIDE per key changed, REMOVE/
// ADD_MANUAL_ITEM per item, UPDATE_NOTE if changed) inside the same TX as
// the state mutation — audit failure rolls back the edit.
func (s *billingService) UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrBillNotFound
			}
			return fmt.Errorf("find bill: %w", err)
		}
		if !b.IsDraft() {
			return respond.ErrBadRequest.WithMessage(ErrNotDraft.Error())
		}
		if !b.IsSettlement() {
			return respond.ErrBadRequest.WithMessage("แก้ไขได้เฉพาะบิลปิดสัญญา")
		}

		// Capture BEFORE snapshots for diff-based audit. Clone the override
		// map since `b` and the reloaded bill share the same pointer in the
		// repo and we mutate Overrides in place below.
		oldManuals := b.ManualItems()
		oldNote := b.Note
		oldOverrides := make(OverrideMap, len(b.Overrides))
		for k, v := range b.Overrides {
			oldOverrides[k] = v
		}

		// Validate manual line types BEFORE any DB write so the bill is never
		// left half-edited inside the TX on invalid input.
		for _, item := range req.ManualItems {
			if !IsValidManualLineType(LineItemType(item.LineType)) {
				return respond.ErrBadRequest.WithMessage(
					fmt.Sprintf("ประเภทรายการ %q ไม่สามารถเพิ่มเองได้", item.LineType))
			}
		}

		// Delete existing MANUAL items
		if err := s.repo.DeleteLineItemsBySource(txCtx, id, LineItemSourceManual); err != nil {
			return fmt.Errorf("delete manual items: %w", err)
		}

		// MANUAL sort_order appends after the last AUTO item. Use max(sort_order)
		// not count: AUTO rows can have holes from prior edits or future schema
		// migrations — count-based baseOrder would collide with existing AUTO
		// sort_orders. max+1 guarantees MANUAL lands strictly after every AUTO
		// row regardless of holes. Mirrors UpdateMonthlyDraft so both paths
		// share the same MANUAL sort_order contract.
		maxSort := 0
		for _, li := range b.LineItems {
			if li.IsAuto() && li.SortOrder > maxSort {
				maxSort = li.SortOrder
			}
		}
		baseOrder := maxSort + 1
		var manualItems []BillLineItem
		for i, item := range req.ManualItems {
			li := BillLineItem{
				BillID:      id,
				LineType:    LineItemType(item.LineType),
				Source:      LineItemSourceManual,
				Description: item.Description,
				SortOrder:   baseOrder + i,
			}
			if item.Quantity != nil && item.UnitPrice != nil && *item.Quantity > 0 {
				// Quantity mode: compute amount from quantity × unit_price
				li.Quantity = *item.Quantity
				li.UnitPrice = money.ToSatang(*item.UnitPrice)
				li.Amount = int64(*item.Quantity) * li.UnitPrice
			} else {
				li.Amount = money.ToSatang(item.Amount)
			}
			manualItems = append(manualItems, li)
		}
		if err := s.repo.CreateLineItems(txCtx, manualItems); err != nil {
			return fmt.Errorf("create manual items: %w", err)
		}

		// Reload line items and recompute totals
		reloaded, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("reload bill: %w", err)
		}

		// Apply overrides
		if req.Overrides != nil {
			overrides := make(OverrideMap, len(req.Overrides))
			for key, amount := range req.Overrides {
				overrides[key] = money.ToSatang(amount)
			}
			reloaded.Overrides = overrides
			if err := reloaded.ValidateOverrides(); err != nil {
				return respond.ErrBadRequest.WithMessage(err.Error())
			}
		}

		// Apply deposit application
		if req.DepositApplication != nil {
			app := DepositApplication(*req.DepositApplication)
			if !app.IsValid() {
				return respond.ErrBadRequest.WithMessage("deposit_application ต้องเป็น FULL, NONE, หรือ CUSTOM")
			}
			reloaded.DepositApp = app
			if app == DepositAppCustom && req.CustomDepositApplied != nil {
				reloaded.CustomDepositApplied = money.ToSatang(*req.CustomDepositApplied)
			} else if app != DepositAppCustom {
				reloaded.CustomDepositApplied = 0
			}
		}

		reloaded.CalculateTotal()
		if req.Note != nil {
			reloaded.Note = *req.Note
		}
		if err := s.repo.Update(txCtx, reloaded); err != nil {
			return err
		}
		return s.emitDraftEditAudit(txCtx, id, actor, oldManuals, manualItems, oldOverrides, reloaded.Overrides, oldNote, reloaded.Note)
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("update settlement draft: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, id)
}

// FinalizeSettlement recomputes totals from line items and marks the DRAFT
// settlement bill as FINALIZED. Called by the move-out service via port.
//
// Move-out service owns the parent transaction (per service_moveout_ports.go
// header convention), so this method uses the inherited ctx directly. Audit
// row is emitted on the same ctx and inherits the same TX — failure rolls
// back the move-out workflow step. Actor is nil because the cross-feature
// port does not currently thread admin userID through.
func (s *billingService) FinalizeSettlement(ctx context.Context, billID uuid.UUID) error {
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill: %w", err)
	}
	if !b.IsSettlement() {
		return respond.ErrBadRequest.WithMessage("สรุปยอดได้เฉพาะบิลปิดสัญญา")
	}

	// Recompute totals from source of truth
	b.CalculateTotal()

	previousStatus := string(b.Status)
	if err := b.Finalize(); err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, b); err != nil {
		return err
	}
	return s.recordAudit(ctx, b.ID, AuditFinalize, nil, AuditFinalizePayload{
		PreviousStatus: previousStatus,
		TotalAmount:    b.TotalAmount,
	})
}

// RegenerateSettlement voids the existing draft, creates a new DRAFT with
// fresh AUTO items, and preserves MANUAL items + note from the old bill.
// The persisted SettlementRentMode is carried over to the new bill.
func (s *billingService) RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error) {
	// Load existing bill to extract MANUAL items + note + rent mode + overrides + deposit app
	existing, err := s.repo.FindByID(ctx, existingBillID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find existing bill: %w", err)
	}
	manualItems := existing.ManualItems()
	note := existing.Note
	existingOverrides := existing.Overrides
	existingDepositApp := existing.DepositApp
	existingCustomDeposit := existing.CustomDepositApplied
	opts := SettlementOptions{RentMode: existing.SettlementRentMode}
	// Override rent mode if explicitly provided
	if rentMode != "" {
		opts.RentMode = SettlementRentMode(rentMode)
	}

	// Void the existing bill
	previousStatus := string(existing.Status)
	if err := existing.Void("REGENERATED"); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("void existing bill: %w", err)
	}
	if err := s.recordAudit(ctx, existing.ID, AuditVoid, nil, AuditVoidPayload{
		PreviousStatus: previousStatus,
		Reason:         "REGENERATED",
	}); err != nil {
		return nil, err
	}

	// Restore absorbed bills so they can be re-absorbed
	if err := s.restoreAbsorbedBills(ctx, contractID); err != nil {
		return nil, fmt.Errorf("restore absorbed bills: %w", err)
	}

	// Generate fresh AUTO items with the same rent mode
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

	// Carry over MANUAL items + note + overrides + deposit app to the new bill
	needsCarryOver := len(manualItems) > 0 || note != "" || len(existingOverrides) > 0 || existingDepositApp != DepositAppFull
	if needsCarryOver {
		newBill, err := s.repo.FindByID(ctx, result.BillID)
		if err != nil {
			return nil, fmt.Errorf("reload new bill: %w", err)
		}

		// Assign MANUAL items to the new bill with correct sort order
		autoCount := len(newBill.LineItems)
		for i := range manualItems {
			manualItems[i].ID = uuid.Nil // new row
			manualItems[i].BillID = result.BillID
			manualItems[i].SortOrder = autoCount + 1 + i
		}
		if len(manualItems) > 0 {
			if err := s.repo.CreateLineItems(ctx, manualItems); err != nil {
				return nil, fmt.Errorf("carry over manual items: %w", err)
			}

			// Reload to include manual items in LineItems slice
			newBill, err = s.repo.FindByID(ctx, result.BillID)
			if err != nil {
				return nil, fmt.Errorf("reload new bill: %w", err)
			}
		}

		newBill.Note = note

		// Carry over overrides — prune keys that no longer match new AUTO items
		if len(existingOverrides) > 0 {
			carried := make(OverrideMap, len(existingOverrides))
			for k, v := range existingOverrides {
				carried[k] = v
			}
			newBill.Overrides = carried
			newBill.PruneStaleOverrides()
		}

		// Carry over deposit application
		newBill.DepositApp = existingDepositApp
		newBill.CustomDepositApplied = existingCustomDeposit

		newBill.CalculateTotal() // applies overrides + deposit app
		if err := s.repo.Update(ctx, newBill); err != nil {
			return nil, fmt.Errorf("update new bill: %w", err)
		}

		// Recompute net amount using single source of truth
		ds := computeDepositSettlementFromBill(newBill)
		updated := toSettlementResult(result.BillID, ds)
		result.NetAmount = updated.NetAmount
		result.DepositUsed = updated.DepositUsed
	}

	return result, nil
}
