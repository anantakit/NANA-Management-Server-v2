package billing

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"nana/internal/shared/database"
	"nana/internal/shared/respond"
)

// ErrRegenerateNotMonthlyDraft rejects regenerate on anything that is not a
// MONTHLY DRAFT. SETTLEMENT drafts are explicitly OUT OF SCOPE for Monthly Draft
// Refresh (Epic A) — their recovery reconciliation is a different workflow (S4),
// see backlog_settlement_overrecord_refund_unreachable.
var ErrRegenerateNotMonthlyDraft = respond.ErrBadRequest.WithMessage("อัปเดตร่างได้เฉพาะบิลรายเดือนที่เป็นร่าง")

// voidReasonRegenerated marks a draft voided by a regenerate (Monthly Draft
// Refresh), distinct from voidReasonCorrection / voidReasonAbsorbed.
const voidReasonRegenerated = "REGENERATED"

// RegenerateDraft is the Monthly Draft Refresh action (Epic A, owner-locked
// 2026-07-08): a stale MONTHLY DRAFT (one that predates a Reading Recovery and is
// blocked by the freshness gate) is atomically VOIDed and replaced by a fresh
// DRAFT generated from source-of-truth — now including the S0-gated recovery
// forward credit. It reuses buildMonthlyDraftBill (the SINGLE monthly generation
// path shared with CreateMonthlyBill and thus the reconciliation/batch generate),
// so the refreshed draft is byte-identical to a freshly generated one — no forked
// generation logic. Void-old + create-new run in ONE TX (no
// voided-but-not-regenerated half-state).
//
// SETTLEMENT is rejected at the guard: it regenerates from deposit + charges +
// rent + exit reading, not the monthly meter, and does not consume monthly
// recoveries — a shared shortcut would silently fail it (see the Step-3 workflow
// audit). That is Epic B / S4.
func (s *billingService) RegenerateDraft(ctx context.Context, billID uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error) {
	// Cheap pre-check outside the TX for a clean early error; the authoritative
	// re-validation happens on the locked row inside the TX.
	pre, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}
	if pre.BillType != BillTypeMonthly || !pre.IsDraft() {
		return nil, ErrRegenerateNotMonthlyDraft
	}

	var newBillID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		// Row-lock the bill so a concurrent finalize/void/regenerate can't race
		// (LockBillForCorrection is a generic SELECT FOR UPDATE on the bill row).
		old, err := s.repo.LockBillForCorrection(txCtx, billID)
		if err != nil {
			if database.IsNotFound(err) {
				return ErrBillNotFound
			}
			return fmt.Errorf("lock bill: %w", err)
		}
		if old.BillType != BillTypeMonthly || !old.IsDraft() {
			return ErrRegenerateNotMonthlyDraft
		}

		c, err := s.contracts.FindByIDSimple(txCtx, old.ContractID)
		if err != nil {
			if database.IsNotFound(err) {
				return ErrContractNotFound
			}
			return fmt.Errorf("find contract: %w", err)
		}
		// Re-read the month's meter — the recovery row is PREFERRED, which is the
		// whole point: pick up the correction the stale draft predates.
		readings, err := s.meters.FindMonthlyByRoomsAndMonth(txCtx, []uuid.UUID{c.RoomID}, old.BillingMonth)
		if err != nil {
			return fmt.Errorf("find meter for regenerate: %w", err)
		}
		reading, ok := readings[c.RoomID]
		if !ok || reading == nil {
			return ErrMeterNotFound
		}

		newBill, err := s.buildMonthlyDraftBill(txCtx, c, reading, old.BillingMonth)
		if err != nil {
			return err
		}

		// Void old FIRST: the partial unique index allows only one non-VOID
		// monthly bill per (contract, month), so the new DRAFT cannot coexist with
		// the old one.
		if err := old.Void(voidReasonRegenerated); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, old); err != nil {
			return fmt.Errorf("void stale draft: %w", err)
		}
		if err := s.repo.Create(txCtx, newBill); err != nil {
			return fmt.Errorf("create regenerated draft: %w", err)
		}

		if err := recordAudit(txCtx, s.audit, old.ID, AuditVoid, actor, AuditVoidPayload{
			PreviousStatus: string(BillStatusDraft),
			Reason:         voidReasonRegenerated,
		}); err != nil {
			return err
		}
		if err := recordAudit(txCtx, s.audit, newBill.ID, AuditCreateDraft, actor, AuditCreateDraftPayload{
			LineItemCount: len(newBill.LineItems),
			TotalAmount:   newBill.TotalAmount,
		}); err != nil {
			return err
		}
		newBillID = newBill.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("regenerate draft: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, newBillID)
}
