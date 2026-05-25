package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"regexp"

	"nana/internal/billingconfig"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var billingMonthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type BillingService interface {
	List(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error)
	GetByID(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	FinalizeBill(ctx context.Context, id uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error)
	VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	CorrectBill(ctx context.Context, id uuid.UUID, req CorrectBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest, createdBy *uuid.UUID) (*BillGenerationBatch, error)
	PreflightMonthly(ctx context.Context, req MonthlyPreflightRequest) (*MonthlyPreflightResult, error)
	CommitBatch(ctx context.Context, batchID uuid.UUID) (*CommitBatchResult, error)
	BatchFinalizeAll(ctx context.Context, batchID uuid.UUID, actor *uuid.UUID) (*BatchFinalizeResult, error)
	GetBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error)
	GetBatchItems(ctx context.Context, id uuid.UUID) ([]BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error)

	// Settlement preview (non-persisting)
	PreviewSettlement(ctx context.Context, input PreviewSettlementInput) (*SettlementPreview, error)

	// Settlement draft editing
	UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest, actor *uuid.UUID) (*BillWithRelations, error)

	// Monthly draft editing
	UpdateMonthlyDraft(ctx context.Context, id uuid.UUID, req UpdateMonthlyDraftRequest, actor *uuid.UUID) (*BillWithRelations, error)

	// Move-out workflow ports (satisfies moveout.BillingCommander + moveout.BillingQuerier)
	GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error)
	RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error)
	CorrectSettlement(ctx context.Context, in moveout.CorrectSettlementInput) (*moveout.SettlementBillResult, error)
	PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode moveout.RentMode) (*moveout.SettlementPreviewResult, error)
	FinalizeSettlement(ctx context.Context, billID uuid.UUID) error
	VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error
}

type billingService struct {
	repo      BillingRepository
	audit     BillAuditRepository
	contracts ContractQuerier
	meters    MeterReadingQuerier
	configs   BillingConfigQuerier
	moveOuts  MoveOutQuerier
	tx        database.TxManager
}

var _ BillingService = (*billingService)(nil)

func NewBillingService(
	repo BillingRepository,
	audit BillAuditRepository,
	contracts ContractQuerier,
	meters MeterReadingQuerier,
	configs BillingConfigQuerier,
	moveOuts MoveOutQuerier,
	tx database.TxManager,
) BillingService {
	return &billingService{
		repo:      repo,
		audit:     audit,
		contracts: contracts,
		meters:    meters,
		configs:   configs,
		moveOuts:  moveOuts,
		tx:        tx,
	}
}

func (s *billingService) List(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error) {
	params.Normalize()
	bills, total, err := s.repo.FindAll(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	if err := s.populateIsEdited(ctx, bills); err != nil {
		return nil, 0, err
	}
	return bills, total, nil
}

// populateIsEdited issues ONE batched audit query for the entire page and
// stamps each BillWithRelations.IsEdited in place. Avoids N+1.
//
// On audit-store failure this returns the wrapped error rather than silently
// hiding edited state — a list response without is_edited would mislead the
// FE Edited badge and erode the forensic guarantee. Per the lock,
// EditedBillIDs failure propagates.
func (s *billingService) populateIsEdited(ctx context.Context, bills []BillWithRelations) error {
	if len(bills) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(bills))
	for i, b := range bills {
		ids[i] = b.ID
	}
	editedSet, err := s.audit.EditedBillIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("populate is_edited: %w", err)
	}
	for i := range bills {
		if editedSet[bills[i].ID] {
			bills[i].IsEdited = true
		}
	}
	return nil
}

func (s *billingService) GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error) {
	return s.repo.GetSummary(ctx, params)
}

func (s *billingService) GetByID(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("get bill: %w", err)
	}
	// Overdue context + policy-reference hint are computed only on detail
	// fetch — both are UI hints for the BillDrawer, never persisted, never
	// affect the bill total. See backlog_late_payment_penalty.md.
	//
	// The embedded `Bill.OverdueDays(today)` method is shadowed by the
	// `BillWithRelations.OverdueDays` int field — disambiguate via the
	// explicit Bill selector.
	now := time.Now()
	b.OverdueDays = b.Bill.OverdueDays(now)
	b.LatePenaltyReferenceAmount = s.lookupLatePenaltyReference(ctx, b, now)

	// is_edited reflects user curation history — true iff the audit log
	// has at least one edit event for this bill. Single-bill EditedBillIDs
	// lookup reuses the same repo path as the list endpoint. Per the lock,
	// audit-store failure propagates rather than silently hiding state.
	editedSet, err := s.audit.EditedBillIDs(ctx, []uuid.UUID{b.ID})
	if err != nil {
		return nil, fmt.Errorf("populate is_edited: %w", err)
	}
	b.IsEdited = editedSet[b.ID]

	// corrected_from_bill_id surfaces the reverse correction-chain link
	// for the BillDrawer's "บิลนี้สร้างจากการแก้ไขบิลเดิม" hint. The
	// forward link (SupersededByBillID) is already on the bill row;
	// the reverse direction requires an indexed lookup on the index
	// (single point query — see FindCorrectedFromBillID). Propagate
	// errors rather than silently hiding lineage; otherwise a transient
	// DB blip would let a replacement bill render as a "normal" DRAFT.
	correctedFrom, err := s.repo.FindCorrectedFromBillID(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("populate corrected_from_bill_id: %w", err)
	}
	b.CorrectedFromBillID = correctedFrom
	return b, nil
}

