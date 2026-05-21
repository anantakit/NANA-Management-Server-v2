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
	findAllFn                           func(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	listBillsByBatchIDFn                func(batchID uuid.UUID) ([]Bill, error)
	updateErrByBillID                   map[uuid.UUID]error // per-bill Update failure injection
	findByIDFn                          func(ctx context.Context, id uuid.UUID) (*Bill, error)
	findByIDWithRelationsFn             func(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	findByContractAndMonthFn            func(ctx context.Context, contractID uuid.UUID, month string, bt BillType) (*Bill, error)
	findNonVoidedByContractMonthFn      func(ctx context.Context, contractID uuid.UUID, month string) ([]Bill, error)
	findActiveContractsByApartmentIDFn  func(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error)
	findExistingByContractsAndMonthFn   func(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error)
	sumPaidFn                           func(ctx context.Context, contractID uuid.UUID, since string) (int64, error)
	hasPaidAdvanceRentFn                func(ctx context.Context, contractID uuid.UUID, month string) (bool, error)
	findUnpaidMonthlyFn                 func(ctx context.Context, contractID uuid.UUID) ([]Bill, error)
	findAbsorbedFn                      func(ctx context.Context, contractID uuid.UUID) ([]Bill, error)
	createFn                            func(ctx context.Context, bill *Bill) error
	updateFn                            func(ctx context.Context, bill *Bill) error
	apartmentID                         uuid.UUID
	findApartmentIDNotFound             bool // explicit opt-in for "room not found" path

	createdBill  *Bill   // last bill created (kept for legacy single-bill tests)
	createdBills []*Bill // every bill created in order (used by batch commit tests)
	updatedBills []*Bill

	// Trackers for line-item mutation tests (UpdateMonthlyDraft / UpdateSettlementDraft).
	deletedSourcesByBillID map[uuid.UUID][]LineItemSource
	createdLineItems       []BillLineItem

	createdBatch      *BillGenerationBatch
	createdBatchItems []BillGenerationBatchItem
}

// defaultMockApartmentID is returned by FindApartmentIDByRoomID when neither
// `apartmentID` nor `findApartmentIDNotFound` is set on the mock. addConfigFees
// now treats ErrRecordNotFound as an invariant violation, so the default has
// to be a valid UUID — tests that don't care about the apartment lookup just
// pass through unaffected.
var defaultMockApartmentID = uuid.MustParse("00000000-0000-0000-0000-000000000aaa")

var _ BillingRepository = (*mockBillingRepo)(nil)

func (m *mockBillingRepo) FindAll(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error) {
	if m.findAllFn != nil {
		return m.findAllFn(ctx, params)
	}
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
// match จาก updatedBills ย้อนหลังก่อน (post-mutation state) แล้วค่อย fallback ไป FindByID.
// Tests that need control over the relation fields (ApartmentID, names) can
// set `findByIDWithRelationsFn` explicitly.
func (m *mockBillingRepo) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	if m.findByIDWithRelationsFn != nil {
		return m.findByIDWithRelationsFn(ctx, id)
	}
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
	if m.findApartmentIDNotFound {
		return uuid.Nil, gorm.ErrRecordNotFound
	}
	// Default: return a stable non-nil UUID. addConfigFees treats
	// ErrRecordNotFound as an invariant violation, so most tests that don't
	// care about the apartment lookup just need *some* valid UUID.
	return defaultMockApartmentID, nil
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
	m.createdBills = append(m.createdBills, bill)
	if m.createFn != nil {
		return m.createFn(ctx, bill)
	}
	return nil
}
func (m *mockBillingRepo) Update(ctx context.Context, bill *Bill) error {
	if err, ok := m.updateErrByBillID[bill.ID]; ok && err != nil {
		return err
	}
	m.updatedBills = append(m.updatedBills, bill)
	if m.updateFn != nil {
		return m.updateFn(ctx, bill)
	}
	return nil
}
func (m *mockBillingRepo) ListBillsByBatchID(_ context.Context, batchID uuid.UUID) ([]Bill, error) {
	if m.listBillsByBatchIDFn != nil {
		return m.listBillsByBatchIDFn(batchID)
	}
	return nil, nil
}
func (m *mockBillingRepo) SumPaidByContractSince(ctx context.Context, contractID uuid.UUID, since string) (int64, error) {
	if m.sumPaidFn != nil {
		return m.sumPaidFn(ctx, contractID, since)
	}
	return 0, nil
}
func (m *mockBillingRepo) HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, month string) (bool, error) {
	if m.hasPaidAdvanceRentFn != nil {
		return m.hasPaidAdvanceRentFn(ctx, contractID, month)
	}
	return false, nil
}
func (m *mockBillingRepo) FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	if m.findUnpaidMonthlyFn != nil {
		return m.findUnpaidMonthlyFn(ctx, contractID)
	}
	return nil, nil
}
func (m *mockBillingRepo) FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	if m.findAbsorbedFn != nil {
		return m.findAbsorbedFn(ctx, contractID)
	}
	return nil, nil
}
func (m *mockBillingRepo) DeleteLineItemsBySource(_ context.Context, billID uuid.UUID, source LineItemSource) error {
	if m.deletedSourcesByBillID == nil {
		m.deletedSourcesByBillID = map[uuid.UUID][]LineItemSource{}
	}
	m.deletedSourcesByBillID[billID] = append(m.deletedSourcesByBillID[billID], source)
	return nil
}
func (m *mockBillingRepo) CreateLineItems(_ context.Context, items []BillLineItem) error {
	m.createdLineItems = append(m.createdLineItems, items...)
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
	// configs is the list returned by FindByApartmentID. When nil
	// (zero value) the mock returns a sensible production default
	// (PRORATE_DAILY_RATE = ฿100/day) so unrelated tests don't have to
	// wire up the config to exercise the settlement bill path. Tests
	// that explicitly want "no config" can set `configs: []billingconfig.BillingConfig{}`.
	configs []billingconfig.BillingConfig
}

