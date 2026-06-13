package billing

import (
	"context"

	"nana/internal/shared/billingmonth"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// MonthlyPreflightResult is the readiness count summary for the Generate page
// — same classification a real batch run would produce, read-only.
type MonthlyPreflightResult struct {
	TotalRooms          int
	ReadyCount          int
	MissingMeterCount   int
	AlreadyExistsCount  int
	MoveOutPendingCount int
	NotBillableCount    int
}

// PreflightMonthly returns readiness counts without persisting anything.
// Uses the same loadBatchInputs + classifyContractForBatch pair as the real
// batch flow, so counts always match what a subsequent batch run would tally.
func (s *billingService) PreflightMonthly(ctx context.Context, req MonthlyPreflightRequest) (*MonthlyPreflightResult, error) {
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
		case cls.ResultType == ResultCreated:
			result.ReadyCount++
		case cls.ReasonCode == ReasonAlreadyExists:
			result.AlreadyExistsCount++
		case cls.ReasonCode == ReasonMissingMeterReading:
			result.MissingMeterCount++
		case cls.ReasonCode == ReasonMoveOutPending:
			result.MoveOutPendingCount++
		case cls.ReasonCode == ReasonNotBillable:
			result.NotBillableCount++
		}
	}
	return result, nil
}
