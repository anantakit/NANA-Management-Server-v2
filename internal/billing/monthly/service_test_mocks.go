package monthly

import (
	"context"
	"errors"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// mockStore is the consolidated test double for the monthly workflow.
// Implements BillStore + AuditStore + BatchRepository so a single instance
// can be passed three times to NewService — mirroring the production
// wiring where billing.MonthlyAdapter satisfies the first two and
// monthly.BatchRepository (this same mockStore) satisfies the third.
//
// Only the methods the migrated tests actually exercise are implemented;
// the surface is intentionally narrower than the production BillingRepository
// to keep the mock focused on monthly-specific test scenarios.
//
// Stub fields (createFn, findByIDFn, etc.) preserve the per-test override
// style used in the original mockBillingRepo so test bodies stay structurally
// similar to their pre-migration form.
type mockStore struct {
	// --- Bill side ---
	createFn                          func(ctx context.Context, bill *billing.Bill) error
	findByIDFn                        func(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	findByContractAndMonthFn          func(ctx context.Context, contractID uuid.UUID, month string, bt billing.BillType) (*billing.Bill, error)
	findRecoverySourceRefundRatesFn   func(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*billing.SourceRefundRates, error)
	findActiveContractsByApartmentFn  func(ctx context.Context, apartmentID uuid.UUID) ([]billing.ContractWithRoom, error)
	findExistingByContractsAndMonthFn func(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*billing.Bill, error)
	listByBatchIDFn                   func(batchID uuid.UUID) ([]billing.Bill, error)
	listMonthlyByApartmentMonthFn     func(apartmentID uuid.UUID, billingMonth string) ([]billing.Bill, error)

	createdBill  *billing.Bill   // last bill created (legacy single-bill tests)
	createdBills []*billing.Bill // every bill created in order (batch commit tests)
	updatedBills []*billing.Bill // every bill mutated via FinalizeBillInTx

	// --- Batch side ---
	createdBatch          *billing.BillGenerationBatch
	createdBatchItems     []billing.BillGenerationBatchItem
	batchItemBillStatuses map[uuid.UUID]billing.BillStatus // bill_id → status, stamped onto BatchItemWithTenant

	// --- Audit side ---
	createErr         error
	createErrByBillID map[uuid.UUID]error // per-bill audit failure injection
	editedErr         error
	logs              []billing.BillAuditLog
	editedQueryCalls  int
}

var (
	_ BillStore       = (*mockStore)(nil)
	_ AuditStore      = (*mockStore)(nil)
	_ BatchRepository = (*mockStore)(nil)
)

// --- BillStore: queries ---

func (m *mockStore) FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	if m.createdBill != nil {
		return m.createdBill, nil
	}
	return nil, errMockNotFound
}

func (m *mockStore) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, month string, bt billing.BillType) (*billing.Bill, error) {
	if m.findByContractAndMonthFn != nil {
		return m.findByContractAndMonthFn(ctx, contractID, month, bt)
	}
	return nil, errMockNotFound
}

func (m *mockStore) FindRecoverySourceRefundRates(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*billing.SourceRefundRates, error) {
	if m.findRecoverySourceRefundRatesFn != nil {
		return m.findRecoverySourceRefundRatesFn(ctx, sourceReadingID, contractID)
	}
	return nil, nil // default: source never billed → no forward credit
}

func (m *mockStore) ListByBatchID(_ context.Context, batchID uuid.UUID) ([]billing.Bill, error) {
	if m.listByBatchIDFn != nil {
		return m.listByBatchIDFn(batchID)
	}
	return nil, nil
}

func (m *mockStore) ListMonthlyByApartmentMonth(_ context.Context, apartmentID uuid.UUID, billingMonth string) ([]billing.Bill, error) {
	if m.listMonthlyByApartmentMonthFn != nil {
		return m.listMonthlyByApartmentMonthFn(apartmentID, billingMonth)
	}
	return nil, nil
}

func (m *mockStore) FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billing.ContractWithRoom, error) {
	if m.findActiveContractsByApartmentFn != nil {
		return m.findActiveContractsByApartmentFn(ctx, apartmentID)
	}
	return nil, nil
}

