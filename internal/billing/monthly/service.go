package monthly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/billing"
	"nana/internal/shared/billingmonth"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Service is the monthly billing workflow's public surface — the lifecycle
// of a monthly billing cycle from preflight through batch generation,
// commit, finalize, and replan.
//
// SCOPE NOTE: this is the MONTHLY billing workflow only. It is not a
// general batch engine. Future batch-shaped workflows (delivery, collection,
// LINE sending, etc.) MUST live in their own workflow packages — never
// fold them in here just because they happen to be batch-shaped.
type Service interface {
	// Batch generation
	PreflightMonthly(ctx context.Context, req MonthlyPreflightRequest) (*MonthlyPreflightResult, error)
	BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest, createdBy *uuid.UUID) (*billing.BillGenerationBatch, error)

	// Batch commit + finalize
	CommitBatch(ctx context.Context, batchID uuid.UUID) (*billing.CommitBatchResult, error)
	BatchFinalizeAll(ctx context.Context, batchID uuid.UUID, actor *uuid.UUID) (*BatchFinalizeResult, error)
	FinalizeAllByMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string, actor *uuid.UUID) (*BatchFinalizeResult, error)

	// Batch read
	GetBatchByID(ctx context.Context, id uuid.UUID) (*billing.BillGenerationBatch, error)
	GetBatchItems(ctx context.Context, id uuid.UUID) ([]billing.BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error)

	// Per-item replan (implemented in replan_service.go)
	RePlanBatchItem(ctx context.Context, batchID, itemID uuid.UUID) (*billing.BatchItemWithTenant, error)
}

// Ports BillStore, AuditStore, MeterReadingSource, MoveOutSource are declared
// in port.go and satisfied by external providers (billing.MonthlyAdapter,
// meterreading + moveout repos). batches is the monthly-owned BatchRepository
// (see repository.go) — local impl, no port indirection because the
// implementation lives in this package.

type service struct {
	bills    BillStore
	audit    AuditStore
	batches  BatchRepository
	meters   MeterReadingSource
	moveOuts MoveOutSource
	tx       database.TxManager
}

var _ Service = (*service)(nil)

func NewService(
	bills BillStore,
	audit AuditStore,
	batches BatchRepository,
	meters MeterReadingSource,
	moveOuts MoveOutSource,
	tx database.TxManager,
) Service {
	return &service{
		bills:    bills,
		audit:    audit,
		batches:  batches,
		meters:   meters,
		moveOuts: moveOuts,
		tx:       tx,
	}
}

// --- PreflightMonthly ---

// PreflightMonthly returns readiness counts without persisting anything.
// Uses the same loadBatchInputs + classifyContractForBatch pair as the real
// batch flow, so counts always match what a subsequent batch run would tally.
func (s *service) PreflightMonthly(ctx context.Context, req MonthlyPreflightRequest) (*MonthlyPreflightResult, error) {
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง")
	}
	if !billingmonth.Valid(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	in, err := s.loadBatchInputs(ctx, apartmentID, req.BillingMonth)
	if err != nil {
		return nil, err
	}

	result := &MonthlyPreflightResult{TotalRooms: len(in.contracts)}
	for _, c := range in.contracts {
		cls := classifyContractForBatch(c, in.startOfMonth, in.endOfMonth, in.pendingMoveOuts, in.meterMap, in.existingMap)
		switch {
		case cls.ResultType == billing.ResultCreated:
			result.ReadyCount++
		case cls.ReasonCode == billing.ReasonAlreadyExists:
			result.AlreadyExistsCount++
		case cls.ReasonCode == billing.ReasonMissingMeterReading:
			result.MissingMeterCount++
		case cls.ReasonCode == billing.ReasonMoveOutPending:
			result.MoveOutPendingCount++
		case cls.ReasonCode == billing.ReasonNotBillable:
			result.NotBillableCount++
		}
	}
	return result, nil
}

// --- BatchCreateMonthlyBills ---