var _ BillingConfigQuerier = (*mockConfigQuerier)(nil)

func (m *mockConfigQuerier) FindByApartmentID(_ context.Context, _ uuid.UUID) ([]billingconfig.BillingConfig, error) {
	if m.configs == nil {
		return []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}, nil
	}
	return m.configs, nil
}

type mockMoveOutQuerier struct {
	notice                          *moveout.MoveOutNotice
	findRoomIDsWithMoveOutInMonthFn func(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
}

var _ MoveOutQuerier = (*mockMoveOutQuerier)(nil)

func (m *mockMoveOutQuerier) FindActiveByContractID(_ context.Context, _ uuid.UUID) (*moveout.MoveOutNotice, error) {
	if m.notice != nil {
		return m.notice, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMoveOutQuerier) FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error) {
	if m.findRoomIDsWithMoveOutInMonthFn != nil {
		return m.findRoomIDsWithMoveOutInMonthFn(ctx, roomIDs, billingMonth)
	}
	return map[uuid.UUID]bool{}, nil
}

type mockTxManager struct{}

func (m *mockTxManager) RunInTx(_ context.Context, fn func(ctx context.Context) error) error {
	return fn(context.Background())
}

// mockBillAuditRepo records every audit event in-memory so tests can assert
// on action / actor / payload without touching the DB. createErr lets tests
// simulate audit failure (which must roll back the parent TX). editedErr
// lets tests simulate audit-query failure on the is_edited read path —
// must propagate, never be silently swallowed.
//
// editedQueryCalls counts how many times EditedBillIDs is invoked so list
// tests can prove batching (one call per page, no N+1).
type mockBillAuditRepo struct {
	createErr        error
	createErrByBillID map[uuid.UUID]error // per-bill audit failure injection (BatchFinalizeAll tests)
	editedErr        error
	logs             []BillAuditLog
	editedQueryCalls int
}

var _ BillAuditRepository = (*mockBillAuditRepo)(nil)

func (m *mockBillAuditRepo) Create(_ context.Context, log *BillAuditLog) error {
	// Per-bill failure injection takes precedence so per-item-isolation
	// tests can fail bill A's audit while bill B/C continue cleanly.
	if err, ok := m.createErrByBillID[log.BillID]; ok && err != nil {
		return err
	}
	if m.createErr != nil {
		return m.createErr
	}
	m.logs = append(m.logs, *log)
	return nil
}

func (m *mockBillAuditRepo) EditedBillIDs(_ context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	m.editedQueryCalls++
	if m.editedErr != nil {
		return nil, m.editedErr
	}
	result := make(map[uuid.UUID]bool, len(billIDs))
	want := make(map[uuid.UUID]bool, len(billIDs))
	for _, id := range billIDs {
		want[id] = true
	}
	for _, log := range m.logs {
		if !log.Action.IsEditEvent() {
			continue
		}
		if want[log.BillID] {
			result[log.BillID] = true
		}
	}
	return result, nil
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
		ID:                   uuid.New(),
		ContractID:           contractID,
		Status:               moveout.MoveOutStatusCompleted,
		NoticeDate:           moveOutDate.AddDate(0, -1, 0),
		ScheduledMoveOutDate: moveOutDate,
		ActualMoveOutDate:    &moveOutDate,
	}
}

func newSvc(repo *mockBillingRepo, contracts *mockContractQuerier, meters *mockMeterQuerier, configs *mockConfigQuerier, moveOuts *mockMoveOutQuerier) BillingService {
	return NewBillingService(repo, &mockBillAuditRepo{}, contracts, meters, configs, moveOuts, &mockTxManager{})
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
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bill.BillType != BillTypeMonthly {
		t.Fatalf("expected MONTHLY, got %s", bill.BillType)
	}
	if bill.Status != BillStatusDraft {
		t.Fatalf("expected DRAFT, got %s (CreateMonthlyBill lands DRAFT-first per 2026-05-21 alignment with batch path)", bill.Status)
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
	}, nil)
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
	}, nil)
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
	}, nil)
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
	}, nil)
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
	}, nil)
	if err != ErrMeterMonthMismatch {
		t.Fatalf("expected ErrMeterMonthMismatch, got %v", err)
	}
}