func (m *mockStore) FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*billing.Bill, error) {
	if m.findExistingByContractsAndMonthFn != nil {
		return m.findExistingByContractsAndMonthFn(ctx, contractIDs, month)
	}
	return map[uuid.UUID]*billing.Bill{}, nil
}

// --- BillStore: commands ---

func (m *mockStore) CreateBill(ctx context.Context, b *billing.Bill) error {
	m.createdBill = b
	m.createdBills = append(m.createdBills, b)
	if m.createFn != nil {
		return m.createFn(ctx, b)
	}
	return nil
}

func (m *mockStore) UpdateBill(_ context.Context, _ *billing.Bill) error {
	return nil
}

// FinalizeBillInTx mirrors the production package-level finalizeBillInTx:
// load via FindByID, run the domain Finalize() guard, append to updatedBills
// on success, then record the FINALIZE audit row. Sentinel errors propagate
// untouched so classifyFinalizeError's errors.Is checks work.
func (m *mockStore) FinalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error {
	b, err := m.FindByID(txCtx, id)
	if err != nil {
		if errors.Is(err, errMockNotFound) {
			return billing.ErrBillNotFound
		}
		return err
	}
	previousStatus := string(b.Status)
	if err := b.Finalize(); err != nil {
		// Raw sentinel — caller (classifyFinalizeError) keys off errors.Is.
		return err
	}
	m.updatedBills = append(m.updatedBills, b)
	return m.RecordAudit(txCtx, b.ID, billing.AuditFinalize, actor, billing.AuditFinalizePayload{
		PreviousStatus: previousStatus,
		TotalAmount:    b.TotalAmount,
	})
}

// --- AuditStore: queries ---

func (m *mockStore) EditedBillIDs(_ context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
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

// --- AuditStore: commands ---

func (m *mockStore) RecordAudit(_ context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error {
	if err, ok := m.createErrByBillID[billID]; ok && err != nil {
		return err
	}
	if m.createErr != nil {
		return m.createErr
	}
	p, err := billing.MarshalAuditPayload(payload)
	if err != nil {
		return err
	}
	m.logs = append(m.logs, billing.BillAuditLog{
		BillID:  billID,
		Action:  action,
		ActorID: actor,
		Payload: p,
	})
	return nil
}

// --- BatchRepository: queries ---

func (m *mockStore) FindBatchByID(_ context.Context, _ uuid.UUID) (*billing.BillGenerationBatch, error) {
	if m.createdBatch != nil {
		return m.createdBatch, nil
	}
	return nil, errMockNotFound
}

func (m *mockStore) FindBatchItemsByBatchID(_ context.Context, _ uuid.UUID) ([]billing.BatchItemWithTenant, error) {
	result := make([]billing.BatchItemWithTenant, len(m.createdBatchItems))
	for i, it := range m.createdBatchItems {
		wrapped := billing.BatchItemWithTenant{BillGenerationBatchItem: it}
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

func (m *mockStore) FindBatchItemByID(_ context.Context, itemID uuid.UUID) (*billing.BillGenerationBatchItem, error) {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			it := m.createdBatchItems[i]
			return &it, nil
		}
	}
	return nil, errMockNotFound
}

func (m *mockStore) FindBatchItemByIDWithTenant(_ context.Context, itemID uuid.UUID) (*billing.BatchItemWithTenant, error) {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			return &billing.BatchItemWithTenant{BillGenerationBatchItem: m.createdBatchItems[i]}, nil
		}
	}
	return nil, errMockNotFound
}

func (m *mockStore) ListBatches(_ context.Context, _ billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error) {
	return nil, 0, nil
}

func (m *mockStore) ListCommitPendingItems(_ context.Context, _ uuid.UUID) ([]billing.BillGenerationBatchItem, error) {
	var pending []billing.BillGenerationBatchItem
	for _, it := range m.createdBatchItems {
		if it.ResultType == billing.ResultCreated && it.BillID == nil {
			pending = append(pending, it)
		}
	}
	return pending, nil
}

// --- BatchRepository: commands ---

