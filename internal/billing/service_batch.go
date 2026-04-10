package billing

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"nana/internal/meterreading"
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