// ============================================================
// Settlement Bill Tests — table-driven per production spec (TC01–TC25)
// ============================================================

// --- Settlement test helpers ---

// runSettlement creates a settlement bill with the given overrides and returns the created bill.
func runSettlement(t *testing.T, opts ...func(*settlementOpts)) *Bill {
	t.Helper()
	o := newSettlementOpts()
	for _, fn := range opts {
		fn(o)
	}
	// Sync meter + notice to moveOutDate
	o.repo.apartmentID = uuid.New()
	meters := &mockMeterQuerier{reading: testExitReading(o.contract.RoomID, o.moveOut)}
	notice := completedNotice(o.contract.ID, o.moveOut)

	svc := newSvc(o.repo, &mockContractQuerier{contract: o.contract},
		meters, o.configs, &mockMoveOutQuerier{notice: notice})

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: o.contract.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.repo.createdBill == nil {
		t.Fatal("no bill created")
	}
	return o.repo.createdBill
}

type settlementOpts struct {
	contract *contract.Contract
	moveOut  time.Time
	repo     *mockBillingRepo
	configs  *mockConfigQuerier
}

func newSettlementOpts() *settlementOpts {
	c := testContract()
	c.Status = contract.ContractStatusEnded
	c.StartDate = time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	c.MinMonths = 6
	return &settlementOpts{
		contract: c,
		moveOut:  time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC),
		repo:     &mockBillingRepo{},
		// Settlement bills with partial-month proration require a
		// PRORATE_DAILY_RATE config to exist for the apartment. Mirror
		// the production default (฿100/day = 10000 satang) here so each
		// test doesn't have to wire it up; tests that explicitly want to
		// exercise the missing-config error path can override `configs`.
		configs: &mockConfigQuerier{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}},
	}
}

