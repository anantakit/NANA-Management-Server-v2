package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Hand-written mocks ---

type mockBillingRepo struct {
	findByIDFn                          func(ctx context.Context, id uuid.UUID) (*Bill, error)
	findByContractAndMonthFn            func(ctx context.Context, contractID uuid.UUID, month string, bt BillType) (*Bill, error)
	findNonVoidedByContractMonthFn      func(ctx context.Context, contractID uuid.UUID, month string) ([]Bill, error)
	findActiveContractsByApartmentIDFn  func(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error)
	findExistingByContractsAndMonthFn   func(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error)
	sumPaidFn                           func(ctx context.Context, contractID uuid.UUID, since string) (int64, error)
	createFn                            func(ctx context.Context, bill *Bill) error
	updateFn                            func(ctx context.Context, bill *Bill) error
	apartmentID                         uuid.UUID

	createdBill  *Bill
	updatedBills []*Bill

	createdBatch      *BillGenerationBatch
	createdBatchItems []BillGenerationBatchItem
}

var _ BillingRepository = (*mockBillingRepo)(nil)

func (m *mockBillingRepo) FindAll(_ context.Context, _ BillListParams) ([]BillWithRelations, int64, error) {
	return nil, 0, nil
}
func (m *mockBillingRepo) GetSummary(_ context.Context, _ BillSummaryParams) (*BillSummaryRaw, error) {
	return &BillSummaryRaw{}, nil
}
func (m *mockBillingRepo) FindByID(ctx context.Context, id uuid.UUID) (*Bill, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	if m.createdBill != nil {
		return m.createdBill, nil
	}
	return nil, gorm.ErrRecordNotFound
}
// FindByIDWithRelations mock จำลอง "latest persisted state by bill ID":
// match จาก updatedBills ย้อนหลังก่อน (post-mutation state) แล้วค่อย fallback ไป FindByID
func (m *mockBillingRepo) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	for i := len(m.updatedBills) - 1; i >= 0; i-- {
		if m.updatedBills[i].ID == id {
			return &BillWithRelations{Bill: *m.updatedBills[i]}, nil
		}
	}
	b, err := m.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &BillWithRelations{Bill: *b}, nil
}
func (m *mockBillingRepo) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, month string, bt BillType) (*Bill, error) {
	if m.findByContractAndMonthFn != nil {
		return m.findByContractAndMonthFn(ctx, contractID, month, bt)
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, month string) ([]Bill, error) {
	if m.findNonVoidedByContractMonthFn != nil {
		return m.findNonVoidedByContractMonthFn(ctx, contractID, month)
	}
	return nil, nil
}
func (m *mockBillingRepo) FindApartmentIDByRoomID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	if m.apartmentID != uuid.Nil {
		return m.apartmentID, nil
	}
	return uuid.Nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error) {
	if m.findActiveContractsByApartmentIDFn != nil {
		return m.findActiveContractsByApartmentIDFn(ctx, apartmentID)
	}
	return nil, nil
}
func (m *mockBillingRepo) FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error) {
	if m.findExistingByContractsAndMonthFn != nil {
		return m.findExistingByContractsAndMonthFn(ctx, contractIDs, month)
	}
	return map[uuid.UUID]*Bill{}, nil
}
func (m *mockBillingRepo) Create(ctx context.Context, bill *Bill) error {
	m.createdBill = bill
	if m.createFn != nil {
		return m.createFn(ctx, bill)
	}
	return nil
}
func (m *mockBillingRepo) Update(ctx context.Context, bill *Bill) error {
	m.updatedBills = append(m.updatedBills, bill)
	if m.updateFn != nil {
		return m.updateFn(ctx, bill)
	}
	return nil
}
func (m *mockBillingRepo) SumPaidByContractSince(ctx context.Context, contractID uuid.UUID, since string) (int64, error) {
	if m.sumPaidFn != nil {
		return m.sumPaidFn(ctx, contractID, since)
	}
	return 0, nil
}
func (m *mockBillingRepo) DeleteLineItemsBySource(_ context.Context, _ uuid.UUID, _ LineItemSource) error {
	return nil
}
func (m *mockBillingRepo) CreateLineItems(_ context.Context, _ []BillLineItem) error {
	return nil
}
func (m *mockBillingRepo) CreateBatch(_ context.Context, batch *BillGenerationBatch, items []BillGenerationBatchItem) error {
	m.createdBatch = batch
	m.createdBatchItems = items
	return nil
}
func (m *mockBillingRepo) FindBatchByID(_ context.Context, _ uuid.UUID) (*BillGenerationBatch, error) {
	if m.createdBatch != nil {
		return m.createdBatch, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) FindBatchItemsByBatchID(_ context.Context, _ uuid.UUID) ([]BatchItemWithTenant, error) {
	result := make([]BatchItemWithTenant, len(m.createdBatchItems))
	for i, it := range m.createdBatchItems {
		result[i] = BatchItemWithTenant{BillGenerationBatchItem: it}
	}
	return result, nil
}
func (m *mockBillingRepo) ListBatches(_ context.Context, _ BatchListParams) ([]BillGenerationBatch, int64, error) {
	return nil, 0, nil
}

// --- Commit flow mocks ---

func (m *mockBillingRepo) LockBatchForCommit(_ context.Context, _ uuid.UUID) (*BillGenerationBatch, error) {
	if m.createdBatch != nil {
		return m.createdBatch, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) ListCommitPendingItems(_ context.Context, _ uuid.UUID) ([]BillGenerationBatchItem, error) {
	var pending []BillGenerationBatchItem
	for _, it := range m.createdBatchItems {
		if it.ResultType == ResultCreated && it.BillID == nil {
			pending = append(pending, it)
		}
	}
	return pending, nil
}
func (m *mockBillingRepo) UpdateBatchItemCommitted(_ context.Context, itemID uuid.UUID, billID uuid.UUID) error {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			m.createdBatchItems[i].BillID = &billID
			break
		}
	}
	return nil
}
func (m *mockBillingRepo) UpdateBatchItemCommitError(_ context.Context, itemID uuid.UUID, reasonText string) error {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			m.createdBatchItems[i].ReasonCode = ReasonCodeCommitError
			m.createdBatchItems[i].ReasonText = reasonText
			break
		}
	}
	return nil
}
func (m *mockBillingRepo) UpdateBatchCommitStatus(_ context.Context, _ uuid.UUID, status CommitStatus, committedAt *time.Time) error {
	if m.createdBatch != nil {
		m.createdBatch.CommitStatus = &status
		m.createdBatch.CommittedAt = committedAt
	}
	return nil
}

