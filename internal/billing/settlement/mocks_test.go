package settlement

// Hand-written mocks for the settlement workflow's port surface. Migrated
// from billing root in W4 commit 3 (2026-06-19). Tests in this package use
// these mocks directly via the field-based dispatch pattern; nil-default
// fields are valid and return zero values.
//
// Compile-time port satisfaction is asserted at the bottom of the file.
// No external assertion library — the test pattern across the repo uses
// hand-rolled mocks with `var _ PortName = (*mockType)(nil)`.

import (
	"context"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- BillStore ---

type mockBillStore struct {
	findByIDFn                     func(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	findByIDWithRelationsFn        func(ctx context.Context, id uuid.UUID) (*billing.BillWithRelations, error)
	findByContractAndMonthFn       func(ctx context.Context, contractID uuid.UUID, month string, bt billing.BillType) (*billing.Bill, error)
	findNonVoidedByContractMonthFn func(ctx context.Context, contractID uuid.UUID, month string) ([]billing.Bill, error)
	findUnpaidMonthlyFn            func(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error)
	findAbsorbedFn                 func(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error)
	hasPaidAdvanceRentFn           func(ctx context.Context, contractID uuid.UUID, month string) (bool, error)
	apartmentID                    uuid.UUID
	findApartmentIDNotFound        bool
	createFn                       func(ctx context.Context, bill *billing.Bill) error
	updateFn                       func(ctx context.Context, bill *billing.Bill) error
	lockBillForCorrectionFn        func(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	hasNonVoidAdjustmentFn         func(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error)
	hasPendingRecoveryFn           func(ctx context.Context, contractID uuid.UUID) (bool, error)
	findUnreflectedFn              func() ([]billing.UnreflectedOverRecord, error)
	sourceRefundRatesFn            func() (*billing.SourceRefundRates, error)

	updateErrByBillID map[uuid.UUID]error

	createdBill            *billing.Bill
	createdBills           []*billing.Bill
	updatedBills           []*billing.Bill
	deletedSourcesByBillID map[uuid.UUID][]billing.LineItemSource
	createdLineItems       []billing.BillLineItem
	zeroedAutoLines        []struct {
		BillID   uuid.UUID
		LineType billing.LineItemType
	}
}

var defaultMockApartmentID = uuid.MustParse("00000000-0000-0000-0000-000000000aaa")

func (m *mockBillStore) FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	if m.createdBill != nil {
		return m.createdBill, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockBillStore) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*billing.BillWithRelations, error) {
	if m.findByIDWithRelationsFn != nil {
		return m.findByIDWithRelationsFn(ctx, id)
	}
	if m.createdBill != nil {
		return &billing.BillWithRelations{Bill: *m.createdBill}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockBillStore) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, month string, bt billing.BillType) (*billing.Bill, error) {
	if m.findByContractAndMonthFn != nil {
		return m.findByContractAndMonthFn(ctx, contractID, month, bt)
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockBillStore) FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, month string) ([]billing.Bill, error) {
	if m.findNonVoidedByContractMonthFn != nil {
		return m.findNonVoidedByContractMonthFn(ctx, contractID, month)
	}
	return nil, nil
}

func (m *mockBillStore) FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error) {
	if m.findUnpaidMonthlyFn != nil {
		return m.findUnpaidMonthlyFn(ctx, contractID)
	}
	return nil, nil
}

func (m *mockBillStore) FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error) {
	if m.findAbsorbedFn != nil {
		return m.findAbsorbedFn(ctx, contractID)
	}
	return nil, nil
}

func (m *mockBillStore) HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, month string) (bool, error) {
	if m.hasPaidAdvanceRentFn != nil {
		return m.hasPaidAdvanceRentFn(ctx, contractID, month)
	}
	return false, nil
}

func (m *mockBillStore) FindApartmentIDByRoomID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	if m.findApartmentIDNotFound {
		return uuid.Nil, gorm.ErrRecordNotFound
	}
	if m.apartmentID != uuid.Nil {
		return m.apartmentID, nil
	}
	return defaultMockApartmentID, nil
}