func withMoveOut(y, m, d int) func(*settlementOpts) {
	return func(o *settlementOpts) { o.moveOut = time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
}

func withRentPaid(o *settlementOpts) {
	o.repo.hasPaidAdvanceRentFn = func(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
		return true, nil
	}
}

func withDeposit(amount int64) func(*settlementOpts) {
	return func(o *settlementOpts) { o.contract.DepositAmount = amount }
}

// ── Detect Paid Advance Rent Coverage ──
//
// ช่องโหว่ที่ใหญ่ที่สุดถ้าไม่มี test นี้:
// rent adjustment table inject rentPaid=true/false ตรง ๆ
// แต่ไม่ได้พิสูจน์ว่า service ส่ง moveOutMonth ไปหา repo ถูก
// และ branch ตาม boolean ถูก
//
// Service calls: repo.HasPaidAdvanceRentForMonth(ctx, contractID, moveOutMonth)
// Repo internally: checks PAID bill where billing_month = moveOutMonth - 1

func TestSettlement_DetectRentCoverage(t *testing.T) {
	tests := []struct {
		name        string
		moveOut     [3]int
		repoReturn  bool
		wantMonth   string // month service passes to repo
		wantPaid    bool
		wantProrate bool
	}{
		// A1 — previous month bill is PAID → detect true
		{"prev_month_paid", [3]int{2026, 4, 14}, true, "2026-04", true, false},
		// A2 — previous month bill NOT PAID → detect false
		{"prev_month_not_paid", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// A3 — other month paid, previous not → detect false (repo returns false)
		{"other_month_paid_prev_not", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// A4 — no previous month bill at all → detect false
		{"no_prev_month_bill", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// Boundary: January move-out → repo receives "2026-01", checks Dec 2025
		{"jan_moveout_checks_prev_year", [3]int{2026, 1, 15}, true, "2026-01", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedMonth string
			o := newSettlementOpts()
			o.moveOut = time.Date(tt.moveOut[0], time.Month(tt.moveOut[1]), tt.moveOut[2], 0, 0, 0, 0, time.UTC)
			o.repo.hasPaidAdvanceRentFn = func(_ context.Context, _ uuid.UUID, month string) (bool, error) {
				capturedMonth = month
				return tt.repoReturn, nil
			}
			o.repo.apartmentID = uuid.New()
			meters := &mockMeterQuerier{reading: testExitReading(o.contract.RoomID, o.moveOut)}
			notice := completedNotice(o.contract.ID, o.moveOut)
			svc := newSvc(o.repo, &mockContractQuerier{contract: o.contract},
				meters, o.configs, &mockMoveOutQuerier{notice: notice})

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			bill := o.repo.createdBill

			// Verify: service passes moveOutMonth to repo (repo does M-1 internally)
			if capturedMonth != tt.wantMonth {
				t.Errorf("month passed to repo = %q, want %q", capturedMonth, tt.wantMonth)
			}
			if bill.RentPaid != tt.wantPaid {
				t.Errorf("RentPaid = %v, want %v", bill.RentPaid, tt.wantPaid)
			}
			hasProrate := findLineByType(bill.LineItems, LineItemProrateRent).Amount > 0
			if hasProrate != tt.wantProrate {
				t.Errorf("hasProrate = %v, want %v", hasProrate, tt.wantProrate)
			}
		})
	}
}

// ── Rent Adjustment (TC01–04, TC16–17) ──

func TestSettlement_RentAdjustment(t *testing.T) {
	// Flat per-day rate from PRORATE_DAILY_RATE config (mock default).
	// 10000 satang = ฿100/day. Amount is exact: rate × days.
	const dailyRate = int64(10000)

	tests := []struct {
		name        string
		moveOut     [3]int // y, m, d
		rentPaid    bool
		wantProrate bool
		wantAmount  int64
		wantDays    int
	}{
		// TC01+11+12+22 — paid advance rent → 0 (no refund, no charge, no double)
		{"paid_mid_month", [3]int{2026, 4, 14}, true, false, 0, 0},
		// TC02+20 — not paid, mid-month → 14 days × rate
		{"not_paid_mid_month", [3]int{2026, 4, 14}, false, true, dailyRate * 14, 14},
		// TC03 — not paid, last day of 30-day month → rent = full month (day >= daysInMonth)
		{"not_paid_last_day_30", [3]int{2026, 4, 30}, false, false, 0, 0},
		// TC04 — not paid, first day (inclusive) → 1 day × rate
		{"not_paid_first_day", [3]int{2026, 4, 1}, false, true, dailyRate * 1, 1},
		// TC16 — leap year Feb 29 = last day → rent = full month
		{"leap_feb_29_full_month", [3]int{2024, 2, 29}, false, false, 0, 0},
		// TC17 — 31-day month, mid-month → 15 days × rate
		{"31day_month_mid", [3]int{2026, 5, 15}, false, true, dailyRate * 15, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []func(*settlementOpts){
				withMoveOut(tt.moveOut[0], tt.moveOut[1], tt.moveOut[2]),
			}
			if tt.rentPaid {
				opts = append(opts, withRentPaid)
			}
			bill := runSettlement(t, opts...)

			// RentPaid flag
			if bill.RentPaid != tt.rentPaid {
				t.Errorf("RentPaid = %v, want %v", bill.RentPaid, tt.rentPaid)
			}

			prorate := findLineByType(bill.LineItems, LineItemProrateRent)
			hasProrate := prorate.LineType == LineItemProrateRent && prorate.Amount > 0

			if hasProrate != tt.wantProrate {
				t.Errorf("hasProrate = %v, want %v", hasProrate, tt.wantProrate)
			}
			if tt.wantProrate {
				if prorate.Amount != tt.wantAmount {
					t.Errorf("prorate amount = %d, want %d", prorate.Amount, tt.wantAmount)
				}
				if prorate.Quantity != tt.wantDays {
					t.Errorf("prorate days = %d, want %d", prorate.Quantity, tt.wantDays)
				}
			}

			// Invariant: PREPAID_CREDIT must never appear (regression TC23)
			if lineItemTypes(bill.LineItems)[LineItemPrepaidCredit] {
				t.Error("PREPAID_CREDIT must never appear — old logic removed")
			}
			// Invariant: no negative rent line (regression TC11)
			for _, li := range bill.LineItems {
				if (li.LineType == LineItemProrateRent || li.LineType == LineItemRoomRent) && li.Amount < 0 {
					t.Errorf("negative rent line: %s = %d", li.LineType, li.Amount)
				}
			}
		})
	}
}

// ── Void Monthly Bills (TC05–07, TC15, TC25) ──

func TestSettlement_VoidMonthlyBills(t *testing.T) {
	tests := []struct {
		name           string
		existingBills  []Bill
		wantVoidIDs    []int // indices into existingBills that should be voided
		wantKeepIDs    []int // indices that must NOT be voided
	}{
		{
			"TC05_void_draft",
			[]Bill{{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusDraft}},
			[]int{0}, nil,
		},
		{
			"TC06_void_finalized",
			[]Bill{{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusFinalized}},
			[]int{0}, nil,
		},
		{
			"TC07_no_existing_bill",
			nil, nil, nil,
		},
		{
			"TC15_never_void_paid",
			[]Bill{{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusPaid, TotalAmount: 638000}},
			nil, []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills := tt.existingBills // capture

			o := newSettlementOpts()
			if len(bills) > 0 {
				o.repo.findNonVoidedByContractMonthFn = func(_ context.Context, _ uuid.UUID, _ string) ([]Bill, error) {
					return bills, nil
				}
			}
			o.repo.apartmentID = uuid.New()
			meters := &mockMeterQuerier{reading: testExitReading(o.contract.RoomID, o.moveOut)}
			notice := completedNotice(o.contract.ID, o.moveOut)
			svc := newSvc(o.repo, &mockContractQuerier{contract: o.contract},
				meters, o.configs, &mockMoveOutQuerier{notice: notice})

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			voidedIDs := map[uuid.UUID]bool{}
			for _, ub := range o.repo.updatedBills {
				if ub.Status == BillStatusVoid {
					voidedIDs[ub.ID] = true
					if ub.VoidReason == nil || *ub.VoidReason != "REPLACED_BY_SETTLEMENT" {
						t.Errorf("bill %s: void_reason should be REPLACED_BY_SETTLEMENT", ub.ID)
					}
				}
			}

			for _, idx := range tt.wantVoidIDs {
				if !voidedIDs[bills[idx].ID] {
					t.Errorf("bill[%d] (status=%s) should be voided", idx, bills[idx].Status)
				}
			}
			for _, idx := range tt.wantKeepIDs {
				if voidedIDs[bills[idx].ID] {
					t.Errorf("bill[%d] (status=%s) must NOT be voided", idx, bills[idx].Status)
				}
			}

			if len(bills) == 0 && len(o.repo.updatedBills) != 0 {
				t.Errorf("no existing bills → expected no void calls, got %d", len(o.repo.updatedBills))
			}
		})
	}
}

// ── Net Amount / Deposit (TC09–TC10) ──

func TestSettlement_NetAmount(t *testing.T) {
	// Compute exact charges for "rent paid + default exit meter" to craft zero case.
	// EXIT: electricity 50×800=40000, water 5×1800=9000 → total utility = 49000
	const utilityTotal = int64(50*800 + 5*1800) // 49000

	tests := []struct {
		name              string
		deposit           int64
		rentPaid          bool
		wantPositiveTotal bool  // total > 0
		wantRefund        bool  // depositBalance > 0
		wantZero          bool  // depositBalance == 0
	}{
		{"deposit_covers_all", 10000000, true, true, true, false},     // 100k >> utility → refund
		{"tenant_pays_extra", 0, false, true, false, false},            // no deposit → tenant owes
		{"exact_zero_balance", utilityTotal, true, true, false, true},  // deposit = charges → net 0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []func(*settlementOpts){withDeposit(tt.deposit)}
			if tt.rentPaid {
				opts = append(opts, withRentPaid)
			}
			bill := runSettlement(t, opts...)

			if tt.wantPositiveTotal && bill.TotalAmount <= 0 {
				t.Errorf("total = %d, want > 0", bill.TotalAmount)
			}
			if tt.wantZero {
				if bill.DepositBalance != 0 {
					t.Errorf("deposit_balance = %d, want exactly 0", bill.DepositBalance)
				}
			} else if tt.wantRefund {
				if bill.DepositBalance <= 0 {
					t.Errorf("deposit_balance = %d, want > 0 (refund)", bill.DepositBalance)
				}
			} else {
				if bill.DepositBalance > 0 {
					t.Errorf("deposit_balance = %d, want <= 0 (tenant owes)", bill.DepositBalance)
				}
			}
		})
	}
}

