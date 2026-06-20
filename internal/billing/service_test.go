package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Hand-written mocks ---

type mockBillingRepo struct {
	findAllFn                           func(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	listBillsByBatchIDFn                func(batchID uuid.UUID) ([]Bill, error)
	listMonthlyBillsByApartmentMonthFn  func(apartmentID uuid.UUID, billingMonth string) ([]Bill, error)
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
	findCorrectedFromBillIDFn           func(ctx context.Context, billID uuid.UUID) (*uuid.UUID, error)
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

	createdBatch         *BillGenerationBatch
	createdBatchItems    []BillGenerationBatchItem
	batchItemBillStatuses map[uuid.UUID]BillStatus // bill_id → status, stamped onto BatchItemWithTenant
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
func (m *mockBillingRepo) ListMonthlyBillsByApartmentMonth(_ context.Context, apartmentID uuid.UUID, billingMonth string) ([]Bill, error) {
	if m.listMonthlyBillsByApartmentMonthFn != nil {
		return m.listMonthlyBillsByApartmentMonthFn(apartmentID, billingMonth)
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
		wrapped := BatchItemWithTenant{BillGenerationBatchItem: it}
		// Tests can opt into per-item bill_status via batchItemBillStatuses
		// without needing a custom mock override. Keyed by bill_id so items
		// with BillID == nil naturally have BillStatus = nil (uncommitted).
		if it.BillID != nil {
			if s, ok := m.batchItemBillStatuses[*it.BillID]; ok {
				bs := s
				wrapped.BillStatus = &bs
			}
		}
		result[i] = wrapped
	}
	return result, nil
}
func (m *mockBillingRepo) ListBatches(_ context.Context, _ BatchListParams) ([]BillGenerationBatch, int64, error) {
	return nil, 0, nil
}

// Single-item re-plan stubs. Service tests for re-plan live in
// service_batch_replan_integration_test.go (real Postgres) because the
// flow touches the planner's joined inputs (contracts + meters + bills)
// and a mock that returns canned rows would skip the integration risks.
func (m *mockBillingRepo) FindBatchItemByID(_ context.Context, itemID uuid.UUID) (*BillGenerationBatchItem, error) {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			it := m.createdBatchItems[i]
			return &it, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) FindBatchItemByIDWithTenant(_ context.Context, itemID uuid.UUID) (*BatchItemWithTenant, error) {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			return &BatchItemWithTenant{BillGenerationBatchItem: m.createdBatchItems[i]}, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockBillingRepo) UpdateBatchItemPlan(_ context.Context, itemID uuid.UUID, resultType ResultType, reasonCode, reasonText string, billID *uuid.UUID, snapshot ComputedSnapshot) error {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			m.createdBatchItems[i].ResultType = resultType
			m.createdBatchItems[i].ReasonCode = reasonCode
			m.createdBatchItems[i].ReasonText = reasonText
			m.createdBatchItems[i].BillID = billID
			m.createdBatchItems[i].ComputedSnapshot = snapshot
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

// --- Commit flow mocks ---

func (m *mockBillingRepo) LockBatchForCommit(_ context.Context, _ uuid.UUID) (*BillGenerationBatch, error) {
	if m.createdBatch != nil {
		return m.createdBatch, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// LockBillForCorrection mirrors FindByID semantics so correction tests can
// inject the "old bill" via findByIDFn (or createdBill fallback). In real
// production the SELECT FOR UPDATE locks the row; here the lock is a no-op
// because mockTxManager doesn't model row-level concurrency.
func (m *mockBillingRepo) LockBillForCorrection(ctx context.Context, id uuid.UUID) (*Bill, error) {
	return m.FindByID(ctx, id)
}

// FindCorrectedFromBillID returns nil pointer by default so existing tests
// (which don't care about the reverse correction-chain link) see the
// "normal bill" path. Tests covering replacement-side bills should set
// findCorrectedFromBillIDFn explicitly.
func (m *mockBillingRepo) FindCorrectedFromBillID(ctx context.Context, billID uuid.UUID) (*uuid.UUID, error) {
	if m.findCorrectedFromBillIDFn != nil {
		return m.findCorrectedFromBillIDFn(ctx, billID)
	}
	return nil, nil
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

func (m *mockBillingRepo) LockBillForPayment(ctx context.Context, id uuid.UUID) (*Bill, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockBillingRepo) FindPaymentsByBillIDs(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]*BillPaymentRecord, error) {
	return map[uuid.UUID]*BillPaymentRecord{}, nil
}

func (m *mockBillingRepo) FindRoomApartmentInfo(_ context.Context, _ uuid.UUID) (uuid.UUID, string, error) {
	return uuid.Nil, "", nil
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

// mockMoveOutQuerier was removed in W4 commit 3 — MoveOutQuerier port no
// longer lives at billing root (settlement consumes its own MoveOutSource
// port). Reconciliation tests use a structurally-typed inline stub.

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

// FindLatestSupersedeReason mirrors the production query in-memory: scan
// recorded logs for the most recent SUPERSEDE row matching the bill and
// unmarshal its AuditSupersedePayload. Returns ("", nil) when no row exists
// so VOID bills without a SUPERSEDE event behave like the real DB path.
func (m *mockBillAuditRepo) FindLatestSupersedeReason(_ context.Context, billID uuid.UUID) (string, error) {
	for i := len(m.logs) - 1; i >= 0; i-- {
		log := m.logs[i]
		if log.BillID != billID || log.Action != AuditSupersede {
			continue
		}
		var payload AuditSupersedePayload
		if err := json.Unmarshal([]byte(log.Payload), &payload); err != nil {
			return "", fmt.Errorf("mock: unmarshal supersede payload: %w", err)
		}
		return payload.CorrectionReason, nil
	}
	return "", nil
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

func newSvc(repo *mockBillingRepo, contracts *mockContractQuerier, meters *mockMeterQuerier, configs *mockConfigQuerier) BillingService {
	return NewBillingService(repo, &mockBillAuditRepo{}, contracts, meters, configs, nil, &mockTxManager{})
}

func TestCreateMonthlyBill_HappyPath(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-03")
	repo := &mockBillingRepo{}

	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{})

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
		&mockMeterQuerier{}, &mockConfigQuerier{})

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
		&mockMeterQuerier{}, &mockConfigQuerier{})

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
		&mockMeterQuerier{reading: exitReading}, &mockConfigQuerier{})

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
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{})

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
		&mockMeterQuerier{reading: reading}, &mockConfigQuerier{})

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

func TestFinalizeBill_HappyPath(t *testing.T) {
	billID := uuid.New()
	svc := newSvc(
		&mockBillingRepo{findByIDFn: func(_ context.Context, _ uuid.UUID) (*Bill, error) {
			return &Bill{ID: billID, Status: BillStatusDraft, LineItems: []BillLineItem{{Amount: 100}}}, nil
		}},
		&mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{})

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
		&mockConfigQuerier{})

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
		&mockConfigQuerier{})

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
		&mockConfigQuerier{})

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
		&mockConfigQuerier{})

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

