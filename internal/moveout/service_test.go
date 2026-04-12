package moveout

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/contract"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

var errMockUnused = errors.New("mock method not configured for this test")

// --- Hand-written mocks (compile-time interface checks) ---

type mockMoveOutRepo struct {
	findForUpdateFn func(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error)
	findByIDFn      func(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	updateFn        func(ctx context.Context, notice *MoveOutNotice) error
	listActiveFn    func(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error)
	listHistoryFn   func(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error)

	updatedNotice      *MoveOutNotice // snapshot of last Update call
	updatedStatus      MoveOutStatus
	updateCalls        int
	findByIDCalledWith uuid.UUID
}

var _ MoveOutRepository = (*mockMoveOutRepo)(nil)

func (m *mockMoveOutRepo) FindAll(_ context.Context, _ MoveOutListParams) ([]MoveOutWithRelations, int64, error) {
	return nil, 0, nil
}
func (m *mockMoveOutRepo) FindByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	m.findByIDCalledWith = id
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return &MoveOutWithRelations{MoveOutNotice: MoveOutNotice{ID: id}}, nil
}
func (m *mockMoveOutRepo) FindByIDSimple(_ context.Context, _ uuid.UUID) (*MoveOutNotice, error) {
	return nil, errMockUnused
}
func (m *mockMoveOutRepo) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error) {
	if m.findForUpdateFn != nil {
		return m.findForUpdateFn(ctx, id)
	}
	return nil, nil
}
func (m *mockMoveOutRepo) FindActiveByContractID(_ context.Context, _ uuid.UUID) (*MoveOutNotice, error) {
	return nil, nil
}
func (m *mockMoveOutRepo) HasActiveByContractID(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}
func (m *mockMoveOutRepo) FindRoomIDsWithPendingNotice(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]bool, error) {
	return nil, nil
}
func (m *mockMoveOutRepo) ListActive(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error) {
	if m.listActiveFn != nil {
		return m.listActiveFn(ctx, params)
	}
	return nil, nil
}
func (m *mockMoveOutRepo) ListHistory(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error) {
	if m.listHistoryFn != nil {
		return m.listHistoryFn(ctx, params)
	}
	return nil, nil
}
func (m *mockMoveOutRepo) Create(_ context.Context, _ *MoveOutNotice) error { return nil }
func (m *mockMoveOutRepo) Update(ctx context.Context, notice *MoveOutNotice) error {
	m.updateCalls++
	m.updatedStatus = notice.Status
	// Deep-copy key fields for assertion
	cp := *notice
	m.updatedNotice = &cp
	if m.updateFn != nil {
		return m.updateFn(ctx, notice)
	}
	return nil
}

type mockContractQuerier struct {
	findFn func(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

var _ ContractQuerier = (*mockContractQuerier)(nil)

func (m *mockContractQuerier) FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error) {
	return m.findFn(ctx, id)
}

type mockContractCommander struct {
	endCalls        int
	endContractID   uuid.UUID
	endContractDate time.Time
}

var _ ContractCommander = (*mockContractCommander)(nil)

func (m *mockContractCommander) EndContract(_ context.Context, id uuid.UUID, endDate time.Time) error {
	m.endCalls++
	m.endContractID = id
	m.endContractDate = endDate
	return nil
}

type mockRoomCommander struct {
	vacantCalls  int
	vacantRoomID uuid.UUID
}

var _ RoomCommander = (*mockRoomCommander)(nil)

func (m *mockRoomCommander) MarkVacant(_ context.Context, id uuid.UUID) error {
	m.vacantCalls++
	m.vacantRoomID = id
	return nil
}

type mockMeterCommander struct {
	deletedRoomID uuid.UUID
	calls         int
}

var _ MeterReadingCommander = (*mockMeterCommander)(nil)

func (m *mockMeterCommander) DeleteExitByRoomID(_ context.Context, roomID uuid.UUID) error {
	m.calls++
	m.deletedRoomID = roomID
	return nil
}

type mockBillingCommander struct {
	generateFn func(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time) (*SettlementBillResult, error)
	voidFn     func(ctx context.Context, billID uuid.UUID, reason string) error

	generateCalls int
	voidCalls     int
	voidBillID    uuid.UUID
	voidReason    string
}

var _ BillingCommander = (*mockBillingCommander)(nil)

func (m *mockBillingCommander) GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time) (*SettlementBillResult, error) {
	m.generateCalls++
	if m.generateFn != nil {
		return m.generateFn(ctx, contractID, moveOutDate)
	}
	return &SettlementBillResult{
		BillID:      uuid.New(),
		NetAmount:   150000, // 1500 baht
		DepositUsed: 500000, // 5000 baht
	}, nil
}

