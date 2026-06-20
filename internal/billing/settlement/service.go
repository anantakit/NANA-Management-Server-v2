package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Service is the settlement billing workflow's public surface.
//
// Concrete struct (NOT an interface) per the W4 audit lock — the moveout-port
// compile-checks in adapter_check.go bind against the concrete type, and the
// workflow surface is large enough that an interface would just be ceremony.
//
// SCOPE NOTE: this is the SETTLEMENT billing workflow only. It is not a
// general document-replacement engine. Two correction flavors stay
// intentionally separate — MONTHLY correction lives at billing root,
// SETTLEMENT correction lives here. See doc.go for the full scope boundary.
type Service struct {
	bills          BillStore
	audit          AuditStore
	contracts      ContractSource
	meters         MeterReadingSource
	configs        BillingConfigSource
	moveOuts       MoveOutSource
	paymentRouting PaymentRoutingSource
	tx             database.TxManager
}

// NewService constructs the settlement workflow service. All port
// dependencies are injected; the constructor returns *Service (concrete)
// rather than an interface because the moveout-port compile-check in
// adapter_check.go binds against the concrete type.
//
// PaymentRoutingSource may be nil — settlement degrades gracefully to a
// null payment-destination snapshot when no rules are configured, mirroring
// the existing billing root behavior.
func NewService(
	bills BillStore,
	audit AuditStore,
	contracts ContractSource,
	meters MeterReadingSource,
	configs BillingConfigSource,
	moveOuts MoveOutSource,
	paymentRouting PaymentRoutingSource,
	tx database.TxManager,
) *Service {
	return &Service{
		bills:          bills,
		audit:          audit,
		contracts:      contracts,
		meters:         meters,
		configs:        configs,
		moveOuts:       moveOuts,
		paymentRouting: paymentRouting,
		tx:             tx,
	}
}

// --- Workflow methods ---

