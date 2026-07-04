package billing

import (
	"context"
	"fmt"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// UpdateMonthlyDraft replaces all MANUAL line items, applies overrides to AUTO
// line item amounts, and updates the note on a DRAFT monthly bill.
//
// Mirrors UpdateSettlementDraft minus the deposit-application path (monthly
// bills have no deposit concept). Reuses ValidateOverrides + the shared
// overrideableLineTypes map from the domain layer, which already covers every
// monthly AUTO line type (ROOM_RENT, ELECTRICITY, WATER).
//
// Sort-order invariant (locked, see project_billing_editable_monthly_arch_lock.md):
//   - AUTO items keep their existing sort_order — never mutated by this method
//   - MANUAL items append after the last AUTO item in request order
//   - Empty manual_items array is valid → bill ends up with only AUTO items
//
// State guards:
//   - Bill must be DRAFT (FINALIZED / PAID / VOID rejected with AppError)
//   - Bill must be MONTHLY (SETTLEMENT routed to UpdateSettlementDraft)
//
// `actor` is captured at the param boundary so the audit recorder can be
// threaded in the next commit without re-touching every callsite. Today it
// is intentionally unused — the underscore assignment documents that.
func (s *billingService) UpdateMonthlyDraft(ctx context.Context, id uuid.UUID, req UpdateMonthlyDraftRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			if database.IsNotFound(err) {
				return ErrBillNotFound
			}
			return fmt.Errorf("find bill: %w", err)
		}
		if !b.IsDraft() {
			return respond.ErrBadRequest.WithMessage(ErrNotDraft.Error())
		}
		if !b.IsMonthly() {
			return respond.ErrBadRequest.WithMessage("แก้ไขได้เฉพาะบิลรายเดือน")
		}

		// Validate manual line types BEFORE touching the DB so an invalid
		// request never leaves the bill in a half-edited state inside the TX.
		for _, item := range req.ManualItems {
			if !IsValidManualLineType(LineItemType(item.LineType)) {
				return respond.ErrBadRequest.WithMessage(
					fmt.Sprintf("ประเภทรายการ %q ไม่สามารถเพิ่มเองได้", item.LineType))
			}
		}

		// Capture BEFORE snapshots for diff-based audit. Clone Overrides since
		// `b` and the reloaded bill share the same pointer in the repo and
		// the mutation below replaces the map reference in place.
		oldManuals := b.ManualItems()
		oldNote := b.Note
		oldOverrides := make(OverrideMap, len(b.Overrides))
		for k, v := range b.Overrides {
			oldOverrides[k] = v
		}

		// Wipe existing MANUAL items — request is the new source of truth.
		// AUTO items are never touched.
		if err := s.repo.DeleteLineItemsBySource(txCtx, id, LineItemSourceManual); err != nil {
			return fmt.Errorf("delete manual items: %w", err)
		}

		// MANUAL sort_order appends after the last AUTO item. Use max(sort_order)
		// not count: AUTO rows can have holes (e.g. [1, 3] from prior edits or
		// future schema migrations) — count-based baseOrder would collide with
		// existing AUTO sort_orders. max+1 guarantees MANUAL lands strictly
		// after every AUTO row regardless of holes. Edge case: no AUTO items
		// → maxSort stays 0 → MANUAL starts at sort_order 1.
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
				// Quantity mode: amount = qty × unit_price (rounded at satang layer).
				li.Quantity = *item.Quantity
				li.UnitPrice = money.ToSatang(*item.UnitPrice)
				li.Amount = int64(*item.Quantity) * li.UnitPrice
			} else {
				li.Amount = money.ToSatang(item.Amount)
			}
			manualItems = append(manualItems, li)
		}
		if len(manualItems) > 0 {
			if err := s.repo.CreateLineItems(txCtx, manualItems); err != nil {
				return fmt.Errorf("create manual items: %w", err)
			}
		}

		// Reload to include the freshly-created MANUAL items + recompute totals
		// off the latest persisted state.
		reloaded, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("reload bill: %w", err)
		}

		// Apply overrides — validate before assigning so the bad-key error
		// surfaces as AppError (not opaque 500 from CalculateTotal).
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

		// Phase 7 — apply baseline corrections. Each entry materializes one
		// ADJUSTMENT BillLineItem (MANUAL source, FK to recovery row, audit
		// emission), gated by a per-row applied-state race check so a
		// concurrent finalize-then-edit can't double-apply.
		appliedLines, err := s.applyBaselineCorrections(txCtx, id, reloaded, req.AppliedCorrections, actor)
		if err != nil {
			return err
		}
		if len(appliedLines) > 0 {
			// Reload again so the appended ADJUSTMENT lines roll into CalculateTotal.
			reloaded, err = s.repo.FindByID(txCtx, id)
			if err != nil {
				return fmt.Errorf("reload bill after correction apply: %w", err)
			}
			if req.Overrides != nil {
				overrides := make(OverrideMap, len(req.Overrides))
				for key, amount := range req.Overrides {
					overrides[key] = money.ToSatang(amount)
				}
				reloaded.Overrides = overrides
			}
		}

		reloaded.CalculateTotal()
		if req.Note != nil {
			reloaded.Note = *req.Note
		}
		if err := s.repo.Update(txCtx, reloaded); err != nil {
			return err
		}
		return EmitDraftEditAudit(txCtx, s, id, actor, oldManuals, manualItems, oldOverrides, reloaded.Overrides, oldNote, reloaded.Note)
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("update monthly draft: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, id)
}