// lookupLatePenaltyReference resolves the apartment's active LATE_PENALTY
// rate and feeds it into ComputeLatePenaltyReference. Returns 0 for every
// "not applicable" branch (non-FINALIZED, not MONTHLY, not overdue, no
// active config, config-lookup error). Best-effort by design — this is a
// reference value, never a hard requirement.
func (s *billingService) lookupLatePenaltyReference(ctx context.Context, b *BillWithRelations, today time.Time) int64 {
	if b == nil || !b.IsOverdue(today) {
		return 0
	}
	if b.ApartmentID == uuid.Nil {
		return 0
	}
	configs, err := s.configs.FindByApartmentID(ctx, b.ApartmentID)
	if err != nil {
		return 0
	}
	var rate int64
	for _, cfg := range configs {
		if cfg.FeeType != billingconfig.FeeTypeLatePenalty {
			continue
		}
		if !cfg.IsActive {
			continue
		}
		rate = cfg.DefaultAmount
		break
	}
	return ComputeLatePenaltyReference(&b.Bill, rate, today)
}

// CreateMonthlyBill generates a DRAFT monthly bill from a single contract +
// meter reading. Same DRAFT-first semantics as the batch path
// (commitOneItem) — admin then reviews + optionally edits + explicitly
// finalizes via PATCH /:id/finalize. Pre-existing finalize-on-create
// behavior removed 2026-05-21 after FE audit confirmed no callers depended
// on immediate-FINALIZED response.
//
// Monthly = ค่าห้องเดือนถัดไป (advance) + ค่าน้ำไฟเดือนนี้ (meter)
//
// Emits exactly one CREATE_DRAFT audit row inside the same TX as the bill
// insert. No FINALIZE event here — the bill stays DRAFT until the admin
// calls FinalizeBill explicitly.
func (s *billingService) CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	meterID, err := uuid.Parse(req.MeterReadingID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("meter_reading_id ไม่ถูกต้อง")
	}

	// Validate contract + meter outside the TX to keep lock time short.
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}
	if !c.IsActive() {
		return nil, ErrContractNotActive
	}

	_, err = s.repo.FindByContractAndMonth(ctx, contractID, req.BillingMonth, BillTypeMonthly)
	if err == nil {
		return nil, ErrBillAlreadyExists
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}

	reading, err := s.meters.FindByIDSimple(ctx, meterID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMeterNotFound
		}
		return nil, fmt.Errorf("find meter: %w", err)
	}
	if !reading.IsMonthly() {
		return nil, ErrMeterTypeMismatch
	}
	if reading.RoomID != c.RoomID {
		return nil, ErrMeterRoomMismatch
	}
	if reading.BillingMonth == nil || *reading.BillingMonth != req.BillingMonth {
		return nil, ErrMeterMonthMismatch
	}

	snapshot := computeMonthlyBillSnapshot(req.BillingMonth,
		c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading)

	// Pre-generate the bill ID so the CREATE_DRAFT audit row can reference
	// it inside the same TX as the bill insert. BeforeCreate skips uuid.New()
	// when ID is already set. Note: line items in the snapshot still hold
	// uuid.Nil for their BillID — repo.Create resolves them via the parent
	// bill association at insert time.
	bill := Bill{
		ID:           uuid.New(),
		ContractID:   contractID,
		BillingMonth: req.BillingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems:    snapshot.ToLineItems(uuid.Nil),
		TotalAmount:  snapshot.TotalAmount,
	}

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, &bill); err != nil {
			return fmt.Errorf("create monthly bill: %w", err)
		}
		return s.recordAudit(txCtx, bill.ID, AuditCreateDraft, actor, AuditCreateDraftPayload{
			LineItemCount: len(bill.LineItems),
			TotalAmount:   bill.TotalAmount,
		})
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("create monthly bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, bill.ID)
}

