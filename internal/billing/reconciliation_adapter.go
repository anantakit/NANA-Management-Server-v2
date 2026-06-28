package billing

import (
	"context"
	"errors"
	"fmt"

	"nana/internal/billingreconciliation"
	"nana/internal/shared/billingmonth"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// ReconciliationAdapter satisfies billingreconciliation.BillsQuerier and
// billingreconciliation.BillsCommander with a narrow dependency surface:
// the bill repository for read projection, the contract + meter queriers
// for per-row resolution, and the underlying BillingService only for the
// CreateMonthlyBill orchestration (TX, validation pipeline, audit emit).
//
// Mirrors PaymentAdapter: billing owns the consumer-defined port at the
// implementation layer, so reconciliation never imports billing. The two
// methods previously lived on billingService directly; that pattern
// dragged a downstream package (billingreconciliation) into billing's
// main interface and inverted the natural adapter direction. The adapter
// shape keeps BillingService's surface scoped to billing's own concerns.
type ReconciliationAdapter struct {
	repo      BillingRepository
	contracts ContractQuerier
	meters    MeterReadingQuerier
	svc       BillingService
}

func NewReconciliationAdapter(
	repo BillingRepository,
	contracts ContractQuerier,
	meters MeterReadingQuerier,
	svc BillingService,
) *ReconciliationAdapter {
	return &ReconciliationAdapter{repo: repo, contracts: contracts, meters: meters, svc: svc}
}

// FindExistingBillsByContractsAndMonth implements
// billingreconciliation.BillsQuerier — pure read, no transaction.
// Maps the billing.Bill rows to the reconciliation-flat projection at the
// boundary (display-read pattern).
func (a *ReconciliationAdapter) FindExistingBillsByContractsAndMonth(
	ctx context.Context,
	contractIDs []uuid.UUID,
	billingMonth string,
) (map[uuid.UUID]*billingreconciliation.BillSnapshot, error) {
	if len(contractIDs) == 0 {
		return map[uuid.UUID]*billingreconciliation.BillSnapshot{}, nil
	}
	bills, err := a.repo.FindExistingByContractsAndMonth(ctx, contractIDs, billingMonth)
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

// CountPendingBaselineCorrectionsByRoomIDs implements
// billingreconciliation.BillsQuerier — pure read, no transaction. Delegates
// to the bill repository which owns the inverse-FK probe (same definition
// as the per-bill HasNonVoidAdjustmentLineByRecoveryID, batched per room).
func (a *ReconciliationAdapter) CountPendingBaselineCorrectionsByRoomIDs(
	ctx context.Context,
	roomIDs []uuid.UUID,
) (map[uuid.UUID]int, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	out, err := a.repo.CountPendingBaselineCorrectionsByRoomIDs(ctx, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("count pending baseline corrections for reconciliation: %w", err)
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
//   - Classify billing-side sentinel errors into the port's two SKIPPED
//     outcomes (ErrAlreadyBilled / ErrLostReady). The reconciliation
//     service cannot do this classification itself because it cannot
//     import billing (cycle would re-form). System errors propagate
//     unchanged for the service to surface as FAILED rows.
func (a *ReconciliationAdapter) CreateMonthlyBillForReconciliation(
	ctx context.Context,
	req billingreconciliation.CreateMonthlyBillForReconciliationRequest,
	actor *uuid.UUID,
) (*billingreconciliation.CreatedBill, error) {
	if !billingmonth.Valid(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	c, err := a.contracts.FindByIDSimple(ctx, req.ContractID)
	if err != nil {
		if database.IsNotFound(err) {
			// Contract disappeared between Reconcile and commit (soft-delete /
			// race) — a LOST_READY outcome for the per-row result.
			return nil, billingreconciliation.ErrLostReady
		}
		return nil, fmt.Errorf("find contract for reconciliation bill: %w", err)
	}
	meters, err := a.meters.FindMonthlyByRoomsAndMonth(ctx, []uuid.UUID{c.RoomID}, req.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("find meter for reconciliation bill: %w", err)
	}
	reading := meters[c.RoomID]
	if reading == nil {
		// Meter missing at commit — operator preview said READY, but the
		// data behind it has since shifted (meter row deleted, anomaly
		// re-correction, etc.).
		return nil, billingreconciliation.ErrLostReady
	}

	bill, err := a.svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID:     req.ContractID.String(),
		BillingMonth:   req.BillingMonth,
		MeterReadingID: reading.ID.String(),
	}, actor)
	if err != nil {
		return nil, classifyCreateMonthlyBillError(err)
	}
	return &billingreconciliation.CreatedBill{ID: bill.ID}, nil
}

// classifyCreateMonthlyBillError translates billing-side sentinel errors into
// the BillsCommander port's two SKIPPED outcomes. System errors and
// unrecognized AppErrors propagate as-is so the reconciliation service can
// surface them as FAILED rows.
//
// Categories:
//   - ErrBillAlreadyExists                       → ErrAlreadyBilled
//   - Postgres unique-violation on idx_bills_unique_monthly
//     (DRAFT-vs-DRAFT collision — CreateMonthlyBill's application-level
//     duplicate check ignores DRAFT bills, so the partial unique index is
//     the only guard for concurrent DRAFT creates)
//     → ErrAlreadyBilled
//   - state-change invariant violations
//     (meter type/room/month mismatch, contract not active, contract not
//     found, meter not found)                    → ErrLostReady
//   - anything else                              → propagated verbatim
func classifyCreateMonthlyBillError(err error) error {
	switch {
	case errors.Is(err, ErrBillAlreadyExists):
		return billingreconciliation.ErrAlreadyBilled
	case errors.Is(err, ErrContractNotActive),
		errors.Is(err, ErrContractNotFound),
		errors.Is(err, ErrMeterNotFound),
		errors.Is(err, ErrMeterTypeMismatch),
		errors.Is(err, ErrMeterRoomMismatch),
		errors.Is(err, ErrMeterMonthMismatch):
		return billingreconciliation.ErrLostReady
	case IsDuplicateBillError(err):
		return billingreconciliation.ErrAlreadyBilled
	}
	return err
}

// Compile-time interface checks — guarantees the adapter stays wired to
// the reconciliation ports at the type level, so a rename on either side
// surfaces immediately at build instead of at DI time.
var (
	_ billingreconciliation.BillsQuerier   = (*ReconciliationAdapter)(nil)
	_ billingreconciliation.BillsCommander = (*ReconciliationAdapter)(nil)
)