type mockContractQuerier struct {
	contract *contract.Contract
}

var _ ContractQuerier = (*mockContractQuerier)(nil)

func (m *mockContractQuerier) FindByIDSimple(_ context.Context, _ uuid.UUID) (*contract.Contract, error) {
	if m.contract != nil {
		return m.contract, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type mockMeterQuerier struct {
	reading                      *meterreading.MeterReading
	findByIDSimpleFn             func(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error)
	findMonthlyByRoomsAndMonthFn func(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
}

var _ MeterReadingQuerier = (*mockMeterQuerier)(nil)

func (m *mockMeterQuerier) FindByIDSimple(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error) {
	if m.findByIDSimpleFn != nil {
		return m.findByIDSimpleFn(ctx, id)
	}
	if m.reading != nil {
		return m.reading, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterQuerier) FindLatestByRoomID(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) {
	if m.reading != nil {
		return m.reading, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterQuerier) FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error) {
	if m.findMonthlyByRoomsAndMonthFn != nil {
		return m.findMonthlyByRoomsAndMonthFn(ctx, roomIDs, month)
	}
	return map[uuid.UUID]*meterreading.MeterReading{}, nil
}

type mockConfigQuerier struct {
	configs []billingconfig.BillingConfig
}

var _ BillingConfigQuerier = (*mockConfigQuerier)(nil)

func (m *mockConfigQuerier) FindByApartmentID(_ context.Context, _ uuid.UUID) ([]billingconfig.BillingConfig, error) {
	return m.configs, nil
}

type mockMoveOutQuerier struct {
	notice                        *moveout.MoveOutNotice
	findRoomIDsWithPendingNoticeFn func(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

var _ MoveOutQuerier = (*mockMoveOutQuerier)(nil)

func (m *mockMoveOutQuerier) FindActiveByContractID(_ context.Context, _ uuid.UUID) (*moveout.MoveOutNotice, error) {
	if m.notice != nil {
		return m.notice, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMoveOutQuerier) FindRoomIDsWithPendingNotice(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if m.findRoomIDsWithPendingNoticeFn != nil {
		return m.findRoomIDsWithPendingNoticeFn(ctx, roomIDs)
	}
	return map[uuid.UUID]bool{}, nil
}

type mockTxManager struct{}

func (m *mockTxManager) RunInTx(_ context.Context, fn func(ctx context.Context) error) error {
	return fn(context.Background())
}

// --- Test helpers ---

func testContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		MonthlyRent:            500000, // 5,000 baht
		DepositAmount:          1000000,
		ElectricityRatePerUnit: 800,  // 8 baht/unit
		WaterRatePerUnit:       1800, // 18 baht/unit
	}
}

func testMonthlyReading(roomID uuid.UUID, billingMonth string) *meterreading.MeterReading {
	bm := billingMonth
	return &meterreading.MeterReading{
		ID:                  uuid.New(),
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1150, // 150 units
		WaterPrevious:       100,
		WaterCurrent:        110, // 10 units
	}
}

func testExitReading(roomID uuid.UUID, moveOutDate time.Time) *meterreading.MeterReading {
	return &meterreading.MeterReading{
		ID:                  uuid.New(),
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeExit,
		ReadingDateActual:   &moveOutDate,
		ElectricityPrevious: 1150,
		ElectricityCurrent:  1200, // 50 units
		WaterPrevious:       110,
		WaterCurrent:        115, // 5 units
	}
}

func completedNotice(contractID uuid.UUID, moveOutDate time.Time) *moveout.MoveOutNotice {
	return &moveout.MoveOutNotice{
		ID:                uuid.New(),
		ContractID:        contractID,
		Status:            moveout.MoveOutStatusCompleted,
		NoticeDate:        moveOutDate.AddDate(0, -1, 0),
		ScheduledMoveOutDate: moveOutDate,
	}
}

func newSvc(repo *mockBillingRepo, contracts *mockContractQuerier, meters *mockMeterQuerier, configs *mockConfigQuerier, moveOuts *mockMoveOutQuerier) BillingService {
	return NewBillingService(repo, contracts, meters, configs, moveOuts, &mockTxManager{})
}

// ============================================================
// Monthly Bill Tests
// ============================================================

func TestCreateMonthlyBill_HappyPath(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-03")
	repo := &mockBillingRepo{}

	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	bill, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: reading.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.BillType != BillTypeMonthly {
		t.Fatalf("expected MONTHLY, got %s", bill.BillType)
	}
	if bill.Status != BillStatusFinalized {
		t.Fatalf("expected FINALIZED, got %s", bill.Status)
	}

	created := repo.createdBill
	if len(created.LineItems) != 3 {
		t.Fatalf("expected 3 line items, got %d", len(created.LineItems))
	}
	if created.LineItems[0].Amount != 500000 {
		t.Errorf("room rent = %d, want 500000", created.LineItems[0].Amount)
	}
	if created.LineItems[1].Amount != 120000 {
		t.Errorf("electricity = %d, want 120000", created.LineItems[1].Amount)
	}
	if created.LineItems[2].Amount != 18000 {
		t.Errorf("water = %d, want 18000", created.LineItems[2].Amount)
	}
	if created.TotalAmount != 638000 {
		t.Fatalf("total = %d, want 638000", created.TotalAmount)
	}
}

func TestCreateMonthlyBill_ContractNotActive(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: uuid.New().String(),
	})
	if err != ErrContractNotActive {
		t.Fatalf("expected ErrContractNotActive, got %v", err)
	}
}

func TestCreateMonthlyBill_DuplicateBill(t *testing.T) {
	c := testContract()

	svc := newSvc(
		&mockBillingRepo{findByContractAndMonthFn: func(_ context.Context, _ uuid.UUID, _ string, _ BillType) (*Bill, error) {
			return &Bill{ID: uuid.New()}, nil
		}},
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: uuid.New().String(),
	})
	if err != ErrBillAlreadyExists {
		t.Fatalf("expected ErrBillAlreadyExists, got %v", err)
	}
}

func TestCreateMonthlyBill_MeterTypeMismatch(t *testing.T) {
	c := testContract()
	exitReading := testExitReading(c.RoomID, time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: exitReading.ID.String(),
	})
	if err != ErrMeterTypeMismatch {
		t.Fatalf("expected ErrMeterTypeMismatch, got %v", err)
	}
}

func TestCreateMonthlyBill_MeterRoomMismatch(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(uuid.New(), "2026-03") // different room

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: reading.ID.String(),
	})
	if err != ErrMeterRoomMismatch {
		t.Fatalf("expected ErrMeterRoomMismatch, got %v", err)
	}
}

