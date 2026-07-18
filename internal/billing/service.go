package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/shared/billingmonth"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type BillingService interface {
	List(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error)
	GetByID(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	FinalizeBill(ctx context.Context, id uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error)
	VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	CorrectBill(ctx context.Context, id uuid.UUID, req CorrectBillRequest, actor *uuid.UUID) (*BillWithRelations, error)
	// RegenerateDraft (Monthly Draft Refresh, Epic A) atomically voids a stale
	// MONTHLY DRAFT and rebuilds it from source-of-truth via the shared monthly
	// generation path. SETTLEMENT is rejected at the guard.
	RegenerateDraft(ctx context.Context, id uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error)
	MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)

	// Monthly draft editing
	UpdateMonthlyDraft(ctx context.Context, id uuid.UUID, req UpdateMonthlyDraftRequest, actor *uuid.UUID) (*BillWithRelations, error)
}

// NOTE: history of method migrations off this interface:
//   - W2 (28381ff, 2026-06-19): 9 batch-related methods moved to
//     internal/billing/monthly (BatchCreateMonthlyBills, PreflightMonthly,
//     CommitBatch, BatchFinalizeAll, FinalizeAllByMonth, RePlanBatchItem,
//     GetBatchByID, GetBatchItems, ListBatches).
//   - W4 commit 3 (this commit, 2026-06-19): 9 settlement methods moved to
//     internal/billing/settlement (PreviewSettlement, CreateSettlementBill,
//     UpdateSettlementDraft, FinalizeSettlement, RegenerateSettlement,
//     GenerateSettlement, CorrectSettlement, VoidSettlement,
//     PreviewSettlementForNotice). moveout.BillingCommander and
//     moveout.BillingQuerier are now satisfied by *settlement.Service —
//     billingService no longer satisfies them.
// See project_billing_extraction_plan_locked.md.

type billingService struct {
	repo           BillingRepository
	audit          BillAuditRepository
	contracts      ContractQuerier
	meters         MeterReadingQuerier
	configs        BillingConfigQuerier
	paymentRouting PaymentRoutingQuerier
	tx             database.TxManager
}

var _ BillingService = (*billingService)(nil)

