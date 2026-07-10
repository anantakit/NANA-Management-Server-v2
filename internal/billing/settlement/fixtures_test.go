package settlement

// Test fixture helpers for the settlement package's mock-based unit tests.
// Migrated from billing root in W4 commit 3 (2026-06-19). Lives alongside
// service_test_mocks.go.

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"

	"github.com/google/uuid"
)

// --- Contract fixtures ---

func earlyExitContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		StartDate:              time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MinMonths:              6, // eligible at July 1
		MonthlyRent:            500000,
		DepositAmount:          1000000, // 10,000 baht
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
}

func normalExitContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		StartDate:              time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		MinMonths:              6, // eligible at Dec 1
		MonthlyRent:            500000,
		DepositAmount:          1000000,
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
}

func testContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		StartDate:              time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		MinMonths:              6,
		MonthlyRent:            500000,
		DepositAmount:          1000000,
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
}

// --- Bill fixtures ---

func unpaidBill(contractID uuid.UUID, billingMonth string, total int64) billing.Bill {
	return billing.Bill{
		ID:           uuid.New(),
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  total,
		LineItems: []billing.BillLineItem{
			{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Amount: 500000, SortOrder: 1},
			{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Amount: total - 500000 - 18000, SortOrder: 2},
			{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Amount: 18000, SortOrder: 3},
		},
	}
}

func unpaidBillWithUtility(contractID uuid.UUID, billingMonth string, rent, elec, water int64) billing.Bill {
	return billing.Bill{
		ID:           uuid.New(),
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  rent + elec + water,
		LineItems: []billing.BillLineItem{
			{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Amount: rent, SortOrder: 1},
			{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Amount: elec, SortOrder: 2},
			{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Amount: water, SortOrder: 3},
		},
	}
}

// --- Meter reading fixtures ---

func testMonthlyReading(roomID uuid.UUID, billingMonth string) *meterreading.MeterReading {
	month := billingMonth
	t := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	return &meterreading.MeterReading{
		ID:                  uuid.New(),
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &month,
		ReadingDateActual:   &t,
		ElectricityPrevious: 100,
		ElectricityCurrent:  150,
		WaterPrevious:       50,
		WaterCurrent:        55,
	}
}

func testExitReading(roomID uuid.UUID, moveOutDate time.Time) *meterreading.MeterReading {
	d := moveOutDate
	return &meterreading.MeterReading{
		ID:                  uuid.New(),
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeExit,
		ReadingDateActual:   &d,
		ElectricityPrevious: 100,
		ElectricityCurrent:  150,
		WaterPrevious:       50,
		WaterCurrent:        55,
	}
}

// --- Move-out notice fixture ---

func completedNotice(contractID uuid.UUID, moveOutDate time.Time) *moveout.MoveOutNotice {
	d := moveOutDate
	return &moveout.MoveOutNotice{
		ID:                    uuid.New(),
		ContractID:            contractID,
		Status:                moveout.MoveOutStatusPendingSettlement,
		NoticeDate:            moveOutDate.AddDate(0, -1, 0),
		ScheduledMoveOutDate:  moveOutDate,
		ActualMoveOutDate:     &d,
	}
}

// --- Service constructors with the standard mock harness ---

// newSvcWithMocks constructs *Service wiring all 7 ports with hand-rolled mocks.
// Mirrors the pattern of billing root's newSvc helper. PaymentRouting defaults
// to nil — settlement degrades gracefully when no rules are configured.
func newSvcWithMocks(
	bills *mockBillStore,
	contracts *mockContractSource,
	meters *mockMeterReadingSource,
	configs *mockBillingConfigSource,
	moveOuts *mockMoveOutSource,
) *Service {
	return NewService(
		bills,
		&mockAuditStore{},
		contracts,
		meters,
		configs,
		moveOuts,
		nil,
		&mockTxManager{},
	)
}

// settlementSetup wires a Service configured for GenerateSettlement paths
// (caller supplies moveOutDate directly — no notice lookup needed). Returns
// the bill store mock for test assertions on persisted state.
func settlementSetup(c *contract.Contract, moveOutDate time.Time, unpaidBills []billing.Bill) (*mockBillStore, *Service) {
	exitReading := testExitReading(c.RoomID, moveOutDate)

	bills := &mockBillStore{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]billing.Bill, error) {
			return unpaidBills, nil
		},
	}
	svc := newSvcWithMocks(
		bills,
		&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
		&mockMeterReadingSource{findExitFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
		&mockBillingConfigSource{},
		&mockMoveOutSource{},
	)
	return bills, svc
}

// previewSetup wires a Service configured for PreviewSettlement paths
// (notice lookup IS exercised — caller does not supply moveOutDate).
func previewSetup(c *contract.Contract, moveOutDate time.Time, unpaidBills []billing.Bill) (*mockBillStore, *Service) {
	exitReading := testExitReading(c.RoomID, moveOutDate)
	notice := completedNotice(c.ID, moveOutDate)

	bills := &mockBillStore{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]billing.Bill, error) {
			return unpaidBills, nil
		},
	}
	svc := newSvcWithMocks(
		bills,
		&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
		&mockMeterReadingSource{findExitFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
		&mockBillingConfigSource{},
		&mockMoveOutSource{notice: notice},
	)
	return bills, svc
}

// extractMockBillStore recovers the bill store mock from the service for test
// assertions on persisted state. Replaces billing root's extractMockRepo.
func extractMockBillStore(svc *Service) *mockBillStore {
	return svc.bills.(*mockBillStore)
}

// _ = (testing.TB)(nil) — keep the testing import live in case a future
// helper takes a *testing.T.
var _ = (testing.TB)(nil)