func TestCreateMonthlyBill_MeterMonthMismatch(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-02") // Feb reading for March bill

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-03", MeterReadingID: reading.ID.String(),
	})
	if err != ErrMeterMonthMismatch {
		t.Fatalf("expected ErrMeterMonthMismatch, got %v", err)
	}
}

// ============================================================
// Settlement Bill Tests
// ============================================================

func TestCreateSettlementBill_HappyPath_CrossMonth(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOutDate)

	repo := &mockBillingRepo{apartmentID: uuid.New()}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeCleaningFee, DefaultAmount: 50000, IsActive: true},
			{FeeType: billingconfig.FeeTypeKeyService, DefaultAmount: 20000, IsActive: true},
		}},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	bill, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.BillType != BillTypeSettlement {
		t.Fatalf("expected SETTLEMENT, got %s", bill.BillType)
	}

	created := repo.createdBill
	if created.BillingMonth != "2026-04" {
		t.Fatalf("billing_month = %s, want 2026-04 (ExitMonth)", created.BillingMonth)
	}
	if created.DepositAmount != c.DepositAmount {
		t.Fatalf("deposit = %d, want %d", created.DepositAmount, c.DepositAmount)
	}

	types := lineItemTypes(created.LineItems)
	expectTypes(t, types, LineItemProrateRent, LineItemElectricity, LineItemWater, LineItemCleaningFee, LineItemKeyService)

	prorate := findLineByType(created.LineItems, LineItemProrateRent)
	if prorate.Amount != 250000 {
		t.Errorf("prorate = %d, want 250000 (15/30 × 500000)", prorate.Amount)
	}
}

