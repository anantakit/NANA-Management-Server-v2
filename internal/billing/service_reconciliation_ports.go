package billing

import (
	"context"
	"errors"
	"fmt"

	"nana/internal/billingreconciliation"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Cross-feature adapter methods that let billing.BillingService satisfy the
// reconciliation workspace's BillsQuerier + BillsCommander ports
// (defined in nana/internal/billingreconciliation/port.go).
//
// Same pattern as service_moveout_ports.go: billing knows the consumer's
// types directly so the wiring stays a single struct (no separate adapter
// object). Cycle direction: billing imports billingreconciliation;
// billingreconciliation does NOT import billing.

// FindExistingBillsByContractsAndMonth implements
// billingreconciliation.BillsQuerier — pure read, no transaction. Delegates
// to the underlying repository and maps the billing.Bill rows to the
// reconciliation-flat projection at the boundary (display-read pattern).
func (s *billingService) FindExistingBillsByContractsAndMonth(
	ctx context.Context,
	contractIDs []uuid.UUID,
	billingMonth string,
) (map[uuid.UUID]*billingreconciliation.BillSnapshot, error) {
	if len(contractIDs) == 0 {
		return map[uuid.UUID]*billingreconciliation.BillSnapshot{}, nil
	}
	bills, err := s.repo.FindExistingByContractsAndMonth(ctx, contractIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find existing bills for reconciliation: %w", err)
	}
	out := make(map[uuid.UUID]*billingreconciliation.BillSnapshot, len(bills))
	for contractID, b := range bills {
		if b == nil {
			continue
		}
		out[contractID] = &billingreconciliation.BillSnapshot{
			BillID:            b.ID,
			Status:            string(b.Status),
			TotalAmountSatang: b.TotalAmount,
		}
	}
	return out, nil
}

// CreateMonthlyBillForReconciliation implements
// billingreconciliation.BillsCommander. Per-call TX, single-bill semantics —
// the reconciliation service fans out N times (one call per room_ids[]
// entry) per project_reconciliation_phase1d_scenario1_locks.md Q1 Contract A.
//
// Adapter responsibilities:
//   - Resolve (contract_id, billing_month) → meter_reading_id by looking up
//     the contract's room and the MONTHLY reading for that (room, month).
//     billing.Service.CreateMonthlyBill requires an explicit meter id; the
//     reconciliation workspace's commit only carries (contract, month).
//   - Delegate to CreateMonthlyBill so the underlying DRAFT-first behavior,
//     duplicate guard, validation pipeline, and CREATE_DRAFT audit row stay
//     identical to single-bill and batch paths.
//   - Errors propagate verbatim — sentinel mapping to SKIPPED reason codes
//     (LOST_READY_BETWEEN_PREVIEW_AND_COMMIT, ALREADY_BILLED_BY_OTHER) is
//     the reconciliation service's job, not the adapter's.
//
// "No meter reading" surfaces as a 409 AppError so callers using
// errors.Is + respond.Is can both reach a stable signal.
func (s *billingService) CreateMonthlyBillForReconciliation(
	ctx context.Context,
	req billingreconciliation.CreateMonthlyBillForReconciliationRequest,
	actor *uuid.UUID,
) (*billingreconciliation.CreatedBill, error) {
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	c, err := s.contracts.FindByIDSimple(ctx, req.ContractID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract for reconciliation bill: %w", err)
	}
	meters, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, []uuid.UUID{c.RoomID}, req.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("find meter for reconciliation bill: %w", err)
	}
	reading := meters[c.RoomID]
	if reading == nil {
		return nil, ErrMeterNotFound
	}

	bill, err := s.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID:     req.ContractID.String(),
		BillingMonth:   req.BillingMonth,
		MeterReadingID: reading.ID.String(),
	}, actor)
	if err != nil {
		return nil, err
	}
	return &billingreconciliation.CreatedBill{ID: bill.ID}, nil
}
