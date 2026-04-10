package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"nana/internal/meterreading"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BatchCreateMonthlyBills generates monthly bills for all eligible active contracts
// in an apartment for a given billing month. Persists a BillGenerationBatch run-log
// with per-contract items so the result can be re-opened later by batch_id.
func (s *billingService) BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest, createdBy *uuid.UUID) (*BillGenerationBatch, error) {
	// --- 1. Validate input ---
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง")
	}
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	// Pre-generate batch ID so bills can reference it before the batch row is inserted.
	batchID := uuid.New()
	batch := &BillGenerationBatch{
		ID:           batchID,
		ApartmentID:  apartmentID,
		BillingMonth: req.BillingMonth,
		CreatedBy:    createdBy,
		CreatedAt:    time.Now().UTC(),
	}
	var items []BillGenerationBatchItem

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
			case ResultCreated:
				batch.CreatedCount++
			case ResultAlreadyExists:
				batch.AlreadyExistsCount++
			case ResultSkipped:
				batch.SkippedCount++
			case ResultFailed:
				batch.FailedCount++
			}
		}
		batch.ComputeStatus()

		return s.repo.CreateBatch(txCtx, batch, items)
	}); err != nil {
		return nil, fmt.Errorf("batch billing: %w", err)
	}

	return batch, nil
}

// buildBatchItems classifies each active contract and computes a snapshot
// for the CREATED cases. Runs inside the batch transaction so everything is atomic.
func (s *billingService) buildBatchItems(
	ctx context.Context,
	apartmentID uuid.UUID,
	billingMonth string,
	batchID uuid.UUID,
) ([]BillGenerationBatchItem, error) {
	contracts, err := s.repo.FindActiveContractsByApartmentID(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("find contracts: %w", err)
	}
	if len(contracts) == 0 {
		return []BillGenerationBatchItem{}, nil
	}

	startOfMonth, endOfMonth := parseBillingMonthRange(billingMonth)

	roomIDs := make([]uuid.UUID, len(contracts))
	contractIDs := make([]uuid.UUID, len(contracts))
	for i, c := range contracts {
		roomIDs[i] = c.RoomID
		contractIDs[i] = c.ContractID
	}

	pendingMoveOuts, err := s.moveOuts.FindRoomIDsWithPendingNotice(ctx, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("check move-outs: %w", err)
	}

	meterMap, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, roomIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find meters: %w", err)
	}

	existingMap, err := s.repo.FindExistingByContractsAndMonth(ctx, contractIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find existing bills: %w", err)
	}

	items := make([]BillGenerationBatchItem, 0, len(contracts))

	for _, c := range contracts {
		item := BillGenerationBatchItem{
			BatchID:    batchID,
			ContractID: c.ContractID,
			RoomID:     c.RoomID,
			RoomNumber: c.RoomNumber,
			RoomFloor:  c.RoomFloor,
		}

		// 1. Move-out: skip if pending (settlement flow takes over)
		if pendingMoveOuts[c.RoomID] {
			item.ResultType = ResultSkipped
			item.ReasonCode = ReasonMoveOutPending
			item.ReasonText = "มีใบแจ้งย้ายออกรอดำเนินการ"
			items = append(items, item)
			continue
		}

		// 2. Billability window
		if c.StartDate.After(endOfMonth) {
			item.ResultType = ResultSkipped
			item.ReasonCode = ReasonNotBillable
			item.ReasonText = "สัญญายังไม่เริ่มในเดือนที่ออกบิล"
			items = append(items, item)
			continue
		}
		if c.EndDate != nil && c.EndDate.Before(startOfMonth) {
			item.ResultType = ResultSkipped
			item.ReasonCode = ReasonNotBillable
			item.ReasonText = "สัญญาจบแล้วก่อนเดือนที่ออกบิล"
			items = append(items, item)
			continue
		}

		// 3. Meter reading required
		reading, hasMeter := meterMap[c.RoomID]
		if !hasMeter {
			item.ResultType = ResultSkipped
			item.ReasonCode = ReasonMissingMeterReading
			item.ReasonText = "ยังไม่มีข้อมูลมิเตอร์สำหรับเดือนนี้"
			items = append(items, item)
			continue
		}

		// 4. Already exists (pre-check)
		if existing, ok := existingMap[c.ContractID]; ok {
			item.ResultType = ResultAlreadyExists
			item.ReasonCode = ReasonAlreadyExists
			item.ReasonText = "มีบิลสำหรับเดือนนี้อยู่แล้ว"
			item.BillID = &existing.ID
			items = append(items, item)
			continue
		}

		// 5. Compute snapshot (no bill row — will be persisted by commit step)
		snapshot := computeMonthlyBillSnapshot(billingMonth,
			c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading)
		item.ResultType = ResultCreated
		item.ComputedSnapshot = snapshot
		items = append(items, item)
	}

	return items, nil
}

// parseBillingMonthRange converts "YYYY-MM" to start and end of that month.
// Uses time.UTC consistently — PG date columns are timezone-naive.
func parseBillingMonthRange(billingMonth string) (start, end time.Time) {
	year, _ := strconv.Atoi(billingMonth[:4])
	month, _ := strconv.Atoi(billingMonth[5:7])
	start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, -1) // last day of month
	return start, end
}