func TestCreateSettlementBill_SameMonthExit_NoProrate(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOutDate)

	repo := &mockBillingRepo{apartmentID: uuid.New()}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := repo.createdBill
	types := lineItemTypes(created.LineItems)
	if types[LineItemProrateRent] {
		t.Error("should NOT have PRORATE_RENT when move-out on last day of month")
	}
	expectTypes(t, types, LineItemElectricity, LineItemWater)
}

func TestCreateSettlementBill_WithPrepaidCredit(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOutDate)

	repo := &mockBillingRepo{
		apartmentID: uuid.New(),
		sumPaidFn: func(_ context.Context, _ uuid.UUID, _ string) (int64, error) {
			return 638000, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := repo.createdBill
	types := lineItemTypes(created.LineItems)
	if !types[LineItemPrepaidCredit] {
		t.Fatal("missing PREPAID_CREDIT line item")
	}

	credit := findLineByType(created.LineItems, LineItemPrepaidCredit)
	if credit.Amount != -638000 {
		t.Errorf("prepaid credit = %d, want -638000", credit.Amount)
	}
}

func TestCreateSettlementBill_RejectsWhenOnlyPending(t *testing.T) {
	c := testContract()

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{},
		&mockMoveOutQuerier{notice: &moveout.MoveOutNotice{
			ID:                uuid.New(),
			ContractID:        c.ID,
			Status:            moveout.MoveOutStatusPendingMeter,
			ScheduledMoveOutDate: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		}})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != ErrMoveOutNotCompleted {
		t.Fatalf("expected ErrMoveOutNotCompleted, got %v", err)
	}
}

func TestCreateSettlementBill_NoMoveOutNotice(t *testing.T) {
	c := testContract()

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != ErrMoveOutNotFound {
		t.Fatalf("expected ErrMoveOutNotFound, got %v", err)
	}
}

func TestCreateSettlementBill_NoExitReading(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != ErrExitReadingMissing {
		t.Fatalf("expected ErrExitReadingMissing, got %v", err)
	}
}

func TestCreateSettlementBill_LatestIsMonthlyNotExit(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	monthlyReading := testMonthlyReading(c.RoomID, "2026-03")

	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: monthlyReading}, &mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != ErrExitReadingMissing {
		t.Fatalf("expected ErrExitReadingMissing, got %v", err)
	}
}

func TestCreateSettlementBill_DuplicateGuard(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	svc := newSvc(
		&mockBillingRepo{findByContractAndMonthFn: func(_ context.Context, _ uuid.UUID, _ string, bt BillType) (*Bill, error) {
			if bt == BillTypeSettlement {
				return &Bill{ID: uuid.New()}, nil
			}
			return nil, gorm.ErrRecordNotFound
		}},
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: testExitReading(c.RoomID, moveOutDate)},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != ErrBillAlreadyExists {
		t.Fatalf("expected ErrBillAlreadyExists, got %v", err)
	}
}

// ============================================================
// Monthly → Settlement replacement
// ============================================================