// ── Utility from EXIT Meter (TC08, TC14, TC24) ──

func TestSettlement_UtilityFromExitMeter(t *testing.T) {
	bill := runSettlement(t)

	// EXIT reading: electricity 1150→1200 = 50 units, water 110→115 = 5 units
	// Rate: 800 satang/elec unit, 1800 satang/water unit
	tests := []struct {
		name       string
		lineType   LineItemType
		wantQty    int
		wantAmount int64
	}{
		{"TC08_electricity", LineItemElectricity, 50, 50 * 800},
		{"TC08_water", LineItemWater, 5, 5 * 1800},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			li := findLineByType(bill.LineItems, tt.lineType)
			if li.Quantity != tt.wantQty {
				t.Errorf("quantity = %d, want %d", li.Quantity, tt.wantQty)
			}
			if li.Amount != tt.wantAmount {
				t.Errorf("amount = %d, want %d", li.Amount, tt.wantAmount)
			}
		})
	}

	// TC14+TC24 — exactly 1 of each, no monthly values leaked
	elecCount, waterCount := 0, 0
	for _, li := range bill.LineItems {
		switch li.LineType {
		case LineItemElectricity:
			elecCount++
		case LineItemWater:
			waterCount++
		}
	}
	if elecCount != 1 {
		t.Errorf("TC24: expected 1 electricity line, got %d", elecCount)
	}
	if waterCount != 1 {
		t.Errorf("TC24: expected 1 water line, got %d", waterCount)
	}
}

// ── Config Fees ──

func TestSettlement_ConfigFees(t *testing.T) {
	tests := []struct {
		name       string
		lineType   LineItemType
		wantAmount int64
	}{
		{"adds_cleaning_fee", LineItemCleaningFee, 50000},
		{"adds_key_service_fee", LineItemKeyService, 20000},
	}

	bill := runSettlement(t, func(o *settlementOpts) {
		o.configs = &mockConfigQuerier{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeCleaningFee, DefaultAmount: 50000, IsActive: true},
			{FeeType: billingconfig.FeeTypeKeyService, DefaultAmount: 20000, IsActive: true},
			// PRORATE_DAILY_RATE required for the partial-month rent line
			// (default move-out is mid-month).
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			li := findLineByType(bill.LineItems, tt.lineType)
			if li.Amount != tt.wantAmount {
				t.Errorf("%s = %d, want %d", tt.lineType, li.Amount, tt.wantAmount)
			}
		})
	}
}

// ── Notice Date Must Be Ignored (TC13) ──
// ล็อกว่า notice date ไม่ affect calculation เลย ไม่ว่าจะก่อนหรือหลัง move-out