func (m *mockBillStore) FindRoomApartmentInfo(_ context.Context, _ uuid.UUID) (uuid.UUID, string, error) {
	if m.apartmentID != uuid.Nil {
		return m.apartmentID, "101", nil
	}
	return defaultMockApartmentID, "101", nil
}

func (m *mockBillStore) LockBillForCorrection(ctx context.Context, id uuid.UUID) (*billing.Bill, error) {
	if m.lockBillForCorrectionFn != nil {
		return m.lockBillForCorrectionFn(ctx, id)
	}
	return m.FindByID(ctx, id)
}

func (m *mockBillStore) CreateBill(ctx context.Context, bill *billing.Bill) error {
	if m.createFn != nil {
		return m.createFn(ctx, bill)
	}
	if bill.ID == uuid.Nil {
		bill.ID = uuid.New()
	}
	cp := *bill
	m.createdBill = &cp
	m.createdBills = append(m.createdBills, &cp)
	return nil
}

func (m *mockBillStore) UpdateBill(ctx context.Context, bill *billing.Bill) error {
	if m.updateErrByBillID != nil {
		if err, ok := m.updateErrByBillID[bill.ID]; ok && err != nil {
			return err
		}
	}
	if m.updateFn != nil {
		return m.updateFn(ctx, bill)
	}
	cp := *bill
	m.updatedBills = append(m.updatedBills, &cp)
	// Also update createdBill in-place so FindByID returns the latest state.
	if m.createdBill != nil && m.createdBill.ID == bill.ID {
		m.createdBill = &cp
	}
	return nil
}

func (m *mockBillStore) DeleteLineItemsBySource(_ context.Context, billID uuid.UUID, source billing.LineItemSource) error {
	if m.deletedSourcesByBillID == nil {
		m.deletedSourcesByBillID = map[uuid.UUID][]billing.LineItemSource{}
	}
	m.deletedSourcesByBillID[billID] = append(m.deletedSourcesByBillID[billID], source)
	return nil
}

func (m *mockBillStore) DeleteEditableManualLineItems(_ context.Context, billID uuid.UUID) error {
	if m.deletedSourcesByBillID == nil {
		m.deletedSourcesByBillID = map[uuid.UUID][]billing.LineItemSource{}
	}
	m.deletedSourcesByBillID[billID] = append(m.deletedSourcesByBillID[billID], billing.LineItemSourceManual)
	return nil
}

func (m *mockBillStore) CreateLineItems(_ context.Context, items []billing.BillLineItem) error {
	m.createdLineItems = append(m.createdLineItems, items...)
	return nil
}

func (m *mockBillStore) HasNonVoidAdjustmentLineByRecoveryID(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error) {
	if m.hasNonVoidAdjustmentFn != nil {
		return m.hasNonVoidAdjustmentFn(ctx, recoveryReadingID)
	}
	return false, nil
}

func (m *mockBillStore) HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx context.Context, recoveryReadingID uuid.UUID, _ billing.AdjustmentUtility) (bool, error) {
	if m.hasNonVoidAdjustmentFn != nil {
		return m.hasNonVoidAdjustmentFn(ctx, recoveryReadingID)
	}
	return false, nil
}

func (m *mockBillStore) HasUnreflectedOverRecordByContractID(ctx context.Context, contractID uuid.UUID) (bool, error) {
	if m.hasPendingRecoveryFn != nil {
		return m.hasPendingRecoveryFn(ctx, contractID)
	}
	return false, nil
}

func (m *mockBillStore) FindUnreflectedOverRecordsByContractID(_ context.Context, _ uuid.UUID) ([]billing.UnreflectedOverRecord, error) {
	if m.findUnreflectedFn != nil {
		return m.findUnreflectedFn()
	}
	return nil, nil
}

func (m *mockBillStore) FindRecoverySourceRefundRates(_ context.Context, _, _ uuid.UUID) (*billing.SourceRefundRates, error) {
	if m.sourceRefundRatesFn != nil {
		return m.sourceRefundRatesFn()
	}
	return nil, nil
}

