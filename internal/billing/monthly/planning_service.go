package monthly

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// contractClassification is the verdict on a single contract for a billing
// month. Pure data — shared by batch (persists + computes snapshots) and
// preflight (counts only) so the rule has a single source of truth.
type contractClassification struct {
	ResultType billing.ResultType
	ReasonCode string
	ReasonText string
	BillID     *uuid.UUID
}

// classifyContractForBatch decides whether a contract should bill this month.
// Pure function. Check order matches batch priority — first match wins:
// move-out → billability window → meter → already-exists → CREATED.
func classifyContractForBatch(
	c billing.ContractWithRoom,
	startOfMonth, endOfMonth time.Time,
	pendingMoveOuts map[uuid.UUID]bool,
	meterMap map[uuid.UUID]*meterreading.MeterReading,
	existingMap map[uuid.UUID]*billing.Bill,
) contractClassification {
	if pendingMoveOuts[c.RoomID] {
		return contractClassification{
			ResultType: billing.ResultSkipped,
			ReasonCode: billing.ReasonMoveOutPending,
			ReasonText: "มีใบแจ้งย้ายออกรอดำเนินการ",
		}
	}
	if c.StartDate.After(endOfMonth) {
		return contractClassification{
			ResultType: billing.ResultSkipped,
			ReasonCode: billing.ReasonNotBillable,
			ReasonText: "สัญญายังไม่เริ่มในเดือนที่ออกบิล",
		}
	}
	if c.EndDate != nil && c.EndDate.Before(startOfMonth) {
		return contractClassification{
			ResultType: billing.ResultSkipped,
			ReasonCode: billing.ReasonNotBillable,
			ReasonText: "สัญญาจบแล้วก่อนเดือนที่ออกบิล",
		}
	}
	if _, hasMeter := meterMap[c.RoomID]; !hasMeter {
		return contractClassification{
			ResultType: billing.ResultSkipped,
			ReasonCode: billing.ReasonMissingMeterReading,
			ReasonText: "ยังไม่มีข้อมูลมิเตอร์สำหรับเดือนนี้",
		}
	}
	if existing, ok := existingMap[c.ContractID]; ok {
		return contractClassification{
			ResultType: billing.ResultAlreadyExists,
			ReasonCode: billing.ReasonAlreadyExists,
			ReasonText: "มีบิลสำหรับเดือนนี้อยู่แล้ว",
			BillID:     &existing.ID,
		}
	}
	return contractClassification{ResultType: billing.ResultCreated}
}

// batchInputs is the data needed to classify a batch of contracts, loaded once.
type batchInputs struct {
	contracts       []billing.ContractWithRoom
	startOfMonth    time.Time
	endOfMonth      time.Time
	pendingMoveOuts map[uuid.UUID]bool
	meterMap        map[uuid.UUID]*meterreading.MeterReading
	// consumptionMap holds the real (non-anchor) MONTHLY row per room, used only
	// to project the recovery overlay's UNAFFECTED utility usage. Separate from
	// meterMap (the recovery-preferred winner) so classification stays unchanged.
	consumptionMap map[uuid.UUID]*meterreading.MeterReading
	existingMap    map[uuid.UUID]*billing.Bill
}

// loadBatchInputs reads all data needed to classify contracts. Read-only;
// shared by BatchCreateMonthlyBills and PreflightMonthly.
func (s *service) loadBatchInputs(ctx context.Context, apartmentID uuid.UUID, billingMonth string) (*batchInputs, error) {
	contracts, err := s.bills.FindActiveContractsByApartmentID(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("find contracts: %w", err)
	}
	startOfMonth, endOfMonth := parseBillingMonthRange(billingMonth)
	if len(contracts) == 0 {
		return &batchInputs{
			contracts:       contracts,
			startOfMonth:    startOfMonth,
			endOfMonth:      endOfMonth,
			pendingMoveOuts: map[uuid.UUID]bool{},
			meterMap:        map[uuid.UUID]*meterreading.MeterReading{},
			consumptionMap:  map[uuid.UUID]*meterreading.MeterReading{},
			existingMap:     map[uuid.UUID]*billing.Bill{},
		}, nil
	}

	roomIDs := make([]uuid.UUID, len(contracts))
	contractIDs := make([]uuid.UUID, len(contracts))
	for i, c := range contracts {
		roomIDs[i] = c.RoomID
		contractIDs[i] = c.ContractID
	}

	pendingMoveOuts, err := s.moveOuts.FindRoomIDsWithMoveOutInMonth(ctx, roomIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("check move-outs: %w", err)
	}
	meterMap, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, roomIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find meters: %w", err)
	}
	consumptionMap, err := s.meters.FindConsumptionMonthlyByRoomsAndMonth(ctx, roomIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find consumption meters: %w", err)
	}
	existingMap, err := s.bills.FindExistingByContractsAndMonth(ctx, contractIDs, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find existing bills: %w", err)
	}

	return &batchInputs{
		contracts:       contracts,
		startOfMonth:    startOfMonth,
		endOfMonth:      endOfMonth,
		pendingMoveOuts: pendingMoveOuts,
		meterMap:        meterMap,
		consumptionMap:  consumptionMap,
		existingMap:     existingMap,
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