func TestCreateSettlementBill_VoidsExistingDraftMonthly(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	draftBill := Bill{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusDraft}

	repo := &mockBillingRepo{
		apartmentID: uuid.New(),
		findNonVoidedByContractMonthFn: func(_ context.Context, _ uuid.UUID, _ string) ([]Bill, error) {
			return []Bill{draftBill}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: testExitReading(c.RoomID, moveOutDate)},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repo.updatedBills) == 0 {
		t.Fatal("expected DRAFT bill to be voided via Update")
	}
	voided := repo.updatedBills[0]
	if voided.Status != BillStatusVoid {
		t.Fatalf("expected voided DRAFT, got status %s", voided.Status)
	}
	if voided.VoidReason == nil || *voided.VoidReason != "REPLACED_BY_SETTLEMENT" {
		t.Fatal("void reason should be REPLACED_BY_SETTLEMENT")
	}
}

func TestCreateSettlementBill_VoidsFinalizedMonthly(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	finalizedBill := Bill{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusFinalized}

	repo := &mockBillingRepo{
		apartmentID: uuid.New(),
		findNonVoidedByContractMonthFn: func(_ context.Context, _ uuid.UUID, _ string) ([]Bill, error) {
			return []Bill{finalizedBill}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: testExitReading(c.RoomID, moveOutDate)},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repo.updatedBills) == 0 {
		t.Fatal("expected FINALIZED bill to be voided")
	}
	if repo.updatedBills[0].Status != BillStatusVoid {
		t.Fatalf("expected VOID, got %s", repo.updatedBills[0].Status)
	}
}

func TestCreateSettlementBill_KeepsPaidMonthlyAsPrepaidCredit(t *testing.T) {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	paidBill := Bill{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusPaid, TotalAmount: 638000}

	repo := &mockBillingRepo{
		apartmentID: uuid.New(),
		findNonVoidedByContractMonthFn: func(_ context.Context, _ uuid.UUID, _ string) ([]Bill, error) {
			return []Bill{paidBill}, nil
		},
		sumPaidFn: func(_ context.Context, _ uuid.UUID, _ string) (int64, error) {
			return 638000, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: testExitReading(c.RoomID, moveOutDate)},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOutDate)})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: c.ID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, ub := range repo.updatedBills {
		if ub.ID == paidBill.ID && ub.Status == BillStatusVoid {
			t.Fatal("PAID bill should NOT be voided")
		}
	}

	created := repo.createdBill
	credit := findLineByType(created.LineItems, LineItemPrepaidCredit)
	if credit.Amount != -638000 {
		t.Errorf("prepaid credit = %d, want -638000", credit.Amount)
	}
}

// ============================================================
// State transition tests (service layer)
// ============================================================

func TestFinalizeBill_HappyPath(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusDraft, LineItems: []BillLineItem{{Amount: 100}}}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	bill, err := svc.FinalizeBill(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.Status != BillStatusFinalized {
		t.Fatalf("expected FINALIZED, got %s", bill.Status)
	}
}

func TestFinalizeBill_DraftToPaidRejected(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusDraft}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.MarkPaid(context.Background(), billID)
	if err == nil {
		t.Fatal("expected error when marking DRAFT as paid")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T", err)
	}
}

func TestVoidBill_HappyPath(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusFinalized}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	bill, err := svc.VoidBill(context.Background(), billID, VoidBillRequest{Reason: "ข้อมูลผิด"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.Status != BillStatusVoid {
		t.Fatalf("expected VOID, got %s", bill.Status)
	}
}

func TestVoidBill_PaidToVoidRejected(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusPaid}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.VoidBill(context.Background(), billID, VoidBillRequest{Reason: "test"})
	if err == nil {
		t.Fatal("expected error when voiding PAID bill")
	}
}

func TestMarkPaid_HappyPath(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusFinalized}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	bill, err := svc.MarkPaid(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.Status != BillStatusPaid {
		t.Fatalf("expected PAID, got %s", bill.Status)
	}
}

// ============================================================
// Helper function tests
// ============================================================

func TestAdvanceMonth(t *testing.T) {
	tests := []struct{ input, expected string }{
		{"2026-01", "2026-02"},
		{"2026-12", "2027-01"},
		{"2026-06", "2026-07"},
	}
	for _, tt := range tests {
		if got := advanceMonth(tt.input); got != tt.expected {
			t.Errorf("advanceMonth(%s) = %s, want %s", tt.input, got, tt.expected)
		}
	}
}

func TestDaysInMonth(t *testing.T) {
	tests := []struct {
		date     time.Time
		expected int
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 31},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 28},
		{time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), 29},
		{time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 30},
	}
	for _, tt := range tests {
		if got := daysInMonth(tt.date); got != tt.expected {
			t.Errorf("daysInMonth(%v) = %d, want %d", tt.date, got, tt.expected)
		}
	}
}