func TestSettlement_UsesActualMoveOutDate(t *testing.T) {
	tests := []struct {
		name       string
		noticeDate time.Time
	}{
		// notice BEFORE move-out
		{"notice_before_moveout", time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)},
		// notice AFTER move-out (e.g. backdated entry)
		{"notice_after_moveout", time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newSettlementOpts() // moveOut = April 14
			o.repo.apartmentID = uuid.New()
			meters := &mockMeterQuerier{reading: testExitReading(o.contract.RoomID, o.moveOut)}
			notice := completedNotice(o.contract.ID, o.moveOut)
			notice.NoticeDate = tt.noticeDate

			svc := newSvc(o.repo, &mockContractQuerier{contract: o.contract},
				meters, o.configs, &mockMoveOutQuerier{notice: notice})

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			bill := o.repo.createdBill

			if bill.BillingMonth != "2026-04" {
				t.Errorf("billing_month = %s, want 2026-04 (from move-out, NOT notice)", bill.BillingMonth)
			}
			prorate := findLineByType(bill.LineItems, LineItemProrateRent)
			if prorate.Quantity != 14 {
				t.Errorf("prorate days = %d, want 14 (from move-out date)", prorate.Quantity)
			}
		})
	}
}

// ── Regression: No PREPAID_CREDIT Even With Old Mock (TC23) ──

func TestSettlement_Regression_NoPrepaidCredit(t *testing.T) {
	bill := runSettlement(t, func(o *settlementOpts) {
		// Old pipeline used SumPaidByContractSince — ensure no PREPAID_CREDIT
		o.repo.sumPaidFn = func(_ context.Context, _ uuid.UUID, _ string) (int64, error) {
			return 999999, nil
		}
	})

	if lineItemTypes(bill.LineItems)[LineItemPrepaidCredit] {
		t.Fatal("REGRESSION: PREPAID_CREDIT must not exist — old logic removed")
	}
}

// ── Invariants — "ผลลัพธ์สุดท้ายต้องไม่มีสิ่งที่ไม่ควรเกิด" ──
// Run settlement ทั้ง 2 path (paid / not-paid) แล้วเช็ค structural invariants
// กัน: future refactor พลาด, dev ใหม่ใส่ logic แปลก, silent data corruption

func TestSettlement_Invariants(t *testing.T) {
	tests := []struct {
		name string
		opts []func(*settlementOpts)
	}{
		{"rent_paid", []func(*settlementOpts){withRentPaid}},
		{"rent_not_paid", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bill := runSettlement(t, tt.opts...)

			// 1. ห้ามมี PREPAID_CREDIT (old logic ลบแล้ว)
			for _, li := range bill.LineItems {
				if li.LineType == LineItemPrepaidCredit {
					t.Error("PREPAID_CREDIT must never appear")
				}
			}

			// 2. ห้ามมี rent line ที่ amount < 0 (ไม่มี rent refund)
			for _, li := range bill.LineItems {
				if (li.LineType == LineItemProrateRent || li.LineType == LineItemRoomRent) && li.Amount < 0 {
					t.Errorf("negative rent line: %s = %d", li.LineType, li.Amount)
				}
			}

			// 3. ELECTRICITY / WATER ต้องมีอย่างละ 1 บรรทัดเท่านั้น
			typeCounts := map[LineItemType]int{}
			for _, li := range bill.LineItems {
				if li.Source == LineItemSourceAuto {
					typeCounts[li.LineType]++
				}
			}
			if typeCounts[LineItemElectricity] != 1 {
				t.Errorf("electricity lines = %d, want exactly 1", typeCounts[LineItemElectricity])
			}
			if typeCounts[LineItemWater] != 1 {
				t.Errorf("water lines = %d, want exactly 1", typeCounts[LineItemWater])
			}

			// 4. AUTO line type ห้ามซ้ำ (1 type = 1 line)
			for lt, count := range typeCounts {
				if count > 1 {
					t.Errorf("duplicate AUTO line type %s: count = %d", lt, count)
				}
			}

			// 5. TotalAmount == sum(line items)
			var sum int64
			for _, li := range bill.LineItems {
				sum += li.Amount
			}
			if bill.TotalAmount != sum {
				t.Errorf("TotalAmount = %d, sum(lines) = %d — mismatch", bill.TotalAmount, sum)
			}

			// 6. DepositBalance consistency
			// Forfeited: -TotalAmount (deposit not applied)
			// Returnable: DepositAmount - TotalAmount
			var wantBalance int64
			if bill.DepositForfeited {
				wantBalance = -bill.TotalAmount
			} else {
				wantBalance = bill.DepositAmount - bill.TotalAmount
			}
			if bill.DepositBalance != wantBalance {
				t.Errorf("DepositBalance = %d, want %d (forfeited=%v deposit=%d total=%d)",
					bill.DepositBalance, wantBalance, bill.DepositForfeited, bill.DepositAmount, bill.TotalAmount)
			}
		})
	}
}

// ── Error Cases (TC19 + guards) ──