// applyBaselineCorrections materializes one ADJUSTMENT BillLineItem per
// input + emits APPLY_RECOVERY_ADJUSTMENT audit entries. Race-safe: each
// row re-checks PENDING state via the inverse-FK probe before insert, so
// a concurrent application (or finalize-then-edit-then-edit) cannot
// double-apply the same recovery.
//
// Phase 7 (locked 2026-06-25): operator-authoritative Amount + Note
// arrive on the bill side. The meter recovery row is loaded only to
// confirm anchor identity + retrieve source_billing_month for the
// tenant-visible Description.
//
// Reuses ValidateAdjustment from the domain layer (Phase 4) — the same
// invariant guard already used by recovery_adapter.go. Audit shape
// matches Phase 5 (AuditApplyRecoveryAdjustmentPayload) so historical
// audit rows from the old recovery-side path remain readable alongside
// the new bill-side rows.
func (s *billingService) applyBaselineCorrections(
	txCtx context.Context,
	billID uuid.UUID,
	bill *Bill,
	inputs []AppliedCorrectionInput,
	actor *uuid.UUID,
) ([]BillLineItem, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	// Append AFTER the current max sort_order so the ADJUSTMENT rows live
	// at the bottom of the bill (operator-friendly grouping).
	maxSort := 0
	for _, li := range bill.LineItems {
		if li.SortOrder > maxSort {
			maxSort = li.SortOrder
		}
	}

	created := make([]BillLineItem, 0, len(inputs))
	for i, in := range inputs {
		recoveryID, err := uuid.Parse(in.RecoveryReadingID)
		if err != nil {
			return nil, respond.ErrBadRequest.WithMessage(
				fmt.Sprintf("รหัสการปรับฐานไม่ถูกต้อง (รายการที่ %d)", i+1))
		}

		// Load the recovery meter row to confirm anchor identity +
		// recover source_billing_month for the tenant-visible Description.
		recovery, err := s.meters.FindByIDSimple(txCtx, recoveryID)
		if err != nil {
			if database.IsNotFound(err) {
				return nil, respond.ErrNotFound.WithMessage(
					fmt.Sprintf("ไม่พบรายการปรับฐาน (รายการที่ %d)", i+1))
			}
			return nil, fmt.Errorf("load recovery %s: %w", recoveryID, err)
		}
		if recovery.AnchorReason == nil || *recovery.AnchorReason != meterreading.AnchorReasonReadingRecovery {
			return nil, respond.ErrBadRequest.WithMessage(
				fmt.Sprintf("รายการปรับฐานไม่ถูกต้อง (รายการที่ %d)", i+1))
		}
		// Source-optional (locked 2026-07-01): a nil-source recovery is a
		// valid, complete correction and remains applicable to a bill. The
		// source month is used only to enrich the tenant-visible description;
		// when absent, the description falls back to a source-less variant.
		// No inference is attempted to recover the missing month.
		sourceMonth := ""
		if recovery.RecoverySourceReadingID != nil {
			source, err := s.meters.FindByIDSimple(txCtx, *recovery.RecoverySourceReadingID)
			if err == nil && source.BillingMonth != nil {
				sourceMonth = *source.BillingMonth
			}
		}

		// Race guard: re-check PENDING state inside the TX.
		applied, err := s.repo.HasNonVoidAdjustmentLineByRecoveryID(txCtx, recoveryID)
		if err != nil {
			return nil, fmt.Errorf("recheck applied state %s: %w", recoveryID, err)
		}
		if applied {
			return nil, respond.ErrConflict.WithMessage(
				fmt.Sprintf("รายการปรับฐานนี้ถูกบันทึกในบิลอื่นไปแล้ว (รายการที่ %d)", i+1))
		}

		amount := money.ToSatang(in.Amount)

		// Shared construction+validation (see recovery_adjustment.go). Charge,
		// refund, and waive all route through the same builder; waive is explicit
		// via in.Waive (never inferred from a zero amount).
		line, err := BuildRecoveryAdjustmentLine(billID, RecoveryResolution{
			RecoveryReadingID: recoveryID,
			Amount:            amount,
			Note:              in.AdjustmentNote,
			Waive:             in.Waive,
		}, sourceMonth, maxSort+1+i)
		if err != nil {
			return nil, respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.CreateLineItems(txCtx, []BillLineItem{line}); err != nil {
			return nil, fmt.Errorf("insert adjustment line: %w", err)
		}
		if err := recordAudit(txCtx, s.audit, billID, AuditApplyRecoveryAdjustment, actor,
			AuditApplyRecoveryAdjustmentPayload{
				Amount:            amount,
				RecoveryReadingID: recoveryID,
				ReasonCode:        string(*line.AdjustmentReasonCode),
				Note:              in.AdjustmentNote,
			}); err != nil {
			return nil, fmt.Errorf("emit recovery adjustment audit: %w", err)
		}
		created = append(created, line)
	}
	return created, nil
}