// --- Test utilities ---

func lineItemTypes(items []BillLineItem) map[LineItemType]bool {
	m := make(map[LineItemType]bool)
	for _, li := range items {
		m[li.LineType] = true
	}
	return m
}

func expectTypes(t *testing.T, types map[LineItemType]bool, expected ...LineItemType) {
	t.Helper()
	for _, e := range expected {
		if !types[e] {
			t.Errorf("missing line item type %s", e)
		}
	}
}

func findLineByType(items []BillLineItem, lt LineItemType) BillLineItem {
	for _, li := range items {
		if li.LineType == lt {
			return li
		}
	}
	return BillLineItem{}
}

// ============================================================
// Batch Monthly Billing Tests
// ============================================================

func testContractWithRoom(floor int, roomNum string) (ContractWithRoom, *contract.Contract) {
	c := testContract()
	c.StartDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return ContractWithRoom{
		ContractID:             c.ID,
		RoomID:                 c.RoomID,
		RoomNumber:             roomNum,
		RoomFloor:              floor,
		StartDate:              c.StartDate,
		MonthlyRent:            c.MonthlyRent,
		ElectricityRatePerUnit: c.ElectricityRatePerUnit,
		WaterRatePerUnit:       c.WaterRatePerUnit,
	}, c
}

func batchSvc(repo *mockBillingRepo, meters *mockMeterQuerier, moveOuts *mockMoveOutQuerier) BillingService {
	return NewBillingService(repo, &mockContractQuerier{}, meters, &mockConfigQuerier{}, moveOuts, &mockTxManager{})
}

// runBatch invokes the service and returns (batch header, items captured by the mock repo).
func runBatch(t *testing.T, svc BillingService, repo *mockBillingRepo, month string) (*BillGenerationBatch, []BillGenerationBatchItem) {
	t.Helper()
	result, err := svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: uuid.New().String(), BillingMonth: month,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return result, repo.createdBatchItems
}

func TestBatchCreateMonthlyBills_HappyPath(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101")
	cwr2, c2 := testContractWithRoom(2, "201")
	cwr3, c3 := testContractWithRoom(3, "301")

	r1 := testMonthlyReading(c1.RoomID, "2026-03")
	r2 := testMonthlyReading(c2.RoomID, "2026-03")
	r3 := testMonthlyReading(c3.RoomID, "2026-03")

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: r1, c2.RoomID: r2, c3.RoomID: r3,
			}, nil
		},
	}

	svc := batchSvc(repo, meters, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.TotalContracts != 3 {
		t.Fatalf("total = %d, want 3", batch.TotalContracts)
	}
	if batch.CreatedCount != 3 {
		t.Fatalf("created = %d, want 3", batch.CreatedCount)
	}
	if batch.Status != BatchStatusCompleted {
		t.Errorf("status = %s, want COMPLETED", batch.Status)
	}
	for _, d := range items {
		if d.ResultType != ResultCreated {
			t.Errorf("room %s: expected CREATED, got %s", d.RoomNumber, d.ResultType)
		}
		if d.BillID != nil {
			t.Errorf("room %s: bill_id should be nil (compute-only)", d.RoomNumber)
		}
		if len(d.ComputedSnapshot.LineItems) != 3 {
			t.Errorf("room %s: expected 3 snapshot line items, got %d",
				d.RoomNumber, len(d.ComputedSnapshot.LineItems))
		}
		if d.ComputedSnapshot.TotalAmount != 638000 {
			t.Errorf("room %s: snapshot total = %d, want 638000",
				d.RoomNumber, d.ComputedSnapshot.TotalAmount)
		}
		if d.ComputedSnapshot.Version != ComputedSnapshotVersion {
			t.Errorf("room %s: snapshot version = %d, want %d",
				d.RoomNumber, d.ComputedSnapshot.Version, ComputedSnapshotVersion)
		}
	}
	if repo.createdBill != nil {
		t.Error("batch should not persist any bill row (compute-only)")
	}
}