func (m *mockBillingCommander) VoidSettlement(_ context.Context, billID uuid.UUID, reason string) error {
	m.voidCalls++
	m.voidBillID = billID
	m.voidReason = reason
	if m.voidFn != nil {
		return m.voidFn(context.Background(), billID, reason)
	}
	return nil
}

// noopTxManager runs the callback with the same context — sufficient for mock repos.
type noopTxManager struct{}

func (noopTxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// --- Helper: build a standard test service with all mocks ---

type testHarness struct {
	repo        *mockMoveOutRepo
	contracts   *mockContractQuerier
	contractCmd *mockContractCommander
	roomCmd     *mockRoomCommander
	meterCmd    *mockMeterCommander
	billingCmd  *mockBillingCommander
	svc         MoveOutService
}

func newTestHarness(roomID, contractID uuid.UUID) testHarness {
	h := testHarness{
		repo: &mockMoveOutRepo{},
		contracts: &mockContractQuerier{
			findFn: func(_ context.Context, id uuid.UUID) (*contract.Contract, error) {
				return &contract.Contract{ID: id, RoomID: roomID}, nil
			},
		},
		contractCmd: &mockContractCommander{},
		roomCmd:     &mockRoomCommander{},
		meterCmd:    &mockMeterCommander{},
		billingCmd:  &mockBillingCommander{},
	}
	h.svc = NewMoveOutService(h.repo, h.contracts, h.contractCmd, h.roomCmd, h.meterCmd, h.billingCmd, noopTxManager{})
	return h
}

// --- Forward command tests ---

func TestRecordExitMeter_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, Status: MoveOutStatusPendingMeter}, nil
	}

	_, err := h.svc.RecordExitMeter(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
}