// BatchCreateMonthlyBills generates monthly bills for all eligible active contracts
// in an apartment for a given billing month. Persists a BillGenerationBatch run-log
// with per-contract items so the result can be re-opened later by batch_id.
func (s *service) BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest, createdBy *uuid.UUID) (*billing.BillGenerationBatch, error) {
	// --- 1. Validate input ---
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง")
	}
	if !billingmonth.Valid(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	// Pre-generate batch ID so bills can reference it before the batch row is inserted.
	batchID := uuid.New()
	batch := &billing.BillGenerationBatch{
		ID:           batchID,
		ApartmentID:  apartmentID,
		BillingMonth: req.BillingMonth,
		CreatedBy:    createdBy,
		CreatedAt:    time.Now().UTC(),
	}
	var items []billing.BillGenerationBatchItem

	// --- 2. Run the whole batch inside a transaction ---
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		built, buildErr := s.buildBatchItems(txCtx, apartmentID, req.BillingMonth, batchID)
		if buildErr != nil {
			return buildErr
		}
		items = built

		// Aggregate counts
		batch.TotalContracts = len(items)
		for _, it := range items {
			switch it.ResultType {
			case billing.ResultCreated:
				batch.CreatedCount++
			case billing.ResultAlreadyExists:
				batch.AlreadyExistsCount++
			case billing.ResultSkipped:
				batch.SkippedCount++
			case billing.ResultFailed:
				batch.FailedCount++
			}
		}
		batch.ComputeStatus()

		return s.batches.CreateBatch(txCtx, batch, items)
	}); err != nil {
		return nil, fmt.Errorf("batch billing: %w", err)
	}

	return batch, nil
}

// buildBatchItems classifies each active contract and computes a snapshot
// for the CREATED cases. Runs inside the batch transaction so everything is atomic.
func (s *service) buildBatchItems(
	ctx context.Context,
	apartmentID uuid.UUID,
	billingMonth string,
	batchID uuid.UUID,
) ([]billing.BillGenerationBatchItem, error) {
	in, err := s.loadBatchInputs(ctx, apartmentID, billingMonth)
	if err != nil {
		return nil, err
	}
	if len(in.contracts) == 0 {
		return []billing.BillGenerationBatchItem{}, nil
	}

	items := make([]billing.BillGenerationBatchItem, 0, len(in.contracts))
	for _, c := range in.contracts {
		cls := classifyContractForBatch(c, in.startOfMonth, in.endOfMonth, in.pendingMoveOuts, in.meterMap, in.existingMap)
		item := billing.BillGenerationBatchItem{
			BatchID:    batchID,
			ContractID: c.ContractID,
			RoomID:     c.RoomID,
			RoomNumber: c.RoomNumber,
			RoomFloor:  c.RoomFloor,
			ResultType: cls.ResultType,
			ReasonCode: cls.ReasonCode,
			ReasonText: cls.ReasonText,
			BillID:     cls.BillID,
		}
		if cls.ResultType == billing.ResultCreated {
			// Utility-scoped recovery overlay: when the winner is a recovery anchor,
			// the unaffected utility bills its real usage from the coexisting
			// consumption row (owner lock 2026-07-18). No-op otherwise.
			reading := billing.ProjectRecoveryUsageOverlay(in.meterMap[c.RoomID], in.consumptionMap[c.RoomID])
			recon, rErr := billing.ResolveRecoveryReconciliation(ctx, reading, c.ContractID, s.bills)
			if rErr != nil {
				return nil, fmt.Errorf("resolve recovery reconciliation: %w", rErr)
			}
			item.ComputedSnapshot = billing.ComputeMonthlyBillSnapshot(
				billingMonth, c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading, recon,
			)
		}
		items = append(items, item)
	}
	return items, nil
}

// --- CommitBatch ---