func (m *mockStore) CreateBatch(_ context.Context, batch *billing.BillGenerationBatch, items []billing.BillGenerationBatchItem) error {
	m.createdBatch = batch
	m.createdBatchItems = items
	return nil
}

func (m *mockStore) LockBatchForCommit(_ context.Context, _ uuid.UUID) (*billing.BillGenerationBatch, error) {
	if m.createdBatch != nil {
		return m.createdBatch, nil
	}
	return nil, errMockNotFound
}

func (m *mockStore) UpdateBatchItemCommitError(_ context.Context, itemID uuid.UUID, reasonText string) error {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			m.createdBatchItems[i].ReasonCode = billing.ReasonCodeCommitError
			m.createdBatchItems[i].ReasonText = reasonText
			break
		}
	}
	return nil
}

func (m *mockStore) UpdateBatchCommitStatus(_ context.Context, _ uuid.UUID, status billing.CommitStatus, committedAt *time.Time) error {
	if m.createdBatch != nil {
		m.createdBatch.CommitStatus = &status
		m.createdBatch.CommittedAt = committedAt
	}
	return nil
}

func (m *mockStore) UpdateBatchItemCommitted(_ context.Context, itemID uuid.UUID, billID uuid.UUID) error {
	for i := range m.createdBatchItems {
		if m.createdBatchItems[i].ID == itemID {
			m.createdBatchItems[i].BillID = &billID
			break
		}
	}
	return nil
}

func (m *mockStore) UpdateBatchItemPlan(_ context.Context, itemID uuid.UUID, resultType billing.ResultType, reasonCode, reasonText string, billID *uuid.UUID, snapshot billing.ComputedSnapshot) error {
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
	return errMockNotFound
}

// errMockNotFound mirrors the repository's NotFound sentinel so the service
// code path that uses database.IsNotFound (errors.Is gorm.ErrRecordNotFound)
// classifies mocked misses correctly — e.g. LockBatchForCommit → billing.
// ErrBatchNotFound. Tests assert on the AppError/sentinel the service emits,
// not on this raw value.
var errMockNotFound = gorm.ErrRecordNotFound

// --- MeterReadingSource mock ---

type mockMeterQuerier struct {
	findMonthlyByRoomsAndMonthFn            func(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
	findConsumptionMonthlyByRoomsAndMonthFn func(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
	findReplacementAnchorsByRoomsAndMonthFn func(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID][]*meterreading.MeterReading, error)
}

var _ MeterReadingSource = (*mockMeterQuerier)(nil)

func (m *mockMeterQuerier) FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error) {
	if m.findMonthlyByRoomsAndMonthFn != nil {
		return m.findMonthlyByRoomsAndMonthFn(ctx, roomIDs, month)
	}
	return map[uuid.UUID]*meterreading.MeterReading{}, nil
}

func (m *mockMeterQuerier) FindConsumptionMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error) {
	if m.findConsumptionMonthlyByRoomsAndMonthFn != nil {
		return m.findConsumptionMonthlyByRoomsAndMonthFn(ctx, roomIDs, month)
	}
	return map[uuid.UUID]*meterreading.MeterReading{}, nil
}

func (m *mockMeterQuerier) FindReplacementAnchorsByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID][]*meterreading.MeterReading, error) {
	if m.findReplacementAnchorsByRoomsAndMonthFn != nil {
		return m.findReplacementAnchorsByRoomsAndMonthFn(ctx, roomIDs, month)
	}
	return map[uuid.UUID][]*meterreading.MeterReading{}, nil
}

// --- MoveOutSource mock ---

type mockMoveOutQuerier struct {
	findRoomIDsWithMoveOutInMonthFn func(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
}

var _ MoveOutSource = (*mockMoveOutQuerier)(nil)

func (m *mockMoveOutQuerier) FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error) {
	if m.findRoomIDsWithMoveOutInMonthFn != nil {
		return m.findRoomIDsWithMoveOutInMonthFn(ctx, roomIDs, billingMonth)
	}
	return map[uuid.UUID]bool{}, nil
}

// --- TxManager mock ---

type mockTxManager struct{}

func (m *mockTxManager) RunInTx(_ context.Context, fn func(ctx context.Context) error) error {
	return fn(context.Background())
}