func TestSettlement_Errors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() BillingService
		wantErr error
	}{
		{
			"TC19_missing_exit_meter",
			func() BillingService {
				c := testContract()
				c.Status = contract.ContractStatusEnded
				moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
				return newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
					&mockMeterQuerier{}, &mockConfigQuerier{},
					&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOut)})
			},
			ErrExitReadingMissing,
		},
		{
			"rejects_no_actual_date",
			func() BillingService {
				c := testContract()
				return newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
					&mockMeterQuerier{}, &mockConfigQuerier{},
					&mockMoveOutQuerier{notice: &moveout.MoveOutNotice{
						ID: uuid.New(), ContractID: c.ID,
						Status:               moveout.MoveOutStatusPendingMeter,
						ScheduledMoveOutDate: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
					}})
			},
			ErrActualDateRequired,
		},
		{
			"no_move_out_notice",
			func() BillingService {
				c := testContract()
				return newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
					&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})
			},
			ErrMoveOutNotFound,
		},
		{
			"latest_reading_is_monthly_not_exit",
			func() BillingService {
				c := testContract()
				c.Status = contract.ContractStatusEnded
				moveOut := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
				return newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
					&mockMeterQuerier{reading: testMonthlyReading(c.RoomID, "2026-03")},
					&mockConfigQuerier{},
					&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOut)})
			},
			ErrExitReadingMissing,
		},
		{
			"duplicate_settlement_guard",
			func() BillingService {
				c := testContract()
				c.Status = contract.ContractStatusEnded
				moveOut := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
				return newSvc(
					&mockBillingRepo{findByContractAndMonthFn: func(_ context.Context, _ uuid.UUID, _ string, bt BillType) (*Bill, error) {
						if bt == BillTypeSettlement {
							return &Bill{ID: uuid.New()}, nil
						}
						return nil, gorm.ErrRecordNotFound
					}},
					&mockContractQuerier{contract: c},
					&mockMeterQuerier{reading: testExitReading(c.RoomID, moveOut)},
					&mockConfigQuerier{},
					&mockMoveOutQuerier{notice: completedNotice(c.ID, moveOut)})
			},
			ErrBillAlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := tt.setup()
			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: uuid.New().String(),
			}, nil)
			if err != tt.wantErr {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
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

	bill, err := svc.FinalizeBill(context.Background(), billID, nil)
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

	bill, err := svc.VoidBill(context.Background(), billID, VoidBillRequest{Reason: "ข้อมูลผิด"}, nil)
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

	_, err := svc.VoidBill(context.Background(), billID, VoidBillRequest{Reason: "test"}, nil)
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

// ============================================================
// Settlement rent mode helpers
// ============================================================

func TestEndOfMonth(t *testing.T) {
	tests := []struct {
		date time.Time
		want time.Time
	}{
		{time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got := endOfMonth(tt.date)
		if !got.Equal(tt.want) {
			t.Errorf("endOfMonth(%s) = %s, want %s", tt.date.Format("2006-01-02"), got.Format("2006-01-02"), tt.want.Format("2006-01-02"))
		}
	}
}

func TestEffectiveMoveOutDate(t *testing.T) {
	mid := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	eom := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	if got := effectiveMoveOutDate(mid, RentModeProrated); !got.Equal(mid) {
		t.Errorf("PRORATED: got %s, want %s", got.Format("2006-01-02"), mid.Format("2006-01-02"))
	}
	if got := effectiveMoveOutDate(mid, RentModeFullMonthKeepDeposit); !got.Equal(eom) {
		t.Errorf("FULL_MONTH: got %s, want %s", got.Format("2006-01-02"), eom.Format("2006-01-02"))
	}
}

// ============================================================
// Deposit qualification with rent mode
// ============================================================

func TestDepositQualification_RentMode(t *testing.T) {
	// Contract: start Jan 15, minMonths = 6 → eligible at July 15
	// Using mid-month start so FULL_MONTH can cross the eligibility threshold.
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	minMonths := 6

	tests := []struct {
		name    string
		moveOut time.Time
		mode    SettlementRentMode
		wantRet bool
	}{
		// July 10 — PRORATED: July 10 < July 15 → forfeited
		{"jul10_prorated", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), RentModeProrated, false},
		// July 10 — FULL_MONTH: effective = July 31 >= July 15 → returnable
		{"jul10_full_month", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), RentModeFullMonthKeepDeposit, true},
		// March 10 — FULL_MONTH: effective = March 31, ~2.5 months < 6 → still forfeited
		{"march_full_month_still_short", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), RentModeFullMonthKeepDeposit, false},
		// July 15 — PRORATED: exactly 6 months → returnable
		{"jul15_prorated", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), RentModeProrated, true},
		// July 14 — PRORATED: 1 day short → forfeited
		{"jul14_prorated", time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), RentModeProrated, false},
		// July 14 — FULL_MONTH: effective = July 31 >= July 15 → returnable
		{"jul14_full_month", time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), RentModeFullMonthKeepDeposit, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qualifyDate := effectiveMoveOutDate(tt.moveOut, tt.mode)
			got := isDepositReturnable(start, qualifyDate, minMonths)
			if got != tt.wantRet {
				t.Errorf("returnable = %v, want %v (qualifyDate = %s)", got, tt.wantRet, qualifyDate.Format("2006-01-02"))
			}
		})
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
	return NewBillingService(repo, &mockBillAuditRepo{}, &mockContractQuerier{}, meters, &mockConfigQuerier{}, moveOuts, &mockTxManager{})
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
		findRoomIDsWithMoveOutInMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
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