// PreviewSettlement computes settlement data without persisting anything.
// Calls prepareSettlementPlan (same path as create) but skips commitSettlementPlan.
func (s *Service) PreviewSettlement(ctx context.Context, input PreviewSettlementInput) (*SettlementPreview, error) {
	// Resolve move-out date from notice (same as CreateSettlementBill)
	notice, err := s.moveOuts.FindActiveByContractID(ctx, input.ContractID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := billing.DefaultSettlementOptions()
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
func (s *Service) CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest, actor *uuid.UUID) (*billing.BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}

	// Resolve actual move-out date from notice
	notice, err := s.moveOuts.FindActiveByContractID(ctx, contractID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := billing.DefaultSettlementOptions()
	if req.RentMode != "" {
		opts.RentMode = billing.SettlementRentMode(req.RentMode)
	}

	// Resolve payment destination outside TX (read-only). Null if no rules configured.
	var paymentDest *apartment.PaymentDestinationInfo
	if c, cErr := s.contracts.FindByIDSimple(ctx, contractID); cErr == nil {
		aptID, roomNum, _ := s.bills.FindRoomApartmentInfo(ctx, c.RoomID)
		paymentDest = s.tryResolvePaymentDestination(ctx, aptID, roomNum)
	}

	var billID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		plan, pErr := s.prepareSettlementPlan(txCtx, contractID, moveOutDate, opts)
		if pErr != nil {
			return pErr
		}
		billing.ApplyPaymentSnapshot(&plan.Bill, paymentDest)
		result, cErr := s.commitSettlementPlan(txCtx, plan)
		if cErr != nil {
			return cErr
		}
		billID = result.BillID
		return s.audit.RecordAudit(txCtx, billID, billing.AuditCreateDraft, actor, billing.AuditCreateDraftPayload{
			LineItemCount: len(plan.Bill.LineItems),
			TotalAmount:   plan.Bill.TotalAmount,
		})
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return s.bills.FindByIDWithRelations(ctx, billID)
}

// UpdateSettlementDraft replaces all MANUAL line items and updates the note
// on a DRAFT settlement bill. AUTO items are untouched.
//
// Emits diff-based audit events (UPDATE_OVERRIDE per key changed, REMOVE/
// ADD_MANUAL_ITEM per item, UPDATE_NOTE if changed) inside the same TX as
// the state mutation — audit failure rolls back the edit.
func (s *Service) UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest, actor *uuid.UUID) (*billing.BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.bills.FindByID(txCtx, id)
		if err != nil {
			if database.IsNotFound(err) {
				return billing.ErrBillNotFound
			}
			return fmt.Errorf("find bill: %w", err)
		}
		if !b.IsDraft() {
			return respond.ErrBadRequest.WithMessage(billing.ErrNotDraft.Error())
		}
		if !b.IsSettlement() {
			return respond.ErrBadRequest.WithMessage("แก้ไขได้เฉพาะบิลปิดสัญญา")
		}

		// Capture BEFORE snapshots for diff-based audit. Clone the override
		// map since `b` and the reloaded bill share the same pointer in the
		// repo and we mutate Overrides in place below.
		oldManuals := b.ManualItems()
		oldNote := b.Note
		oldOverrides := make(billing.OverrideMap, len(b.Overrides))
		for k, v := range b.Overrides {
			oldOverrides[k] = v
		}

		// Validate manual line types BEFORE any DB write so the bill is never
		// left half-edited inside the TX on invalid input.
		for _, item := range req.ManualItems {
			if !billing.IsValidManualLineType(billing.LineItemType(item.LineType)) {
				return respond.ErrBadRequest.WithMessage(
					fmt.Sprintf("ประเภทรายการ %q ไม่สามารถเพิ่มเองได้", item.LineType))
			}
		}

		// Delete existing MANUAL items
		if err := s.bills.DeleteLineItemsBySource(txCtx, id, billing.LineItemSourceManual); err != nil {
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
		var manualItems []billing.BillLineItem
		for i, item := range req.ManualItems {
			li := billing.BillLineItem{
				BillID:      id,
				LineType:    billing.LineItemType(item.LineType),
				Source:      billing.LineItemSourceManual,
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
		if err := s.bills.CreateLineItems(txCtx, manualItems); err != nil {
			return fmt.Errorf("create manual items: %w", err)
		}

		// Reload line items and recompute totals
		reloaded, err := s.bills.FindByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("reload bill: %w", err)
		}

		// Apply overrides
		if req.Overrides != nil {
			overrides := make(billing.OverrideMap, len(req.Overrides))
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
			app := billing.DepositApplication(*req.DepositApplication)
			if !app.IsValid() {
				return respond.ErrBadRequest.WithMessage("deposit_application ต้องเป็น FULL, NONE, หรือ CUSTOM")
			}
			reloaded.DepositApp = app
			if app == billing.DepositAppCustom && req.CustomDepositApplied != nil {
				reloaded.CustomDepositApplied = money.ToSatang(*req.CustomDepositApplied)
			} else if app != billing.DepositAppCustom {
				reloaded.CustomDepositApplied = 0
			}
		}

		reloaded.CalculateTotal()
		if req.Note != nil {
			reloaded.Note = *req.Note
		}
		if err := s.bills.UpdateBill(txCtx, reloaded); err != nil {
			return err
		}
		return billing.EmitDraftEditAudit(txCtx, s.audit, id, actor, oldManuals, manualItems, oldOverrides, reloaded.Overrides, oldNote, reloaded.Note)
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("update settlement draft: %w", err)
	}

	return s.bills.FindByIDWithRelations(ctx, id)
}

// FinalizeSettlement recomputes totals from line items and marks the DRAFT
// settlement bill as FINALIZED. Called by the move-out service via port,
// AND also reachable as a public Service method.
//
// Move-out service owns the parent transaction (per the cross-feature port
// contract), so this method uses the inherited ctx directly. Audit row is
// emitted on the same ctx and inherits the same TX — failure rolls back
// the move-out workflow step. Actor is nil because the cross-feature port
// does not currently thread admin userID through.
func (s *Service) FinalizeSettlement(ctx context.Context, billID uuid.UUID) error {
	b, err := s.bills.FindByID(ctx, billID)
	if err != nil {
		if database.IsNotFound(err) {
			return billing.ErrBillNotFound
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
	if err := s.bills.UpdateBill(ctx, b); err != nil {
		return err
	}
	return s.audit.RecordAudit(ctx, b.ID, billing.AuditFinalize, nil, billing.AuditFinalizePayload{
		PreviousStatus: previousStatus,
		TotalAmount:    b.TotalAmount,
	})
}

// RegenerateSettlement voids the existing draft, creates a new DRAFT with
// fresh AUTO items, and preserves MANUAL items + note from the old bill.
// The persisted SettlementRentMode is carried over to the new bill.
func (s *Service) RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error) {
	// Load existing bill to extract MANUAL items + note + rent mode + overrides + deposit app
	existing, err := s.bills.FindByID(ctx, existingBillID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, billing.ErrBillNotFound
		}
		return nil, fmt.Errorf("find existing bill: %w", err)
	}
	manualItems := existing.ManualItems()
	note := existing.Note
	existingOverrides := existing.Overrides
	existingDepositApp := existing.DepositApp
	existingCustomDeposit := existing.CustomDepositApplied
	opts := billing.SettlementOptions{RentMode: existing.SettlementRentMode}
	// Override rent mode if explicitly provided
	if rentMode != "" {
		opts.RentMode = billing.SettlementRentMode(rentMode)
	}

	// Void the existing bill
	previousStatus := string(existing.Status)
	if err := existing.Void("REGENERATED"); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.bills.UpdateBill(ctx, existing); err != nil {
		return nil, fmt.Errorf("void existing bill: %w", err)
	}
	if err := s.audit.RecordAudit(ctx, existing.ID, billing.AuditVoid, nil, billing.AuditVoidPayload{
		PreviousStatus: previousStatus,
		Reason:         "REGENERATED",
	}); err != nil {
		return nil, err
	}

	// Restore absorbed bills so they can be re-absorbed
	if err := s.restoreAbsorbedBills(ctx, contractID); err != nil {
		return nil, fmt.Errorf("restore absorbed bills: %w", err)
	}

	// Resolve payment destination before plan preparation (mirrors CreateSettlementBill).
	// tryResolvePaymentDestination swallows routing errors with a warning — generate/regen
	// paths use the soft policy; only correction paths hard-fail on routing errors.
	var paymentDest *apartment.PaymentDestinationInfo
	if c, cErr := s.contracts.FindByIDSimple(ctx, contractID); cErr == nil {
		aptID, roomNum, _ := s.bills.FindRoomApartmentInfo(ctx, c.RoomID)
		paymentDest = s.tryResolvePaymentDestination(ctx, aptID, roomNum)
	}

	// Generate fresh AUTO items with the same rent mode
	plan, err := s.prepareSettlementPlan(ctx, contractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}
	billing.ApplyPaymentSnapshot(&plan.Bill, paymentDest)
	result, err := s.commitSettlementPlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := s.audit.RecordAudit(ctx, result.BillID, billing.AuditCreateDraft, nil, billing.AuditCreateDraftPayload{
		LineItemCount: len(plan.Bill.LineItems),
		TotalAmount:   plan.Bill.TotalAmount,
	}); err != nil {
		return nil, err
	}

	// Carry over MANUAL items + note + overrides + deposit app to the new bill
	needsCarryOver := len(manualItems) > 0 || note != "" || len(existingOverrides) > 0 || existingDepositApp != billing.DepositAppFull
	if needsCarryOver {
		newBill, err := s.bills.FindByID(ctx, result.BillID)
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
			if err := s.bills.CreateLineItems(ctx, manualItems); err != nil {
				return nil, fmt.Errorf("carry over manual items: %w", err)
			}

			// Reload to include manual items in LineItems slice
			newBill, err = s.bills.FindByID(ctx, result.BillID)
			if err != nil {
				return nil, fmt.Errorf("reload new bill: %w", err)
			}
		}

		newBill.Note = note

		// Carry over overrides — prune keys that no longer match new AUTO items
		if len(existingOverrides) > 0 {
			carried := make(billing.OverrideMap, len(existingOverrides))
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
		if err := s.bills.UpdateBill(ctx, newBill); err != nil {
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

// --- Helpers (instance-scoped) ---

// tryResolvePaymentDestination resolves the payment destination for a room.
// Returns nil on error or when no rules are configured — never blocks bill
// creation. Mirrors billing root's billingService.tryResolvePaymentDestination
// (intentional duplicate ~10 LOC — settlement uses its own PaymentRoutingSource
// port rather than reaching into billing's private helper, see W4 audit §3).
func (s *Service) tryResolvePaymentDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) *apartment.PaymentDestinationInfo {
	if s.paymentRouting == nil {
		return nil
	}
	dest, err := s.paymentRouting.ResolveDestination(ctx, apartmentID, roomNumber)
	if err != nil {
		slog.Warn("payment routing resolve failed, bill will have null destination",
			"apartment_id", apartmentID, "room_number", roomNumber, "error", err)
		return nil
	}
	return dest
}