func TestGenerateSettlement_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	h := newTestHarness(uuid.New(), contractID)

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:         id,
			ContractID: contractID,
			Status:     MoveOutStatusPendingSettlement,
		}, nil
	}

	_, err := h.svc.GenerateSettlement(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.billingCmd.generateCalls != 1 {
		t.Errorf("GenerateSettlement calls: got %d, want 1", h.billingCmd.generateCalls)
	}
	if h.repo.updatedStatus != MoveOutStatusPendingPayment {
		t.Errorf("status: got %q, want PENDING_PAYMENT", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.SettlementBillID == nil {
		t.Error("settlement_bill_id must be set")
	}
	if h.repo.updatedNotice.NetAmount == nil {
		t.Error("net_amount must be set")
	}
}

func TestRecordPaymentOutcome_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	billID := uuid.New()
	netAmount := int64(150000)
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusPendingPayment,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
		}, nil
	}

	req := RecordPaymentOutcomeRequest{
		PaymentOutcome: "PAID_EXTRA",
		PaymentNote:    "ชำระเพิ่ม 1500",
	}
	_, err := h.svc.RecordPaymentOutcome(context.Background(), noticeID, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.repo.updatedStatus != MoveOutStatusReadyToClose {
		t.Errorf("status: got %q, want READY_TO_CLOSE", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.PaymentOutcome == nil || *h.repo.updatedNotice.PaymentOutcome != PaymentOutcomePaidExtra {
		t.Error("payment_outcome must be PAID_EXTRA")
	}
}

func TestCloseMoveOut_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(0)
	outcome := PaymentOutcomeZeroBalance
	scheduledDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ScheduledMoveOutDate: scheduledDate,
			Status:               MoveOutStatusReadyToClose,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
			PaymentOutcome:       &outcome,
		}, nil
	}

	_, err := h.svc.CloseMoveOut(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("status: got %q, want COMPLETED", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.ClosedAt == nil {
		t.Error("closed_at must be set")
	}
	if h.contractCmd.endCalls != 1 {
		t.Errorf("EndContract calls: got %d, want 1", h.contractCmd.endCalls)
	}
	if h.contractCmd.endContractID != contractID {
		t.Errorf("EndContract id: got %v, want %v", h.contractCmd.endContractID, contractID)
	}
	if h.roomCmd.vacantCalls != 1 {
		t.Errorf("MarkVacant calls: got %d, want 1", h.roomCmd.vacantCalls)
	}
	if h.roomCmd.vacantRoomID != roomID {
		t.Errorf("MarkVacant roomID: got %v, want %v", h.roomCmd.vacantRoomID, roomID)
	}
}

func TestCancel_HappyPath_PendingMeter(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:         id,
			ContractID: contractID,
			Status:     MoveOutStatusPendingMeter,
		}, nil
	}

	_, err := h.svc.Cancel(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.repo.updatedStatus != MoveOutStatusCancelled {
		t.Errorf("status: got %q, want CANCELLED", h.repo.updatedStatus)
	}
	if h.meterCmd.calls != 1 {
		t.Errorf("DeleteExitByRoomID calls: got %d, want 1", h.meterCmd.calls)
	}
	// No settlement to void
	if h.billingCmd.voidCalls != 0 {
		t.Errorf("VoidSettlement calls: got %d, want 0", h.billingCmd.voidCalls)
	}
}

// --- Correction command tests ---

func TestUpdateExitMeter_PendingPayment_VoidsAndReverts(t *testing.T) {
	noticeID := uuid.New()
	billID := uuid.New()
	netAmount := int64(100000)

	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusPendingPayment,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
		}, nil
	}

	_, err := h.svc.UpdateExitMeter(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must void the settlement
	if h.billingCmd.voidCalls != 1 {
		t.Errorf("VoidSettlement calls: got %d, want 1", h.billingCmd.voidCalls)
	}
	if h.billingCmd.voidBillID != billID {
		t.Errorf("VoidSettlement billID: got %v, want %v", h.billingCmd.voidBillID, billID)
	}
	// Must revert to PENDING_SETTLEMENT
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
	// Fields cleared
	if h.repo.updatedNotice.SettlementBillID != nil {
		t.Error("settlement_bill_id must be nil after revert")
	}
	if h.repo.updatedNotice.NetAmount != nil {
		t.Error("net_amount must be nil after revert")
	}
}

func TestUpdateExitMeter_PendingSettlement_NoOp(t *testing.T) {
	noticeID := uuid.New()
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, Status: MoveOutStatusPendingSettlement}, nil
	}

	_, err := h.svc.UpdateExitMeter(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No void
	if h.billingCmd.voidCalls != 0 {
		t.Errorf("VoidSettlement calls: got %d, want 0", h.billingCmd.voidCalls)
	}
	// Status stays PENDING_SETTLEMENT
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
}