func TestBatchCreateMonthlyBills_MixedResults(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101") // will have meter → created
	cwr2, c2 := testContractWithRoom(2, "201") // existing bill
	cwr3, _ := testContractWithRoom(3, "301")  // no meter → skipped

	r1 := testMonthlyReading(c1.RoomID, "2026-03")
	existingBillID := uuid.New()

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*Bill, error) {
			return map[uuid.UUID]*Bill{
				c2.ID: {ID: existingBillID},
			}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: r1,
				c2.RoomID: testMonthlyReading(c2.RoomID, "2026-03"),
			}, nil
		},
	}

	svc := batchSvc(repo, meters, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.CreatedCount != 1 {
		t.Errorf("created = %d, want 1", batch.CreatedCount)
	}
	if batch.AlreadyExistsCount != 1 {
		t.Errorf("already_exists = %d, want 1", batch.AlreadyExistsCount)
	}
	if batch.SkippedCount != 1 {
		t.Errorf("skipped = %d, want 1", batch.SkippedCount)
	}
	if batch.Status != BatchStatusPartial {
		t.Errorf("status = %s, want PARTIAL", batch.Status)
	}
	for _, d := range items {
		if d.ResultType == ResultAlreadyExists && (d.BillID == nil || *d.BillID != existingBillID) {
			t.Error("already_exists result should have correct bill_id")
		}
		if d.ResultType == ResultSkipped && d.ReasonCode != ReasonMissingMeterReading {
			t.Errorf("skipped reason = %s, want MISSING_METER_READING", d.ReasonCode)
		}
	}
}

func TestBatchCreateMonthlyBills_MoveOutPending(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr}, nil
		},
	}

	moveOuts := &mockMoveOutQuerier{
		findRoomIDsWithPendingNoticeFn: func(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]bool, error) {
			return map[uuid.UUID]bool{cwr.RoomID: true}, nil
		},
	}

	svc := batchSvc(repo, &mockMeterQuerier{}, moveOuts)
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != ReasonMoveOutPending {
		t.Errorf("reason = %s, want MOVE_OUT_PENDING", items[0].ReasonCode)
	}
}

func TestBatchCreateMonthlyBills_NotBillable_StartAfterMonth(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")
	cwr.StartDate = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // starts May, billing March

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr}, nil
		},
	}

	svc := batchSvc(repo, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != ReasonNotBillable {
		t.Errorf("reason = %s, want NOT_BILLABLE", items[0].ReasonCode)
	}
}

func TestBatchCreateMonthlyBills_NotBillable_EndedBeforeMonth(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")
	endDate := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) // ended Feb, billing March
	cwr.EndDate = &endDate

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr}, nil
		},
	}

	svc := batchSvc(repo, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != ReasonNotBillable {
		t.Errorf("reason = %s, want NOT_BILLABLE", items[0].ReasonCode)
	}
}

func TestBatchCreateMonthlyBills_EmptyApartment(t *testing.T) {
	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return nil, nil
		},
	}

	svc := batchSvc(repo, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, _ := runBatch(t, svc, repo, "2026-03")

	if batch.TotalContracts != 0 {
		t.Fatalf("total = %d, want 0", batch.TotalContracts)
	}
}

func TestBatchCreateMonthlyBills_InvalidInput(t *testing.T) {
	svc := batchSvc(&mockBillingRepo{}, &mockMeterQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: "bad-uuid", BillingMonth: "2026-03",
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}

	_, err = svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: uuid.New().String(), BillingMonth: "2026-1",
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid month format")
	}
}

func TestBatchCreateMonthlyBills_DeterministicOrdering(t *testing.T) {
	cwr3, _ := testContractWithRoom(3, "301")
	cwr1, _ := testContractWithRoom(1, "101")
	cwr2, _ := testContractWithRoom(2, "201")

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			// repo returns sorted by floor, room_number
			return []ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
	}

	svc := batchSvc(repo, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	_, items := runBatch(t, svc, repo, "2026-03")

	if len(items) != 3 {
		t.Fatalf("items count = %d, want 3", len(items))
	}
	if items[0].RoomNumber != "101" || items[1].RoomNumber != "201" || items[2].RoomNumber != "301" {
		t.Errorf("order = %s,%s,%s — want 101,201,301",
			items[0].RoomNumber, items[1].RoomNumber, items[2].RoomNumber)
	}
}

func TestBatchCreateMonthlyBills_IdempotentRerun(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101")
	cwr2, c2 := testContractWithRoom(2, "201")
	bill1ID, bill2ID := uuid.New(), uuid.New()

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr1, cwr2}, nil
		},
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*Bill, error) {
			return map[uuid.UUID]*Bill{
				c1.ID: {ID: bill1ID},
				c2.ID: {ID: bill2ID},
			}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: testMonthlyReading(c1.RoomID, "2026-03"),
				c2.RoomID: testMonthlyReading(c2.RoomID, "2026-03"),
			}, nil
		},
	}

	svc := batchSvc(repo, meters, &mockMoveOutQuerier{})
	batch, _ := runBatch(t, svc, repo, "2026-03")

	if batch.CreatedCount != 0 {
		t.Errorf("created = %d, want 0 (idempotent)", batch.CreatedCount)
	}
	if batch.AlreadyExistsCount != 2 {
		t.Errorf("already_exists = %d, want 2", batch.AlreadyExistsCount)
	}
	if batch.Status != BatchStatusCompleted {
		t.Errorf("status = %s, want COMPLETED", batch.Status)
	}
}