func (m *mockBillStore) ZeroAutoLineUsage(_ context.Context, billID uuid.UUID, lineType billing.LineItemType) error {
	m.zeroedAutoLines = append(m.zeroedAutoLines, struct {
		BillID   uuid.UUID
		LineType billing.LineItemType
	}{billID, lineType})
	return nil
}

// --- AuditStore ---

type mockAuditStore struct {
	recordAuditFn func(ctx context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error

	recorded []recordedAuditEvent
}

type recordedAuditEvent struct {
	BillID  uuid.UUID
	Action  billing.BillAuditAction
	ActorID *uuid.UUID
	Payload any
}

func (m *mockAuditStore) RecordAudit(ctx context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error {
	if m.recordAuditFn != nil {
		return m.recordAuditFn(ctx, billID, action, actor, payload)
	}
	m.recorded = append(m.recorded, recordedAuditEvent{BillID: billID, Action: action, ActorID: actor, Payload: payload})
	return nil
}

// --- Cross-feature mocks ---

type mockContractSource struct {
	findByIDFn func(_ context.Context, _ uuid.UUID) (*contract.Contract, error)
}

func (m *mockContractSource) FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, gorm.ErrRecordNotFound
}

type mockMeterReadingSource struct {
	findExitFn func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error)
	findByIDFn func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error)
}

func (m *mockMeterReadingSource) FindExitByRoomID(ctx context.Context, roomID uuid.UUID) (*meterreading.MeterReading, error) {
	if m.findExitFn != nil {
		return m.findExitFn(ctx, roomID)
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockMeterReadingSource) FindByIDSimple(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, gorm.ErrRecordNotFound
}

// mockBillingConfigSource — when configs is nil the mock returns a sensible
// production default (PRORATE_DAILY_RATE = ฿100/day) so tests don't have to
// wire one up just to exercise the settlement path. Tests that explicitly
// want "no config" set `configs: []billingconfig.BillingConfig{}`.
type mockBillingConfigSource struct {
	configs []billingconfig.BillingConfig
	findFn  func(_ context.Context, _ uuid.UUID) ([]billingconfig.BillingConfig, error)
}

func (m *mockBillingConfigSource) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error) {
	if m.findFn != nil {
		return m.findFn(ctx, apartmentID)
	}
	if m.configs == nil {
		return []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}, nil
	}
	return m.configs, nil
}

type mockMoveOutSource struct {
	notice       *moveout.MoveOutNotice
	findErr      error
	findActiveFn func(_ context.Context, _ uuid.UUID) (*moveout.MoveOutNotice, error)
}

func (m *mockMoveOutSource) FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*moveout.MoveOutNotice, error) {
	if m.findActiveFn != nil {
		return m.findActiveFn(ctx, contractID)
	}
	if m.findErr != nil {
		return nil, m.findErr
	}
	if m.notice != nil {
		return m.notice, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type mockPaymentRoutingSource struct {
	dest *apartment.PaymentDestinationInfo
	err  error
}

func (m *mockPaymentRoutingSource) ResolveDestination(_ context.Context, _ uuid.UUID, _ string) (*apartment.PaymentDestinationInfo, error) {
	return m.dest, m.err
}

// --- TxManager ---

type mockTxManager struct{}

func (m *mockTxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// --- Compile-time port satisfaction checks ---

var (
	_ BillStore            = (*mockBillStore)(nil)
	_ AuditStore           = (*mockAuditStore)(nil)
	_ ContractSource       = (*mockContractSource)(nil)
	_ MeterReadingSource   = (*mockMeterReadingSource)(nil)
	_ BillingConfigSource  = (*mockBillingConfigSource)(nil)
	_ MoveOutSource        = (*mockMoveOutSource)(nil)
	_ PaymentRoutingSource = (*mockPaymentRoutingSource)(nil)
)

// --- Time helpers (settlement tests use these heavily) ---

func mustParseTime(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
