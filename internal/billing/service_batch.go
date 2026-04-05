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
// in an apartment for a given billing month. Best-effort mode: processes every contract
// independently and returns a structured report.
func (s *billingService) BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest) (*BatchBillResult, error) {
	// --- 1. Validate input ---
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง")
	}
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	startOfMonth, endOfMonth := parseBillingMonthRange(req.BillingMonth)

	// --- 2. Fetch candidates ---
	contracts, err := s.repo.FindActiveContractsByApartmentID(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("batch billing: find contracts: %w", err)
	}
	if len(contracts) == 0 {
		return &BatchBillResult{Details: []ContractBillResult{}}, nil
	}

	// --- 3. Bulk pre-fetch (3 queries) ---
	roomIDs := make([]uuid.UUID, len(contracts))
	contractIDs := make([]uuid.UUID, len(contracts))
	for i, c := range contracts {
		roomIDs[i] = c.RoomID
		contractIDs[i] = c.ContractID
	}

	pendingMoveOuts, err := s.moveOuts.FindRoomIDsWithPendingNotice(ctx, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("batch billing: check move-outs: %w", err)
	}

	meterMap, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, roomIDs, req.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("batch billing: find meters: %w", err)
	}

	existingMap, err := s.repo.FindExistingByContractsAndMonth(ctx, contractIDs, req.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("batch billing: find existing bills: %w", err)
	}

	// --- 4. Process each contract ---
	details := make([]ContractBillResult, 0, len(contracts))
	var summary BatchBillSummary
	summary.TotalContracts = len(contracts)

	for _, c := range contracts {
		result := ContractBillResult{
			ContractID: c.ContractID,
			RoomID:     c.RoomID,
			RoomNumber: c.RoomNumber,
			RoomFloor:  c.RoomFloor,
		}

		// 4a. Move-out: skip if pending (priority — settlement flow takes over)
		if pendingMoveOuts[c.RoomID] {
			result.Status = BatchItemSkipped
			result.ReasonCode = "MOVE_OUT_PENDING"
			result.ReasonText = "มีใบแจ้งย้ายออกรอดำเนินการ"
			summary.Skipped++
			details = append(details, result)
			continue
		}

		// 4b. Billability: skip if not billable for this month
		if c.StartDate.After(endOfMonth) {
			result.Status = BatchItemSkipped
			result.ReasonCode = "NOT_BILLABLE"
			result.ReasonText = "สัญญายังไม่เริ่มในเดือนที่ออกบิล"
			summary.Skipped++
			details = append(details, result)
			continue
		}
		if c.EndDate != nil && c.EndDate.Before(startOfMonth) {
			result.Status = BatchItemSkipped
			result.ReasonCode = "NOT_BILLABLE"
			result.ReasonText = "สัญญาจบแล้วก่อนเดือนที่ออกบิล"
			summary.Skipped++
			details = append(details, result)
			continue
		}

		// 4c. Meter: skip if not found
		reading, hasMeter := meterMap[c.RoomID]
		if !hasMeter {
			result.Status = BatchItemSkipped
			result.ReasonCode = "NO_METER_READING"
			result.ReasonText = "ยังไม่มีข้อมูลมิเตอร์สำหรับเดือนนี้"
			summary.Skipped++
			details = append(details, result)
			continue
		}

		// 4d. Existing: mark if found (skip create)
		if existing, ok := existingMap[c.ContractID]; ok {
			result.Status = BatchItemExisting
			result.ReasonCode = "ALREADY_EXISTS"
			result.ReasonText = "มีบิลสำหรับเดือนนี้อยู่แล้ว"
			result.BillID = &existing.ID
			summary.Existing++
			details = append(details, result)
			continue
		}

		// 4e. Create bill directly (skip re-validation — already pre-checked)
		bill, createErr := s.buildAndCreateMonthlyBill(ctx, c.ContractID, req.BillingMonth,
			c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading)
		if createErr == nil {
			result.Status = BatchItemCreated
			result.BillID = &bill.ID
			summary.Created++
			details = append(details, result)
			continue
		}

		// Race condition: bill created between pre-check and create
		if errors.Is(createErr, ErrBillAlreadyExists) {
			existing, refetchErr := s.repo.FindByContractAndMonth(ctx, c.ContractID, req.BillingMonth, BillTypeMonthly)
			if refetchErr == nil {
				result.Status = BatchItemExisting
				result.ReasonCode = "ALREADY_EXISTS"
				result.ReasonText = "มีบิลสำหรับเดือนนี้อยู่แล้ว"
				result.BillID = &existing.ID
				summary.Existing++
				details = append(details, result)
				continue
			}
			// Re-fetch failed — log and classify as system error
			slog.Warn("batch billing: race re-fetch failed",
				"contract_id", c.ContractID,
				"room", c.RoomNumber,
				"error", refetchErr,
			)
			result.Status = BatchItemFailed
			result.ReasonCode = "SYSTEM_ERROR"
			result.ReasonText = "เกิดข้อผิดพลาดของระบบ"
			summary.Failed++
			details = append(details, result)
			continue
		}

		// Failed: categorize error
		result.Status = BatchItemFailed
		result.ReasonCode, result.ReasonText = classifyBatchError(createErr)
		summary.Failed++
		details = append(details, result)

		slog.Warn("batch billing: create failed",
			"contract_id", c.ContractID,
			"room", c.RoomNumber,
			"error", createErr,
		)
	}

	return &BatchBillResult{
		Summary: summary,
		Details: details,
	}, nil
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
	// Map specific known errors to precise reason codes
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

	// Generic classification by HTTP status
	var appErr *respond.AppError
	if errors.As(err, &appErr) {
		if appErr.HTTPStatus >= 400 && appErr.HTTPStatus < 500 {
			return "VALIDATION_ERROR", appErr.Message
		}
	}
	return "SYSTEM_ERROR", "เกิดข้อผิดพลาดของระบบ"
}