// ============================================================
// Domain: ComputeStatus mapping
// ============================================================

func TestBatch_ComputeStatus(t *testing.T) {
	tests := []struct {
		name                                           string
		created, alreadyExists, skipped, failed        int
		want                                           BatchStatus
	}{
		{"empty apartment", 0, 0, 0, 0, BatchStatusCompleted},
		{"all created", 3, 0, 0, 0, BatchStatusCompleted},
		{"existing counts as success", 0, 3, 0, 0, BatchStatusCompleted},
		{"some failed with some created", 2, 0, 0, 1, BatchStatusPartial},
		{"some skipped with some created", 2, 0, 1, 0, BatchStatusPartial},
		{"all failed", 0, 0, 0, 3, BatchStatusFailed},
		{"all skipped no failed", 0, 0, 3, 0, BatchStatusPartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BillGenerationBatch{
				CreatedCount:       tt.created,
				AlreadyExistsCount: tt.alreadyExists,
				SkippedCount:       tt.skipped,
				FailedCount:        tt.failed,
			}
			b.ComputeStatus()
			if b.Status != tt.want {
				t.Errorf("got %s, want %s", b.Status, tt.want)
			}
		})
	}
}

// --- Deposit eligibility ---

func TestIsDepositReturnable(t *testing.T) {
	start := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		moveOut   time.Time
		minMonths int
		want      bool
	}{
		// Exact boundary: start=Jan 20, min=6 → must reach Jul 20
		{"exact day — eligible", time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), 6, true},
		{"one day before — not eligible", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), 6, false},
		{"one day after — eligible", time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), 6, true},

		// Well before / well after
		{"3 months — not eligible", time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), 6, false},
		{"12 months — eligible", time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 6, true},

		// minMonths = 0 → always eligible
		{"zero min — same day eligible", start, 0, true},

		// Same day = not eligible (0 months stayed, need 6)
		{"same day — not eligible", start, 6, false},

		// End-of-month tested separately below (different start date)

		// moveOut before start = not eligible (data anomaly guard)
		{"moveOut before start — not eligible", time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), 6, false},

		// Cross-year
		{"cross year — eligible", time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 12, true},
		{"cross year — not eligible", time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC), 12, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDepositReturnable(start, tt.moveOut, tt.minMonths)
			if got != tt.want {
				t.Errorf("isDepositReturnable(%s, %s, %d) = %v, want %v",
					start.Format("2006-01-02"), tt.moveOut.Format("2006-01-02"), tt.minMonths, got, tt.want)
			}
		})
	}
}

func TestIsDepositReturnable_EndOfMonth(t *testing.T) {
	// Jan 31 + 1 month = Feb 28 (clamped, not Mar 3)
	t.Run("Jan31+1m: Feb 27 — not eligible", func(t *testing.T) {
		start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
	t.Run("Jan31+1m: Feb 28 — eligible", func(t *testing.T) {
		start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	// Aug 31 + 1 month = Sep 30
	t.Run("Aug31+1m: Sep 29 — not eligible", func(t *testing.T) {
		start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
	t.Run("Aug31+1m: Sep 30 — eligible", func(t *testing.T) {
		start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	// Leap year: Jan 31 + 1 month = Feb 29 in 2024
	t.Run("Jan31+1m leap year: Feb 29 — eligible", func(t *testing.T) {
		start := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	t.Run("Jan31+1m leap year: Feb 28 — not eligible", func(t *testing.T) {
		start := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2024, 2, 28, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
}

func TestEffectiveDeposit(t *testing.T) {
	t.Run("eligible — returns full deposit", func(t *testing.T) {
		c := &contract.Contract{
			StartDate:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			MinMonths:     6,
			DepositAmount: 300000, // ฿3,000
		}
		moveOut := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		if got := effectiveDeposit(c, moveOut); got != 300000 {
			t.Errorf("got %d, want 300000", got)
		}
	})

	t.Run("not eligible — returns zero", func(t *testing.T) {
		c := &contract.Contract{
			StartDate:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			MinMonths:     6,
			DepositAmount: 300000,
		}
		moveOut := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		if got := effectiveDeposit(c, moveOut); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}
