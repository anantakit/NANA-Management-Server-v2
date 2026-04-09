package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
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

// buildBatchItems classifies each active contract and attempts to create a bill
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

		// 5. Create bill
		bill, createErr := s.buildAndCreateMonthlyBill(ctx, c.ContractID, billingMonth,
			c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading, &batchID)
		if createErr == nil {
			item.ResultType = ResultCreated
			item.BillID = &bill.ID
			items = append(items, item)
			continue
		}

		// 5a. Race: another request created the bill between pre-check and insert
		if errors.Is(createErr, ErrBillAlreadyExists) {
			existing, refetchErr := s.repo.FindByContractAndMonth(ctx, c.ContractID, billingMonth, BillTypeMonthly)
			if refetchErr == nil {
				item.ResultType = ResultAlreadyExists
				item.ReasonCode = ReasonAlreadyExists
				item.ReasonText = "มีบิลสำหรับเดือนนี้อยู่แล้ว"
				item.BillID = &existing.ID
				items = append(items, item)
				continue
			}
			slog.Warn("batch billing: race re-fetch failed",
				"contract_id", c.ContractID, "room", c.RoomNumber, "error", refetchErr)
			item.ResultType = ResultFailed
			item.ReasonCode = ReasonSystemError
			item.ReasonText = "เกิดข้อผิดพลาดของระบบ"
			items = append(items, item)
			continue
		}

		// 5b. Other failures — classify
		item.ResultType = ResultFailed
		item.ReasonCode, item.ReasonText = classifyBatchError(createErr)
		items = append(items, item)

		slog.Warn("batch billing: create failed",
			"contract_id", c.ContractID, "room", c.RoomNumber, "error", createErr)
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

// classifyBatchError maps errors to reason code + Thai text for batch results.
func classifyBatchError(err error) (code, text string) {
	switch {
	case errors.Is(err, ErrContractNotActive):
		return "CONTRACT_NOT_ACTIVE", "สัญญาไม่ได้อยู่ในสถานะใช้งาน"
	case errors.Is(err, ErrMeterNotFound):
		return "METER_NOT_FOUND", "ไม่พบข้อมูลมิเตอร์"
	case errors.Is(err, ErrMeterTypeMismatch):
		return "METER_TYPE_MISMATCH", "มิเตอร์ไม่ใช่ประเภทรายเดือน"
	case errors.Is(err, ErrMeterRoomMismatch):
		return "METER_ROOM_MISMATCH", "มิเตอร์ไม่ตรงกับห้องในสัญญา"
	case errors.Is(err, ErrMeterMonthMismatch):
		return "METER_MONTH_MISMATCH", "เดือนของมิเตอร์ไม่ตรงกับเดือนที่ออกบิล"
	}

	var appErr *respond.AppError
	if errors.As(err, &appErr) {
		if appErr.HTTPStatus >= 400 && appErr.HTTPStatus < 500 {
			return ReasonValidationError, appErr.Message
		}
	}
	return ReasonSystemError, "เกิดข้อผิดพลาดของระบบ"
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
	return s.repo.ListBatches(ctx, params)
}