// Move-out scheduled NEXT month must not skip the current-month billing.
// Tenant is still in the room for billing_month and must receive the normal
// monthly bill; settlement will replace the next-month bill instead.
func TestBatchCreateMonthlyBills_MoveOutNextMonth_StillBills(t *testing.T) {
	cwr, c := testContractWithRoom(1, "101")
	r := testMonthlyReading(c.RoomID, "2026-03")

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr}, nil
		},
	}

	// Repo filters by month, so the next-month notice simply doesn't appear
	// in the map returned for the current billing month — mock that contract.
	moveOuts := &mockMoveOutQuerier{
		findRoomIDsWithMoveOutInMonthFn: func(_ context.Context, _ []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error) {
			if billingMonth == "2026-03" {
				return map[uuid.UUID]bool{}, nil // not skipped for current month
			}
			return map[uuid.UUID]bool{cwr.RoomID: true}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: r}, nil
		},
	}

	svc := batchSvc(repo, meters, moveOuts)
	batch, items := runBatch(t, svc, repo, "2026-03")

	if batch.CreatedCount != 1 {
		t.Fatalf("created = %d, want 1 (next-month move-out must not block this month's bill)", batch.CreatedCount)
	}
	if batch.SkippedCount != 0 {
		t.Errorf("skipped = %d, want 0", batch.SkippedCount)
	}
	if items[0].ResultType != ResultCreated {
		t.Errorf("result = %s, want CREATED", items[0].ResultType)
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

func TestComputeDepositSettlement(t *testing.T) {
	t.Run("returnable — deposit exceeds charges → refund", func(t *testing.T) {
		ds := computeDepositSettlement(1000000, 600000, true) // 10k deposit, 6k charges
		if ds.OriginalAmount != 1000000 {
			t.Errorf("OriginalAmount = %d, want 1000000", ds.OriginalAmount)
		}
		if ds.ForfeitedAmount != 0 {
			t.Errorf("ForfeitedAmount = %d, want 0", ds.ForfeitedAmount)
		}
		if ds.AppliedAmount != 600000 {
			t.Errorf("AppliedAmount = %d, want 600000", ds.AppliedAmount)
		}
		if ds.RefundAmount != 400000 {
			t.Errorf("RefundAmount = %d, want 400000", ds.RefundAmount)
		}
		if ds.AmountDue != 0 {
			t.Errorf("AmountDue = %d, want 0", ds.AmountDue)
		}
	})

	t.Run("returnable — charges exceed deposit → tenant pays", func(t *testing.T) {
		ds := computeDepositSettlement(300000, 800000, true) // 3k deposit, 8k charges
		if ds.AppliedAmount != 300000 {
			t.Errorf("AppliedAmount = %d, want 300000", ds.AppliedAmount)
		}
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0", ds.RefundAmount)
		}
		if ds.AmountDue != 500000 {
			t.Errorf("AmountDue = %d, want 500000", ds.AmountDue)
		}
	})

	t.Run("early exit — deposit forfeited, not applied to charges", func(t *testing.T) {
		ds := computeDepositSettlement(1000000, 400000, false) // 10k deposit, 4k charges
		if ds.AvailableToApply != 0 {
			t.Errorf("AvailableToApply = %d, want 0 (forfeited deposit not available)", ds.AvailableToApply)
		}
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited deposit not applied)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 1000000 {
			t.Errorf("ForfeitedAmount = %d, want 1000000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0", ds.RefundAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("early exit — charges exceed deposit, tenant pays full charges", func(t *testing.T) {
		ds := computeDepositSettlement(300000, 800000, false) // 3k deposit, 8k charges
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited deposit not applied)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 300000 {
			t.Errorf("ForfeitedAmount = %d, want 300000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 800000 {
			t.Errorf("AmountDue = %d, want 800000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("early exit — zero deposit, tenant pays full charges", func(t *testing.T) {
		ds := computeDepositSettlement(0, 400000, false)
		if ds.ForfeitedAmount != 0 {
			t.Errorf("ForfeitedAmount = %d, want 0 (nothing to forfeit)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000", ds.AmountDue)
		}
	})

	t.Run("early exit — charges exactly equal deposit", func(t *testing.T) {
		ds := computeDepositSettlement(500000, 500000, false) // 5k deposit = 5k charges
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 500000 {
			t.Errorf("ForfeitedAmount = %d, want 500000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 500000 {
			t.Errorf("AmountDue = %d, want 500000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("returnable — zero deposit", func(t *testing.T) {
		ds := computeDepositSettlement(0, 400000, true)
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0 (no deposit to refund)", ds.RefundAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000", ds.AmountDue)
		}
	})
}