// CommitBatch reads the computed snapshots from a generate batch and creates
// Bill(DRAFT) rows. Per-item transactions ensure partial progress is preserved.
// Idempotent: retrying after partial commit only processes uncommitted items.
//
// DRAFT (not FINALIZED) is intentional — monthly bills enter the editable
// curation phase mirroring settlement bills. Admin reviews + optionally edits
// (override AUTO amounts, add MANUAL items, set note) before explicit Finalize.
// See project_billing_editable_monthly_arch_lock.md for the locked semantics.
func (s *service) CommitBatch(ctx context.Context, batchID uuid.UUID) (*billing.CommitBatchResult, error) {
	// 1. Lock batch + read pending items in a short tx, then release.
	var batch *billing.BillGenerationBatch
	var items []billing.BillGenerationBatchItem

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.batches.LockBatchForCommit(txCtx, batchID)
		if err != nil {
			return err
		}
		if err := b.CanCommit(); err != nil {
			return err
		}
		batch = b

		pending, err := s.batches.ListCommitPendingItems(txCtx, batchID)
		if err != nil {
			return err
		}
		items = pending
		return nil
	}); err != nil {
		if database.IsNotFound(err) {
			return nil, billing.ErrBatchNotFound
		}
		if errors.Is(err, billing.ErrBatchAlreadyCommitted) {
			return nil, respond.ErrConflict.WithMessage("batch ถูก commit ไปแล้ว")
		}
		return nil, fmt.Errorf("lock batch: %w", err)
	}

	// 2. Per-item commit loop
	successCount := 0
	failCount := 0
	var lastInfraErr error

	for _, item := range items {
		err := s.commitOneItem(ctx, batch, item)
		if err != nil {
			if isInfraError(err) {
				lastInfraErr = err
				break
			}
			// Business error: record in a separate tx so rollback of create doesn't erase it.
			if logErr := s.batches.UpdateBatchItemCommitError(ctx, item.ID, err.Error()); logErr != nil {
				slog.Error("failed to record commit error on batch item", "item_id", item.ID, "error", logErr)
			}
			failCount++
			continue
		}
		successCount++
	}

	// 3. Finalize commit status
	pendingCount := len(items) - successCount - failCount
	batch.MarkCommitResult(successCount, failCount, pendingCount)

	if batch.CommitStatus != nil {
		if err := s.batches.UpdateBatchCommitStatus(ctx, batchID, *batch.CommitStatus, batch.CommittedAt); err != nil {
			slog.Warn("failed to update batch commit status", "batch_id", batchID, "status", *batch.CommitStatus, "error", err)
		}
	}

	result := &billing.CommitBatchResult{
		Batch:        batch,
		SuccessCount: successCount,
		FailCount:    failCount,
		PendingCount: pendingCount,
	}

	if lastInfraErr != nil {
		return result, fmt.Errorf("commit batch (partial): %w", lastInfraErr)
	}
	return result, nil
}

// commitOneItem creates a single DRAFT bill from a batch item's snapshot.
// Runs in its own transaction so failures are isolated per-item.
//
// Bills land as DRAFT so admin can review + edit before explicit Finalize.
// Audit: emits CREATE_DRAFT with actor=nil (batch commit is system-triggered,
// admin clicks "commit batch" but per-bill creation is not a per-bill admin
// action). Payload includes batch_id / room_id / billing_month so the audit
// timeline can link back to the batch run without joining batch_items.
func (s *service) commitOneItem(ctx context.Context, batch *billing.BillGenerationBatch, item billing.BillGenerationBatchItem) error {
	return s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		snapshot := item.ComputedSnapshot
		if err := snapshot.Validate(); err != nil {
			return err
		}

		// Pre-generate bill ID so line items can reference it in a single Create.
		// BeforeCreate hook skips uuid.New() when ID is already set.
		billID := uuid.New()
		bill := &billing.Bill{
			ID:           billID,
			ContractID:   item.ContractID,
			BillingMonth: batch.BillingMonth,
			BillType:     billing.BillTypeMonthly,
			Status:       billing.BillStatusDraft,
			BatchID:      &item.BatchID,
			LineItems:    snapshot.ToLineItems(billID),
		}
		bill.CalculateTotal()

		if err := s.bills.CreateBill(txCtx, bill); err != nil {
			// PG unique-violation on idx_bills_unique_monthly: another non-VOID
			// bill for the same (contract, month) exists. Race against a parallel
			// single-bill create / correction DRAFT that landed between planner
			// snapshot and commit. Translate to ErrBillAlreadyExists so the
			// isInfraError whitelist surfaces it as a business conflict — the
			// loop marks this item failed and continues with the rest of the
			// batch instead of aborting the whole commit.
			if billing.IsDuplicateBillError(err) {
				return billing.ErrBillAlreadyExists
			}
			return err
		}

		if err := s.audit.RecordAudit(txCtx, bill.ID, billing.AuditCreateDraft, nil, billing.AuditCreateDraftPayload{
			LineItemCount: len(bill.LineItems),
			TotalAmount:   bill.TotalAmount,
			BatchID:       &item.BatchID,
			RoomID:        &item.RoomID,
			BillingMonth:  batch.BillingMonth,
		}); err != nil {
			return err
		}

		return s.batches.UpdateBatchItemCommitted(txCtx, item.ID, bill.ID)
	})
}

