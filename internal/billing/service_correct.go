package billing

import (
	"context"
	"fmt"

	"nana/internal/apartment"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// CorrectBill executes the void+recreate correction flow on a FINALIZED bill.
// Atomicity invariants (single TX):
//
//  1. Row-lock the old bill first — gates the double-correction race so two
//     concurrent POST /:id/correct calls serialize on the lock.
//  2. CanCorrect() blocks PAID / DRAFT / VOID / already-superseded —
//     document-replacement invariants, single source of truth in the
//     domain layer.
//  3. Dispatch by bill_type. This endpoint is MONTHLY-only by design;
//     SETTLEMENT correction is routed via the move-out workflow
//     (POST /move-out-notices/:id/correct-settlement, Phase 2.1E-B+).
//     The switch's default rejects SETTLEMENT with the Thai sentinel.
//  4. Old bill: MarkSupersededByCorrection sets status=VOID +
//     void_reason=CORRECTION + superseded_by_bill_id=newID, all atomic.
//  5. New bill: regenerated DRAFT from contract + meter source-of-truth
//     (no override/manual carryover — fresh recalculation).
//  6. Emit SUPERSEDE on old + CREATE_FROM_CORRECTION on new in the same
//     TX. Audit failure rolls back the entire correction.
//
// Returns the new DRAFT bill with relations. Caller (handler) presents it
// for further editing via UpdateMonthlyDraft + FinalizeBill.
func (s *billingService) CorrectBill(ctx context.Context, id uuid.UUID, req CorrectBillRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	var newBillID uuid.UUID

	// Pre-resolve payment destination outside the correction TX (read-only pre-fetch).
	// Routing queries on payment_destination_rules/apartment_bank_accounts must not
	// extend the correction row-lock window. If routing is configured but the resolver
	// returns an unexpected error, fail fast — correction must not silently produce a
	// null-destination document when rules are configured.
	paymentDest, err := s.resolvePaymentDestForCorrection(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("resolve payment destination for correction: %w", err)
	}

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		old, err := s.repo.LockBillForCorrection(txCtx, id)
		if err != nil {
			if database.IsNotFound(err) {
				return ErrBillNotFound
			}
			return fmt.Errorf("lock bill for correction: %w", err)
		}

		if err := old.CanCorrect(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}

		switch old.BillType {
		case BillTypeMonthly:
			nid, err := s.correctMonthlyBillInTx(txCtx, old, req, actor, paymentDest)
			if err != nil {
				return err
			}
			newBillID = nid
			return nil
		default:
			// SETTLEMENT correction is routed via the move-out workflow
			// (POST /move-out-notices/:id/correct-settlement, Phase 2.1E-B+),
			// not this billing /bills/:id/correct endpoint. The billing
			// dispatcher is MONTHLY-only by design — domain CanCorrect (since
			// Phase 2.1E-A) accepts SETTLEMENT, so the endpoint routing IS
			// the gate here. See project_settlement_correction_design_lock.
			return respond.ErrBadRequest.WithMessage(ErrSettlementCorrectionNotSupported.Error())
		}
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("correct bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, newBillID)
}

// correctMonthlyBillInTx is the MONTHLY-specific arm of CorrectBill. Caller
// must hold a row-lock on `old` and have validated CanCorrect(). Returns the
// new DRAFT bill ID for the outer fetch-with-relations.
//
// Regenerate (not clone): old line items + overrides are intentionally NOT
// copied. The new DRAFT is rebuilt from the contract + current monthly meter
// reading — fresh recalculation per the architecture lock. Admin re-applies
// any overrides via UpdateMonthlyDraft on the new DRAFT if needed.
//
// Contract IsActive guard is intentionally absent — the original bill may
// be on an ENDED contract (e.g. post-move-out correction window). The bill
// already exists, so correction must work regardless of contract status.
func (s *billingService) correctMonthlyBillInTx(
	txCtx context.Context,
	old *Bill,
	req CorrectBillRequest,
	actor *uuid.UUID,
	paymentDest *apartment.PaymentDestinationInfo,
) (uuid.UUID, error) {
	previousStatus := string(old.Status)

	c, err := s.contracts.FindByIDSimple(txCtx, old.ContractID)
	if err != nil {
		if database.IsNotFound(err) {
			return uuid.Nil, ErrContractNotFound
		}
		return uuid.Nil, fmt.Errorf("find contract: %w", err)
	}

	// Look up the monthly meter reading for the same (room, billing_month)
	// the old bill targeted. If admin re-recorded the meter, regeneration
	// picks up the corrected values. If no meter exists for that month
	// today, correction can't proceed — admin must record meter first.
	readings, err := s.meters.FindMonthlyByRoomsAndMonth(txCtx, []uuid.UUID{c.RoomID}, old.BillingMonth)
	if err != nil {
		return uuid.Nil, fmt.Errorf("find meter for correction: %w", err)
	}
	reading, ok := readings[c.RoomID]
	if !ok || reading == nil {
		return uuid.Nil, ErrMeterNotFound
	}

	snapshot := ComputeMonthlyBillSnapshot(old.BillingMonth,
		c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading)

	// Pre-generate the new bill's ID so both the SUPERSEDE link (set on
	// old NOW) and the CREATE_FROM_CORRECTION audit can reference it.
	// BatchID stays nil — corrected bill is a manual artifact, not part of
	// the original batch run (batch_item keeps pointing at the old bill;
	// FE walks superseded_by chain to find latest).
	newBill := Bill{
		ID:           uuid.New(),
		ContractID:   old.ContractID,
		BillingMonth: old.BillingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems:    snapshot.ToLineItems(uuid.Nil),
		TotalAmount:  snapshot.TotalAmount,
	}
	applyPaymentSnapshot(&newBill, paymentDest)

	// Persist order MATTERS: void+supersede old FIRST, then create new.
	// The partial UNIQUE INDEX idx_bills_unique_monthly allows only one
	// non-VOID monthly bill per (contract_id, billing_month), so creating
	// the new DRAFT while the old is still FINALIZED would 500 on a
	// constraint violation.
	//
	// The supersede FK on old → newBill.ID is set BEFORE newBill exists in
	// the DB; migration 00032 makes that FK DEFERRABLE INITIALLY DEFERRED,
	// so the check fires at COMMIT (when newBill exists) rather than at
	// statement time.
	if err := old.MarkSupersededByCorrection(newBill.ID); err != nil {
		// Domain re-checks CanCorrect — under correct caller usage this
		// can't fire, but stay defensive in case lock semantics change.
		return uuid.Nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(txCtx, old); err != nil {
		return uuid.Nil, fmt.Errorf("supersede old bill: %w", err)
	}
	if err := s.repo.Create(txCtx, &newBill); err != nil {
		return uuid.Nil, fmt.Errorf("create corrected bill: %w", err)
	}

	if err := recordAudit(txCtx, s.audit, old.ID, AuditSupersede, actor, AuditSupersedePayload{
		PreviousStatus:   previousStatus,
		NewBillID:        newBill.ID,
		CorrectionReason: req.CorrectionReason,
	}); err != nil {
		return uuid.Nil, err
	}
	if err := recordAudit(txCtx, s.audit, newBill.ID, AuditCreateFromCorrection, actor, AuditCreateFromCorrectionPayload{
		SupersededBillID: old.ID,
		CorrectionReason: req.CorrectionReason,
	}); err != nil {
		return uuid.Nil, err
	}
	return newBill.ID, nil
}

// resolvePaymentDestForCorrection pre-fetches the payment destination for a bill
// outside any transaction. Returns (nil, nil) when no rules are configured or when
// bill/contract/room lookups fail (the TX will surface those as proper errors).
// Returns (nil, err) only when the routing service itself fails — callers must
// propagate that error rather than silently proceeding with a null destination.
func (s *billingService) resolvePaymentDestForCorrection(ctx context.Context, billID uuid.UUID) (*apartment.PaymentDestinationInfo, error) {
	if s.paymentRouting == nil {
		return nil, nil
	}
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		return nil, nil // LockBillForCorrection inside TX will surface the 404
	}
	c, err := s.contracts.FindByIDSimple(ctx, b.ContractID)
	if err != nil {
		return nil, nil // TX will surface this
	}
	aptID, roomNum, err := s.repo.FindRoomApartmentInfo(ctx, c.RoomID)
	if err != nil {
		return nil, nil // TX will surface this
	}
	return s.paymentRouting.ResolveDestination(ctx, aptID, roomNum)
}