func TestRegenerateSettlement_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	oldBillID := uuid.New()
	netAmount := int64(100000)
	moveOutDate := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ScheduledMoveOutDate: moveOutDate,
			Status:               MoveOutStatusPendingPayment,
			SettlementBillID:     &oldBillID,
			NetAmount:            &netAmount,
		}, nil
	}

	_, err := h.svc.RegenerateSettlement(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Old bill voided
	if h.billingCmd.voidCalls != 1 {
		t.Errorf("VoidSettlement calls: got %d, want 1", h.billingCmd.voidCalls)
	}
	if h.billingCmd.voidBillID != oldBillID {
		t.Errorf("VoidSettlement billID: got %v, want %v", h.billingCmd.voidBillID, oldBillID)
	}
	// New bill generated
	if h.billingCmd.generateCalls != 1 {
		t.Errorf("GenerateSettlement calls: got %d, want 1", h.billingCmd.generateCalls)
	}
	// Status stays PENDING_PAYMENT
	if h.repo.updatedStatus != MoveOutStatusPendingPayment {
		t.Errorf("status: got %q, want PENDING_PAYMENT (stays)", h.repo.updatedStatus)
	}
	// New bill ID is set (different from old)
	if h.repo.updatedNotice.SettlementBillID == nil {
		t.Error("settlement_bill_id must be set after regenerate")
	}
	if *h.repo.updatedNotice.SettlementBillID == oldBillID {
		t.Error("settlement_bill_id must differ from old bill")
	}
}

func TestReopenForCorrection_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	billID := uuid.New()
	netAmount := int64(0)
	outcome := PaymentOutcomeZeroBalance

	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusReadyToClose,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
			PaymentOutcome:   &outcome,
			PaymentNote:      "ok",
		}, nil
	}

	_, err := h.svc.ReopenForCorrection(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.repo.updatedStatus != MoveOutStatusPendingPayment {
		t.Errorf("status: got %q, want PENDING_PAYMENT", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.PaymentOutcome != nil {
		t.Error("payment_outcome must be cleared")
	}
	if h.repo.updatedNotice.PaymentNote != "" {
		t.Error("payment_note must be cleared")
	}
}

// --- Invariant tests ---

// D5: CloseMoveOut requires settlement_bill_id + payment_outcome.
// The domain guard (CanClose) enforces this — test that the service surfaces
// it as an AppError, not an opaque 500.
func TestCloseMoveOut_MissingSettlement_ReturnsAppError(t *testing.T) {
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:     id,
			Status: MoveOutStatusReadyToClose,
			// SettlementBillID is nil — D5 violation
		}, nil
	}

	_, err := h.svc.CloseMoveOut(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
}

// D8: Cancel with settlement → void the draft, not hard-delete.
func TestCancel_WithSettlement_VoidsDraft(t *testing.T) {
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(100000)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			ContractID:       contractID,
			Status:           MoveOutStatusPendingSettlement,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
		}, nil
	}

	_, err := h.svc.Cancel(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Settlement must be voided (D8)
	if h.billingCmd.voidCalls != 1 {
		t.Errorf("VoidSettlement calls: got %d, want 1", h.billingCmd.voidCalls)
	}
	if h.billingCmd.voidBillID != billID {
		t.Errorf("VoidSettlement billID: got %v, want %v", h.billingCmd.voidBillID, billID)
	}
	if h.billingCmd.voidReason != "CANCELLED_MOVE_OUT" {
		t.Errorf("VoidSettlement reason: got %q, want CANCELLED_MOVE_OUT", h.billingCmd.voidReason)
	}
	// EXIT reading must be soft-deleted
	if h.meterCmd.calls != 1 {
		t.Errorf("DeleteExitByRoomID calls: got %d, want 1", h.meterCmd.calls)
	}
}

// GenerateSettlement: billing error must surface as-is (not wrapped as 500).
func TestGenerateSettlement_BillingError_Propagates(t *testing.T) {
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:     id,
			Status: MoveOutStatusPendingSettlement,
		}, nil
	}
	h.billingCmd.generateFn = func(_ context.Context, _ uuid.UUID, _ time.Time) (*SettlementBillResult, error) {
		return nil, respond.ErrBadRequest.WithMessage("ไม่พบข้อมูลมิเตอร์ย้ายออก")
	}

	_, err := h.svc.GenerateSettlement(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
}

// Cancel from COMPLETED → domain guard must reject.
func TestCancel_NonCancellable_ReturnsAppError(t *testing.T) {
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, Status: MoveOutStatusCompleted}, nil
	}

	_, err := h.svc.Cancel(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("Update calls: got %d, want 0", h.repo.updateCalls)
	}
}