// --- BatchFinalizeAll ---

// BatchFinalizeAll transitions every DRAFT monthly bill in the given batch
// to FINALIZED in per-item transactions. Idempotent + continue-on-error:
// already-FINALIZED bills are silent-skipped (not counted), non-DRAFT
// non-FINALIZED states surface as NOT_DRAFT failures, infra errors surface
// as INFRA_ERROR. Each successful finalize emits one FINALIZE audit row
// inside the same per-item TX (lock #10: audit count == success count).
//
// Settlement bills are excluded at SQL by ListBillsByBatchID; the
// b.IsMonthly() guard below is defense-in-depth per lock #9.
func (s *service) BatchFinalizeAll(ctx context.Context, batchID uuid.UUID, actor *uuid.UUID) (*BatchFinalizeResult, error) {
	bills, err := s.bills.ListByBatchID(ctx, batchID)
	if err != nil {
		return nil, fmt.Errorf("list batch bills: %w", err)
	}

	result := &BatchFinalizeResult{
		TotalCount: len(bills),
		Failures:   []BatchFinalizeFailure{},
	}

	for _, b := range bills {
		// Lock #9 — never finalize settlement bills.
		if !b.IsMonthly() {
			slog.Warn("BatchFinalizeAll: skipping non-monthly bill in batch",
				"batch_id", batchID, "bill_id", b.ID, "bill_type", b.BillType)
			continue
		}

		// Idempotency: already-FINALIZED bills don't count toward success
		// or failure — they're invisible to this call (lock #1).
		if b.IsFinalized() {
			continue
		}

		err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
			return s.bills.FinalizeBillInTx(txCtx, b.ID, actor)
		})
		if err != nil {
			code, msg := classifyFinalizeError(err)
			if code == FailureCodeInfraError {
				slog.Error("BatchFinalizeAll: bill finalize infra error",
					"batch_id", batchID, "bill_id", b.ID, "error", err)
			}
			result.Failures = append(result.Failures, BatchFinalizeFailure{
				BillID:  b.ID,
				Code:    code,
				Message: msg,
			})
			continue
		}
		result.SuccessCount++
	}

	result.FailCount = len(result.Failures)
	return result, nil
}

// --- FinalizeAllByMonth ---

// FinalizeAllByMonth bulk-finalizes every DRAFT MONTHLY bill scoped to
// (apartment, billing_month). Per-month sibling of BatchFinalizeAll for
// bills created via the reconciliation Generate path (which produces bills
// without a Batch wrapper). Loop semantics mirror BatchFinalizeAll exactly
// — only the scope query differs.
func (s *service) FinalizeAllByMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string, actor *uuid.UUID) (*BatchFinalizeResult, error) {
	if !billingmonth.Valid(billingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	bills, err := s.bills.ListMonthlyByApartmentMonth(ctx, apartmentID, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("list monthly bills by apartment+month: %w", err)
	}

	result := &BatchFinalizeResult{
		TotalCount: len(bills),
		Failures:   []BatchFinalizeFailure{},
	}

	for _, b := range bills {
		if !b.IsMonthly() {
			slog.Warn("FinalizeAllByMonth: skipping non-monthly bill",
				"apartment_id", apartmentID, "billing_month", billingMonth, "bill_id", b.ID, "bill_type", b.BillType)
			continue
		}

		if b.IsFinalized() || b.IsPaid() {
			continue
		}

		err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
			return s.bills.FinalizeBillInTx(txCtx, b.ID, actor)
		})
		if err != nil {
			code, msg := classifyFinalizeError(err)
			if code == FailureCodeInfraError {
				slog.Error("FinalizeAllByMonth: bill finalize infra error",
					"apartment_id", apartmentID, "billing_month", billingMonth, "bill_id", b.ID, "error", err)
			}
			result.Failures = append(result.Failures, BatchFinalizeFailure{
				BillID:  b.ID,
				Code:    code,
				Message: msg,
			})
			continue
		}
		result.SuccessCount++
	}

	result.FailCount = len(result.Failures)
	return result, nil
}