func NewBillingService(
	repo BillingRepository,
	audit BillAuditRepository,
	contracts ContractQuerier,
	meters MeterReadingQuerier,
	configs BillingConfigQuerier,
	paymentRouting PaymentRoutingQuerier,
	tx database.TxManager,
) BillingService {
	return &billingService{
		repo:           repo,
		audit:          audit,
		contracts:      contracts,
		meters:         meters,
		configs:        configs,
		paymentRouting: paymentRouting,
		tx:             tx,
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
		if database.IsNotFound(err) {
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

	// correction_reason surfaces the admin-typed reason on the VOID(CORRECTION)
	// drawer so "why was this voided" is readable without DB access. Single
	// indexed lookup on bill_audit_log(bill_id, created_at DESC); audit log
	// stays single source of truth (no denormalization to bills table). Empty
	// reason when no SUPERSEDE event exists — graceful for pre-correction
	// VOID bills and any non-CORRECTION void path.
	if b.IsSupersededByCorrection() {
		reason, err := s.audit.FindLatestSupersedeReason(ctx, b.ID)
		if err != nil {
			return nil, fmt.Errorf("populate correction_reason: %w", err)
		}
		b.CorrectionReason = reason
	}
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
// finalizes via PATCH /:id/finalize.
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
	if !billingmonth.Valid(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	meterID, err := uuid.Parse(req.MeterReadingID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("meter_reading_id ไม่ถูกต้อง")
	}

	// Validate contract + meter outside the TX to keep lock time short.
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if database.IsNotFound(err) {
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
	if !database.IsNotFound(err) {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}

	reading, err := s.meters.FindByIDSimple(ctx, meterID)
	if err != nil {
		if database.IsNotFound(err) {
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

	bill, err := s.buildMonthlyDraftBill(ctx, c, reading, req.BillingMonth)
	if err != nil {
		return nil, err
	}

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, bill); err != nil {
			return fmt.Errorf("create monthly bill: %w", err)
		}
		return recordAudit(txCtx, s.audit, bill.ID, AuditCreateDraft, actor, AuditCreateDraftPayload{
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

// buildMonthlyDraftBill assembles a fresh DRAFT monthly Bill from source-of-truth
// — contract + meter reading + recovery reconciliation (S0-gated forward credit)
// + payment routing. It is READ-ONLY (no writes) and is the SINGLE monthly
// generation path: CreateMonthlyBill (and therefore the reconciliation-workspace
// generate + batch commit, which delegate to it) AND RegenerateDraft both call
// it, so a regenerated draft is byte-identical to a freshly generated one. The
// caller persists inside its own TX (RegenerateDraft additionally voids the old
// draft in that same TX for atomicity). The bill ID is pre-generated so the
// caller's CREATE_DRAFT audit can reference it in the same TX as the insert.
func (s *billingService) buildMonthlyDraftBill(ctx context.Context, c *contract.Contract, reading *meterreading.MeterReading, billingMonth string) (*Bill, error) {
	// Utility-scoped recovery overlay (owner lock 2026-07-18). When `reading` is a
	// recovery anchor, a real consumption row may coexist for the same room+month
	// (the reconciliation-adapter path re-fetches the recovery-preferred winner by
	// ID, so the projection MUST happen HERE, post-refetch, not at selection). The
	// unaffected utility bills its real usage from that consumption row; the
	// affected utility keeps the anchor's 0-usage + refund. No-op for a plain
	// consumption reading. The projected reading keeps the recovery's identity, so
	// the resolver + adjustment FK + lineage below are unchanged.
	consumption, err := s.meters.FindConsumptionMonthlyByRoomAndMonth(ctx, c.RoomID, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find coexisting consumption reading: %w", err)
	}
	reading = ProjectRecoveryUsageOverlay(reading, consumption)

	recon, err := ResolveRecoveryReconciliation(ctx, reading, c.ID, s.repo)
	if err != nil {
		return nil, fmt.Errorf("resolve recovery reconciliation: %w", err)
	}
	snapshot := ComputeMonthlyBillSnapshot(billingMonth,
		c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading, recon)

	// Payment destination is a read-only lookup; null when no rules configured.
	aptID, roomNum, _ := s.repo.FindRoomApartmentInfo(ctx, c.RoomID)
	paymentDest := s.tryResolvePaymentDestination(ctx, aptID, roomNum)

	bill := &Bill{
		ID:           uuid.New(),
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems:    snapshot.ToLineItems(uuid.Nil),
		TotalAmount:  snapshot.TotalAmount,
	}
	ApplyPaymentSnapshot(bill, paymentDest)
	return bill, nil
}

// FinalizeBill transitions a DRAFT bill (monthly or settlement) to FINALIZED.
// Wrapped in a TX so the bill Update + FINALIZE audit row are atomic — audit
// failure rolls back the status transition.
//
// Delegates the inner steps to the package-level finalizeBillInTx so
// BatchFinalizeAll, FinalizeAllByMonth, and the cross-feature MonthlyAdapter
// reuse the exact same finalize semantics + audit boundary on a per-item
// basis. Domain errors (ErrNotDraft / ErrNoLineItems) bubble up unwrapped
// from the helper so they reach a single AppError conversion at this caller
// layer — the bulk callers classify via errors.Is instead.
func (s *billingService) FinalizeBill(ctx context.Context, id uuid.UUID, actor *uuid.UUID) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		return finalizeBillInTx(txCtx, s.repo, s.audit, id, actor)
	}); err != nil {
		// Domain-sentinel errors → 400 AppError so the API responds with the
		// Thai validation message verbatim.
		if errors.Is(err, ErrNotDraft) || errors.Is(err, ErrNoLineItems) ||
			errors.Is(err, ErrBillStaleAfterRecovery) {
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
// single-bill FinalizeBill, bulk BatchFinalizeAll, FinalizeAllByMonth, and
// the cross-feature MonthlyAdapter. Caller MUST provide a txCtx — audit +
// Update run on it so audit failure rolls the parent TX back (correctness >
// availability for billing forensics).
//
// Returns raw domain sentinel errors (ErrBillNotFound / ErrNotDraft /
// ErrNoLineItems) so callers can classify via errors.Is. Infra errors
// from repo or audit are wrapped with %w. Callers convert as needed:
//   - FinalizeBill   : ErrNotDraft / ErrNoLineItems → AppError 400
//   - BatchFinalizeAll / FinalizeAllByMonth: same sentinels → BatchFinalizeFailureCode
//
// Package-level function (not a method on billingService) so the cross-feature
// MonthlyAdapter can share the exact same finalize path without duplicating
// the load → domain method → persist → audit orchestration. Extracted from
// billingService.finalizeBillInTx on 2026-06-19 during the monthly extraction
// scaffold.
func finalizeBillInTx(txCtx context.Context, repo BillingRepository, audit BillAuditRepository, id uuid.UUID, actor *uuid.UUID) error {
	b, err := repo.FindByID(txCtx, id)
	if err != nil {
		if database.IsNotFound(err) {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill: %w", err)
	}

	// Q1.6 freshness gate: block finalizing a bill that predates a meter
	// correction. If the contract has an over-record recovery whose refund is not
	// yet reflected on any live bill, this bill is stale — the operator must
	// regenerate (VOID + GENERATE) so the refund is auto-emitted. Raw sentinel —
	// FinalizeBill maps it to 400, batch classifies it as a business-rule skip.
	stale, err := repo.HasUnreflectedOverRecordByContractID(txCtx, b.ContractID)
	if err != nil {
		return fmt.Errorf("check stale bill after recovery: %w", err)
	}
	if stale {
		return ErrBillStaleAfterRecovery
	}

	previousStatus := string(b.Status)
	if err := b.Finalize(); err != nil {
		// Raw sentinel — caller classifies. No AppError wrap here.
		return err
	}
	if err := repo.Update(txCtx, b); err != nil {
		return fmt.Errorf("finalize bill: %w", err)
	}
	return recordAudit(txCtx, audit, b.ID, AuditFinalize, actor, AuditFinalizePayload{
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
			if database.IsNotFound(err) {
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
		return recordAudit(txCtx, s.audit, b.ID, AuditVoid, actor, AuditVoidPayload{
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

func (s *billingService) MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if database.IsNotFound(err) {
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