// FinalizeBill transitions a DRAFT bill (monthly or settlement) to FINALIZED.
// Wrapped in a TX so the bill Update + FINALIZE audit row are atomic — audit
// failure rolls back the status transition.
//
// Delegates the inner steps to finalizeBillInTx so BatchFinalizeAll can reuse
// the exact same finalize semantics + audit boundary on a per-item basis.
// Domain errors (ErrNotDraft / ErrNoLineItems) bubble up unwrapped from the
// helper so they reach a single AppError conversion at this caller layer —
// the bulk caller (BatchFinalizeAll) classifies via errors.Is instead.
func (s *billingService) FinalizeBill(ctx context.Context, id uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		return s.finalizeBillInTx(txCtx, id, actor)
	}); err != nil {
		// Domain-sentinel errors → 400 AppError so the API responds with the
		// Thai validation message verbatim.
		if errors.Is(err, ErrNotDraft) || errors.Is(err, ErrNoLineItems) {
			return nil, respond.ErrBadRequest.WithMessage(err.Error())
		}
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("finalize bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, id)
}

// finalizeBillInTx is the shared inner DRAFT→FINALIZED transition used by
// single-bill FinalizeBill and bulk BatchFinalizeAll. Caller MUST provide a
// txCtx — audit + Update run on it so audit failure rolls the parent TX
// back (correctness > availability for billing forensics).
//
// Returns raw domain sentinel errors (ErrBillNotFound / ErrNotDraft /
// ErrNoLineItems) so callers can classify via errors.Is. Infra errors
// from repo or audit are wrapped with %w. Callers convert as needed:
//   - FinalizeBill  : ErrNotDraft / ErrNoLineItems → AppError 400
//   - BatchFinalizeAll: same sentinels → BatchFinalizeFailureCode
func (s *billingService) finalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error {
	b, err := s.repo.FindByID(txCtx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill: %w", err)
	}

	previousStatus := string(b.Status)
	if err := b.Finalize(); err != nil {
		// Raw sentinel — caller classifies. No AppError wrap here.
		return err
	}
	if err := s.repo.Update(txCtx, b); err != nil {
		return fmt.Errorf("finalize bill: %w", err)
	}
	return s.recordAudit(txCtx, b.ID, AuditFinalize, actor, AuditFinalizePayload{
		PreviousStatus: previousStatus,
		TotalAmount:    b.TotalAmount,
	})
}

// VoidBill marks a bill as VOID with the provided reason. Wrapped in a TX so
// the bill Update + VOID audit row are atomic.
func (s *billingService) VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrBillNotFound
			}
			return fmt.Errorf("find bill: %w", err)
		}

		previousStatus := string(b.Status)
		if err := b.Void(req.Reason); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, b); err != nil {
			return fmt.Errorf("void bill: %w", err)
		}
		return s.recordAudit(txCtx, b.ID, AuditVoid, actor, AuditVoidPayload{
			PreviousStatus: previousStatus,
			Reason:         req.Reason,
		})
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("void bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, id)
}

// CorrectBill executes the void+recreate correction flow on a FINALIZED bill.
// Atomicity invariants (single TX):
//
//   1. Row-lock the old bill first — gates the double-correction race so two
//      concurrent POST /:id/correct calls serialize on the lock.
//   2. CanCorrect() blocks PAID / DRAFT / VOID / already-superseded —
//      document-replacement invariants, single source of truth in the
//      domain layer.
//   3. Dispatch by bill_type. This endpoint is MONTHLY-only by design;
//      SETTLEMENT correction is routed via the move-out workflow
//      (POST /move-out-notices/:id/correct-settlement, Phase 2.1E-B+).
//      The switch's default rejects SETTLEMENT with the Thai sentinel.
//   4. Old bill: MarkSupersededByCorrection sets status=VOID +
//      void_reason=CORRECTION + superseded_by_bill_id=newID, all atomic.
//   5. New bill: regenerated DRAFT from contract + meter source-of-truth
//      (no override/manual carryover — fresh recalculation).
//   6. Emit SUPERSEDE on old + CREATE_FROM_CORRECTION on new in the same
//      TX. Audit failure rolls back the entire correction.
//
// Returns the new DRAFT bill with relations. Caller (handler) presents it
// for further editing via UpdateMonthlyDraft + FinalizeBill.
func (s *billingService) CorrectBill(ctx context.Context, id uuid.UUID, req CorrectBillRequest, actor *uuid.UUID) (*BillWithRelations, error) {
	var newBillID uuid.UUID

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		old, err := s.repo.LockBillForCorrection(txCtx, id)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrBillNotFound
			}
			return fmt.Errorf("lock bill for correction: %w", err)
		}

		if err := old.CanCorrect(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}

		switch old.BillType {
		case BillTypeMonthly:
			nid, err := s.correctMonthlyBillInTx(txCtx, old, req, actor)
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
) (uuid.UUID, error) {
	previousStatus := string(old.Status)

	c, err := s.contracts.FindByIDSimple(txCtx, old.ContractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
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

	snapshot := computeMonthlyBillSnapshot(old.BillingMonth,
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

	if err := s.recordAudit(txCtx, old.ID, AuditSupersede, actor, AuditSupersedePayload{
		PreviousStatus:   previousStatus,
		NewBillID:        newBill.ID,
		CorrectionReason: req.CorrectionReason,
	}); err != nil {
		return uuid.Nil, err
	}
	if err := s.recordAudit(txCtx, newBill.ID, AuditCreateFromCorrection, actor, AuditCreateFromCorrectionPayload{
		SupersededBillID: old.ID,
		CorrectionReason: req.CorrectionReason,
	}); err != nil {
		return uuid.Nil, err
	}
	return newBill.ID, nil
}

func (s *billingService) MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}

	if err := b.MarkPaid(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Update(ctx, b); err != nil {
		return nil, fmt.Errorf("mark paid: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, b.ID)
}