// computeMonthlyBillSnapshot builds the line items + total for a monthly bill
// without touching the bills table. Persistence happens later in the commit step.
func computeMonthlyBillSnapshot(
	billingMonth string,
	monthlyRent, elecRate, waterRate int64,
	reading *meterreading.MeterReading,
) ComputedSnapshot {
	nextMonth := advanceMonth(billingMonth)
	elecUnits := reading.ElectricityUsed()
	waterUnits := reading.WaterUsed()

	lines := []ComputedLineItem{
		{
			Type:        LineItemRoomRent,
			Description: fmt.Sprintf("ค่าห้อง %s", nextMonth),
			Amount:      monthlyRent,
			SortOrder:   1,
		},
		{
			Type:        LineItemElectricity,
			Description: fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits),
			Amount:      int64(elecUnits) * elecRate,
			Quantity:    elecUnits,
			UnitPrice:   elecRate,
			SortOrder:   2,
		},
		{
			Type:        LineItemWater,
			Description: fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits),
			Amount:      int64(waterUnits) * waterRate,
			Quantity:    waterUnits,
			UnitPrice:   waterRate,
			SortOrder:   3,
		},
	}

	var total int64
	for _, li := range lines {
		total += li.Amount
	}

	return ComputedSnapshot{
		Version:     ComputedSnapshotVersion,
		LineItems:   lines,
		TotalAmount: total,
		ComputedAt:  time.Now().UTC(),
	}
}

// CommitBatch reads the computed snapshots from a generate batch and creates
// Bill(FINALIZED) rows. Per-item transactions ensure partial progress is preserved.
// Idempotent: retrying after partial commit only processes uncommitted items.
func (s *billingService) CommitBatch(ctx context.Context, batchID uuid.UUID) (*CommitBatchResult, error) {
	// 1. Lock batch + read pending items in a short tx, then release.
	var batch *BillGenerationBatch
	var items []BillGenerationBatchItem

	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.repo.LockBatchForCommit(txCtx, batchID)
		if err != nil {
			return err
		}
		if err := b.CanCommit(); err != nil {
			return err
		}
		batch = b

		pending, err := s.repo.ListCommitPendingItems(txCtx, batchID)
		if err != nil {
			return err
		}
		items = pending
		return nil
	}); err != nil {
		if isNotFound(err) {
			return nil, ErrBatchNotFound
		}
		if errors.Is(err, ErrBatchAlreadyCommitted) {
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
			if logErr := s.repo.UpdateBatchItemCommitError(ctx, item.ID, err.Error()); logErr != nil {
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
		if err := s.repo.UpdateBatchCommitStatus(ctx, batchID, *batch.CommitStatus, batch.CommittedAt); err != nil {
			slog.Warn("failed to update batch commit status", "batch_id", batchID, "status", *batch.CommitStatus, "error", err)
		}
	}

	result := &CommitBatchResult{
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

// commitOneItem creates a single FINALIZED bill from a batch item's snapshot.
// Runs in its own transaction so failures are isolated per-item.
func (s *billingService) commitOneItem(ctx context.Context, batch *BillGenerationBatch, item BillGenerationBatchItem) error {
	return s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		snapshot := item.ComputedSnapshot
		if err := snapshot.Validate(); err != nil {
			return err
		}

		// Pre-generate bill ID so line items can reference it in a single Create.
		// BeforeCreate hook skips uuid.New() when ID is already set.
		billID := uuid.New()
		bill := &Bill{
			ID:           billID,
			ContractID:   item.ContractID,
			BillingMonth: batch.BillingMonth,
			BillType:     BillTypeMonthly,
			Status:       BillStatusFinalized,
			BatchID:      &item.BatchID,
			LineItems:    snapshot.ToLineItems(billID),
		}
		bill.CalculateTotal()

		if err := s.repo.Create(txCtx, bill); err != nil {
			return err
		}

		return s.repo.UpdateBatchItemCommitted(txCtx, item.ID, bill.ID)
	})
}

// isInfraError returns true for infrastructure/system errors that should stop the loop.
// Business errors (validation, snapshot issues) are whitelisted and return false.
func isInfraError(err error) bool {
	// Whitelist known business errors — everything else is infra.
	if errors.Is(err, ErrSnapshotUnsupportedVersion) ||
		errors.Is(err, ErrSnapshotNoLineItems) ||
		errors.Is(err, ErrSnapshotNegativeTotal) ||
		errors.Is(err, ErrBillAlreadyExists) ||
		errors.Is(err, ErrBatchAlreadyCommitted) {
		return false
	}
	// respond.AppError with 4xx status = business error
	var appErr *respond.AppError
	if errors.As(err, &appErr) && appErr.HTTPStatus >= 400 && appErr.HTTPStatus < 500 {
		return false
	}
	return true
}

// isNotFound checks for gorm.ErrRecordNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// --- Batch query (for review page) ---

func (s *billingService) GetBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error) {
	b, err := s.repo.FindBatchByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find batch: %w", err)
	}
	return b, nil
}

func (s *billingService) GetBatchItems(ctx context.Context, id uuid.UUID) ([]BillGenerationBatchItem, error) {
	return s.repo.FindBatchItemsByBatchID(ctx, id)
}

func (s *billingService) ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error) {
	params.Normalize()
	if params.Status != "" && !BatchStatus(params.Status).IsValid() {
		return nil, 0, respond.ErrBadRequest.WithMessage("status ไม่ถูกต้อง")
	}
	return s.repo.ListBatches(ctx, params)
}