// --- Batch query (for review page) ---

func (s *service) GetBatchByID(ctx context.Context, id uuid.UUID) (*billing.BillGenerationBatch, error) {
	b, err := s.batches.FindBatchByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find batch: %w", err)
	}
	return b, nil
}

// GetBatchItems returns every batch item with its tenant + edit history flag.
// Issues ONE batched EditedBillIDs query for all committed bills in the
// response (items where BillID != nil) so the Edited badge can render
// directly on BillBatchReview without forcing the admin to open every
// drawer. Pre-commit items (BillID == nil) keep IsEdited=false trivially.
//
// On audit-store failure the call returns the wrapped error per the locked
// is_edited contract — never silently hide edited state.
func (s *service) GetBatchItems(ctx context.Context, id uuid.UUID) ([]billing.BatchItemWithTenant, error) {
	items, err := s.batches.FindBatchItemsByBatchID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Collect bill IDs from committed items only — uncommitted items have
	// no bill row and therefore no audit history to query.
	var billIDs []uuid.UUID
	for _, it := range items {
		if it.BillID != nil {
			billIDs = append(billIDs, *it.BillID)
		}
	}
	if len(billIDs) == 0 {
		return items, nil
	}

	editedSet, err := s.audit.EditedBillIDs(ctx, billIDs)
	if err != nil {
		return nil, fmt.Errorf("populate batch item is_edited: %w", err)
	}
	for i := range items {
		if items[i].BillID != nil && editedSet[*items[i].BillID] {
			items[i].IsEdited = true
		}
	}
	return items, nil
}

func (s *service) ListBatches(ctx context.Context, params billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error) {
	params.Normalize()
	if params.Status != "" && !billing.BatchStatus(params.Status).IsValid() {
		return nil, 0, respond.ErrBadRequest.WithMessage("status ไม่ถูกต้อง")
	}
	return s.batches.ListBatches(ctx, params)
}

// --- Error classification helpers ---

// classifyFinalizeError maps a finalizeBillInTx error to the FE-facing
// failure code + Thai message. ErrNotDraft / ErrNoLineItems are domain
// sentinels surfaced via errors.Is; everything else falls through to
// INFRA_ERROR so the underlying cause never leaks to the FE.
func classifyFinalizeError(err error) (BatchFinalizeFailureCode, string) {
	switch {
	case errors.Is(err, billing.ErrNoLineItems):
		return FailureCodeNoLineItems, "ออกบิลไม่ได้: บิลไม่มีรายการ"
	case errors.Is(err, billing.ErrNotDraft):
		return FailureCodeNotDraft, "ออกบิลไม่ได้: บิลไม่ใช่สถานะร่าง"
	case errors.Is(err, billing.ErrBillStaleAfterRecovery):
		return FailureCodeStaleAfterRecovery, "ออกบิลไม่ได้: มีการแก้ค่ามิเตอร์หลังจากสร้างบิลนี้ กรุณาสร้างบิลใหม่"
	default:
		return FailureCodeInfraError, "เกิดข้อผิดพลาดของระบบ กรุณาลองอีกครั้ง"
	}
}

// isInfraError returns true for infrastructure/system errors that should stop the loop.
// Business errors (validation, snapshot issues) are whitelisted and return false.
func isInfraError(err error) bool {
	// Whitelist known business errors — everything else is infra.
	if errors.Is(err, billing.ErrSnapshotUnsupportedVersion) ||
		errors.Is(err, billing.ErrSnapshotNoLineItems) ||
		errors.Is(err, billing.ErrSnapshotNegativeTotal) ||
		errors.Is(err, billing.ErrBillAlreadyExists) ||
		errors.Is(err, billing.ErrBatchAlreadyCommitted) {
		return false
	}
	// respond.AppError with 4xx status = business error
	var appErr *respond.AppError
	if errors.As(err, &appErr) && appErr.HTTPStatus >= 400 && appErr.HTTPStatus < 500 {
		return false
	}
	return true
}
