package moveout

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/contract"
	"nana/internal/domain"
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
func (m *mockMoveOutRepo) FindRoomIDsWithMoveOutInMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
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
	vacantFn     func(ctx context.Context, id uuid.UUID) error
}

var _ RoomCommander = (*mockRoomCommander)(nil)

func (m *mockRoomCommander) MarkVacant(ctx context.Context, id uuid.UUID) error {
	m.vacantCalls++
	m.vacantRoomID = id
	if m.vacantFn != nil {
		return m.vacantFn(ctx, id)
	}
	return nil
}

type mockMeterCommander struct {
	deletedRoomID    uuid.UUID
	calls            int
	createExitCalls  int
	createExitRoomID uuid.UUID
	createExitFn     func(ctx context.Context, roomID uuid.UUID, date time.Time, elec, water int) error

	updateExitCalls  int
	updateExitRoomID uuid.UUID
	updateExitFn     func(ctx context.Context, roomID uuid.UUID, elecCurrent, waterCurrent *int, readingDate *time.Time, elecReplaced, waterReplaced, elecRollover, waterRollover *bool) error
}

var _ MeterReadingCommander = (*mockMeterCommander)(nil)

func (m *mockMeterCommander) CreateExitForMoveOut(ctx context.Context, roomID uuid.UUID, date time.Time, elec, water int) error {
	m.createExitCalls++
	m.createExitRoomID = roomID
	if m.createExitFn != nil {
		return m.createExitFn(ctx, roomID, date, elec, water)
	}
	return nil
}

func (m *mockMeterCommander) UpdateExitForMoveOut(ctx context.Context, roomID uuid.UUID, elecCurrent, waterCurrent *int, readingDate *time.Time, elecReplaced, waterReplaced, elecRollover, waterRollover *bool) error {
	m.updateExitCalls++
	m.updateExitRoomID = roomID
	if m.updateExitFn != nil {
		return m.updateExitFn(ctx, roomID, elecCurrent, waterCurrent, readingDate, elecReplaced, waterReplaced, elecRollover, waterRollover)
	}
	return nil
}

func (m *mockMeterCommander) DeleteExitByRoomID(_ context.Context, roomID uuid.UUID) error {
	m.calls++
	m.deletedRoomID = roomID
	return nil
}

type mockBillingCommander struct {
	generateFn     func(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error)
	regenerateFn   func(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error)
	finalizeFn     func(ctx context.Context, billID uuid.UUID) error
	voidFn         func(ctx context.Context, billID uuid.UUID, reason string) error
	correctFn      func(ctx context.Context, in CorrectSettlementInput) (*SettlementBillResult, error)

	generateCalls   int
	regenerateCalls int
	finalizeCalls   int
	voidCalls       int
	correctCalls    int
	voidBillID      uuid.UUID
	voidReason      string
	finalizeBillID  uuid.UUID
	correctInput    CorrectSettlementInput
}

var _ BillingCommander = (*mockBillingCommander)(nil)

func (m *mockBillingCommander) GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error) {
	m.generateCalls++
	if m.generateFn != nil {
		return m.generateFn(ctx, contractID, moveOutDate, rentMode)
	}
	return &SettlementBillResult{
		BillID:      uuid.New(),
		NetAmount:   150000, // 1500 baht
		DepositUsed: 500000, // 5000 baht
	}, nil
}

func (m *mockBillingCommander) RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error) {
	m.regenerateCalls++
	if m.regenerateFn != nil {
		return m.regenerateFn(ctx, existingBillID, contractID, moveOutDate, rentMode)
	}
	return &SettlementBillResult{
		BillID:      uuid.New(),
		NetAmount:   150000,
		DepositUsed: 500000,
	}, nil
}

// mockBillingQuerier satisfies BillingQuerier for tests.
type mockBillingQuerier struct{}

var _ BillingQuerier = (*mockBillingQuerier)(nil)

func (m *mockBillingQuerier) PreviewSettlementForNotice(_ context.Context, _ uuid.UUID, _ RentMode) (*SettlementPreviewResult, error) {
	return &SettlementPreviewResult{Outcome: "ZERO_BALANCE"}, nil
}

func (m *mockBillingCommander) FinalizeSettlement(_ context.Context, billID uuid.UUID) error {
	m.finalizeCalls++
	m.finalizeBillID = billID
	if m.finalizeFn != nil {
		return m.finalizeFn(context.Background(), billID)
	}
	return nil
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

func (m *mockBillingCommander) CorrectSettlement(ctx context.Context, in CorrectSettlementInput) (*SettlementBillResult, error) {
	m.correctCalls++
	m.correctInput = in
	if m.correctFn != nil {
		return m.correctFn(ctx, in)
	}
	return &SettlementBillResult{
		BillID:      uuid.New(),
		NetAmount:   75000, // 750 baht — distinct from generate/regenerate defaults
		DepositUsed: 425000,
	}, nil
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
	h.svc = NewMoveOutService(h.repo, h.contracts, h.contractCmd, h.roomCmd, h.meterCmd, h.billingCmd, &mockBillingQuerier{}, noopTxManager{})
	return h
}

// --- Forward command tests ---

func TestRecordExitMeter_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, ContractID: contractID, Status: MoveOutStatusPendingMeter}, nil
	}

	req := RecordExitMeterRequest{
		ActualMoveOutDate:  "2026-04-15",
		ElectricityCurrent: 12345,
		WaterCurrent:       678,
	}
	_, err := h.svc.RecordExitMeter(context.Background(), noticeID, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.ActualMoveOutDate == nil {
		t.Error("actual_move_out_date must be set")
	}
	if h.meterCmd.createExitCalls != 1 {
		t.Errorf("CreateExitForMoveOut calls: got %d, want 1", h.meterCmd.createExitCalls)
	}
}

func TestGenerateSettlement_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	actualDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	h := newTestHarness(uuid.New(), contractID)

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                id,
			ContractID:        contractID,
			ActualMoveOutDate: &actualDate,
			Status:            MoveOutStatusPendingSettlement,
		}, nil
	}

	_, err := h.svc.GenerateSettlement(context.Background(), noticeID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.billingCmd.generateCalls != 1 {
		t.Errorf("GenerateSettlement calls: got %d, want 1", h.billingCmd.generateCalls)
	}
	// Stays in PENDING_SETTLEMENT (not PENDING_PAYMENT) — user must finalize separately
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
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
		PaymentMethod:  "CASH",
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
	// Silent-failure case: forgetting to wire req.PaymentMethod through to
	// the domain method wouldn't be caught by status/outcome assertions alone.
	if h.repo.updatedNotice.PaymentMethod == nil || *h.repo.updatedNotice.PaymentMethod != domain.PaymentMethodCash {
		t.Errorf("payment_method: got %v, want CASH", h.repo.updatedNotice.PaymentMethod)
	}
}

func TestRecordPaymentOutcome_RequiresMethodWhenNotZero(t *testing.T) {
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
		PaymentMethod:  "", // missing — must reject
	}
	_, err := h.svc.RecordPaymentOutcome(context.Background(), uuid.New(), req)
	if err == nil {
		t.Fatal("expected error when PAID_EXTRA submitted without method")
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update should not be called on validation failure, got %d calls", h.repo.updateCalls)
	}
}

// ZERO_BALANCE normalization: even if FE sends a stale method (race after
// outcome flips), service forces method to nil so we never persist a
// nonsensical "ZERO + CASH" record.
func TestRecordPaymentOutcome_NormalizesZeroBalance(t *testing.T) {
	billID := uuid.New()
	zero := int64(0)
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusPendingPayment,
			SettlementBillID: &billID,
			NetAmount:        &zero,
		}, nil
	}

	req := RecordPaymentOutcomeRequest{
		PaymentOutcome: "ZERO_BALANCE",
		PaymentMethod:  "CASH", // stale — should be normalized away
	}
	_, err := h.svc.RecordPaymentOutcome(context.Background(), uuid.New(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedNotice.PaymentMethod != nil {
		t.Errorf("payment_method should be nil for ZERO_BALANCE; got %v", *h.repo.updatedNotice.PaymentMethod)
	}
}

func TestSkipPayment_HappyPath(t *testing.T) {
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

	_, err := h.svc.SkipPayment(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedStatus != MoveOutStatusReadyToClose {
		t.Errorf("status: got %q, want READY_TO_CLOSE", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.PaymentOutcome != nil {
		t.Errorf("PaymentOutcome must stay nil for skip; got %v", *h.repo.updatedNotice.PaymentOutcome)
	}
}

// Idempotency: a second call against an already-READY_TO_CLOSE notice must
// be a no-op — neither overwrite a recorded outcome nor flip status.
func TestSkipPayment_Idempotent(t *testing.T) {
	billID := uuid.New()
	outcome := PaymentOutcomePaidExtra
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusReadyToClose,
			SettlementBillID: &billID,
			PaymentOutcome:   &outcome,
		}, nil
	}

	_, err := h.svc.SkipPayment(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update should not be called for already-past notice; got %d", h.repo.updateCalls)
	}
}

// Idempotency is narrow: only READY_TO_CLOSE is "already skipped, double-
// click safe". COMPLETED / CANCELLED notices represent a stale-tab situation
// (another tab finished closing the move-out, or it was cancelled) — silent
// no-op would mask the inconsistency. Domain SkipPayment rejects, service
// surfaces it as 400.
func TestSkipPayment_RejectsCompleted(t *testing.T) {
	billID := uuid.New()
	outcome := PaymentOutcomePaidExtra
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusCompleted,
			SettlementBillID: &billID,
			PaymentOutcome:   &outcome,
		}, nil
	}

	_, err := h.svc.SkipPayment(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for SkipPayment on COMPLETED notice, got nil")
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update should not be called when domain rejects; got %d", h.repo.updateCalls)
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
			ActualMoveOutDate:    &scheduledDate,
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
	// CancelledAt must be stamped by the domain method (mirrors closed_at on
	// Close). FE's CancelledBanner uses this timestamp instead of the
	// drift-prone updated_at proxy.
	if h.repo.updatedNotice == nil || h.repo.updatedNotice.CancelledAt == nil {
		t.Errorf("CancelledAt: not stamped on cancel")
	}
	if h.meterCmd.calls != 1 {
		t.Errorf("DeleteExitByRoomID calls: got %d, want 1", h.meterCmd.calls)
	}
	// No settlement to void
	if h.billingCmd.voidCalls != 0 {
		t.Errorf("VoidSettlement calls: got %d, want 0", h.billingCmd.voidCalls)
	}
}

func TestFinalizeSettlement_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	billID := uuid.New()
	netAmount := int64(150000)

	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusPendingSettlement,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
		}, nil
	}

	_, err := h.svc.FinalizeSettlement(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Bill must be finalized via port
	if h.billingCmd.finalizeCalls != 1 {
		t.Errorf("FinalizeSettlement calls: got %d, want 1", h.billingCmd.finalizeCalls)
	}
	if h.billingCmd.finalizeBillID != billID {
		t.Errorf("FinalizeSettlement billID: got %v, want %v", h.billingCmd.finalizeBillID, billID)
	}
	// Status advances to PENDING_PAYMENT
	if h.repo.updatedStatus != MoveOutStatusPendingPayment {
		t.Errorf("status: got %q, want PENDING_PAYMENT", h.repo.updatedStatus)
	}
}

// --- Correction command tests ---

// --- UpdateExitMeter (full flow: meter update → void+regenerate → state) ---

func intPtr(v int) *int { return &v }

func TestUpdateExitMeter_WithBody_UpdatesMeterAndRegenerates(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	oldBillID := uuid.New()
	netAmount := int64(100000)
	actualDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ActualMoveOutDate:    &actualDate,
			ScheduledMoveOutDate: actualDate,
			Status:               MoveOutStatusPendingSettlement,
			SettlementBillID:     &oldBillID,
			NetAmount:            &netAmount,
		}, nil
	}

	req := UpdateExitMeterRequest{
		ElectricityCurrent: intPtr(5000),
		WaterCurrent:       intPtr(200),
	}
	_, err := h.svc.UpdateExitMeter(context.Background(), noticeID, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Meter port called
	if h.meterCmd.updateExitCalls != 1 {
		t.Errorf("UpdateExitForMoveOut calls: got %d, want 1", h.meterCmd.updateExitCalls)
	}
	if h.meterCmd.updateExitRoomID != roomID {
		t.Errorf("UpdateExitForMoveOut roomID: got %v, want %v", h.meterCmd.updateExitRoomID, roomID)
	}
	// Regenerate called (void + new draft)
	if h.billingCmd.regenerateCalls != 1 {
		t.Errorf("RegenerateSettlement calls: got %d, want 1", h.billingCmd.regenerateCalls)
	}
	// Status stays PENDING_SETTLEMENT
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
	// New bill attached (different from old)
	if h.repo.updatedNotice.SettlementBillID == nil {
		t.Error("settlement_bill_id must be set after regenerate")
	}
	if *h.repo.updatedNotice.SettlementBillID == oldBillID {
		t.Error("settlement_bill_id must differ from old bill")
	}
	if h.repo.updatedNotice.NetAmount == nil {
		t.Error("net_amount must be set after regenerate")
	}
}

func TestUpdateExitMeter_WithBody_NoDraft_UpdatesMeterOnly(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	actualDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ActualMoveOutDate:    &actualDate,
			ScheduledMoveOutDate: actualDate,
			Status:               MoveOutStatusPendingSettlement,
			// No SettlementBillID
		}, nil
	}

	req := UpdateExitMeterRequest{ElectricityCurrent: intPtr(5000)}
	_, err := h.svc.UpdateExitMeter(context.Background(), noticeID, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Meter updated
	if h.meterCmd.updateExitCalls != 1 {
		t.Errorf("UpdateExitForMoveOut calls: got %d, want 1", h.meterCmd.updateExitCalls)
	}
	// No void/regenerate (no draft)
	if h.billingCmd.regenerateCalls != 0 {
		t.Errorf("RegenerateSettlement calls: got %d, want 0", h.billingCmd.regenerateCalls)
	}
	if h.billingCmd.voidCalls != 0 {
		t.Errorf("VoidSettlement calls: got %d, want 0", h.billingCmd.voidCalls)
	}
	// Status stays
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
}

func TestUpdateExitMeter_WithBody_PendingPayment_RevertsAndRegenerates(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(100000)
	actualDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ActualMoveOutDate:    &actualDate,
			ScheduledMoveOutDate: actualDate,
			Status:               MoveOutStatusPendingPayment,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
		}, nil
	}

	req := UpdateExitMeterRequest{WaterCurrent: intPtr(300)}
	_, err := h.svc.UpdateExitMeter(context.Background(), noticeID, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Meter updated
	if h.meterCmd.updateExitCalls != 1 {
		t.Errorf("UpdateExitForMoveOut calls: got %d, want 1", h.meterCmd.updateExitCalls)
	}
	// Regenerate called
	if h.billingCmd.regenerateCalls != 1 {
		t.Errorf("RegenerateSettlement calls: got %d, want 1", h.billingCmd.regenerateCalls)
	}
	// Reverted to PENDING_SETTLEMENT
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT", h.repo.updatedStatus)
	}
	// New bill attached
	if h.repo.updatedNotice.SettlementBillID == nil {
		t.Error("settlement_bill_id must be set")
	}
}

// Regression: editing the meter date must sync notice.ActualMoveOutDate so
// the regenerated draft (and any future settlement preview) prorates against
// the new date — not the original one.
func TestUpdateExitMeter_DateChange_SyncsNoticeAndRegeneratesWithNewDate(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	oldBillID := uuid.New()
	netAmount := int64(100000)
	contractStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	originalDate := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	editedDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.contracts.findFn = func(_ context.Context, id uuid.UUID) (*contract.Contract, error) {
		return &contract.Contract{ID: id, RoomID: roomID, StartDate: contractStart}, nil
	}
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ActualMoveOutDate:    &originalDate,
			ScheduledMoveOutDate: originalDate,
			Status:               MoveOutStatusPendingSettlement,
			SettlementBillID:     &oldBillID,
			NetAmount:            &netAmount,
		}, nil
	}
	var capturedRegenDate time.Time
	h.billingCmd.regenerateFn = func(_ context.Context, _ uuid.UUID, _ uuid.UUID, moveOutDate time.Time, _ RentMode) (*SettlementBillResult, error) {
		capturedRegenDate = moveOutDate
		return &SettlementBillResult{BillID: uuid.New(), NetAmount: 200000, DepositUsed: 500000}, nil
	}

	editedDateStr := editedDate.Format("2006-01-02")
	req := UpdateExitMeterRequest{ReadingDateActual: &editedDateStr}
	if _, err := h.svc.UpdateExitMeter(context.Background(), noticeID, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.repo.updatedNotice.ActualMoveOutDate == nil || !h.repo.updatedNotice.ActualMoveOutDate.Equal(editedDate) {
		t.Errorf("notice.ActualMoveOutDate: got %v, want %v", h.repo.updatedNotice.ActualMoveOutDate, editedDate)
	}
	if !capturedRegenDate.Equal(editedDate) {
		t.Errorf("RegenerateSettlement moveOutDate: got %v, want %v (must use NEW date for prorate)", capturedRegenDate, editedDate)
	}
}

func TestUpdateExitMeter_MeterFailure_NoStateChange(t *testing.T) {
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(100000)
	actualDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ActualMoveOutDate:    &actualDate,
			ScheduledMoveOutDate: actualDate,
			Status:               MoveOutStatusPendingSettlement,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
		}, nil
	}
	h.meterCmd.updateExitFn = func(_ context.Context, _ uuid.UUID, _, _ *int, _ *time.Time, _, _, _, _ *bool) error {
		return errors.New("disk full")
	}

	req := UpdateExitMeterRequest{ElectricityCurrent: intPtr(9999)}
	_, err := h.svc.UpdateExitMeter(context.Background(), uuid.New(), req)
	if err == nil {
		t.Fatal("expected error from meter failure, got nil")
	}

	// Meter port was attempted
	if h.meterCmd.updateExitCalls != 1 {
		t.Errorf("UpdateExitForMoveOut calls: got %d, want 1", h.meterCmd.updateExitCalls)
	}
	// No regenerate (meter failed first)
	if h.billingCmd.regenerateCalls != 0 {
		t.Errorf("RegenerateSettlement calls: got %d, want 0", h.billingCmd.regenerateCalls)
	}
	// No repo.Update (atomicity)
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update calls: got %d, want 0", h.repo.updateCalls)
	}
}

func TestUpdateExitMeter_EmptyBody_ReturnsAppError(t *testing.T) {
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, Status: MoveOutStatusPendingSettlement}, nil
	}

	// All fields nil → should be rejected
	_, err := h.svc.UpdateExitMeter(context.Background(), uuid.New(), UpdateExitMeterRequest{})
	if err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
	if h.meterCmd.updateExitCalls != 0 {
		t.Errorf("meter port should not be called, got %d calls", h.meterCmd.updateExitCalls)
	}
}

func TestUpdateExitMeter_InvalidStatus_ReturnsAppError(t *testing.T) {
	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{ID: id, Status: MoveOutStatusCompleted}, nil
	}

	_, err := h.svc.UpdateExitMeter(context.Background(), uuid.New(), UpdateExitMeterRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update calls: got %d, want 0", h.repo.updateCalls)
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
			ActualMoveOutDate:    &moveOutDate,
			Status:               MoveOutStatusPendingSettlement,
			SettlementBillID:     &oldBillID,
			NetAmount:            &netAmount,
		}, nil
	}

	_, err := h.svc.RegenerateSettlement(context.Background(), noticeID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Uses RegenerateSettlement port (void + generate + preserve MANUAL)
	if h.billingCmd.regenerateCalls != 1 {
		t.Errorf("RegenerateSettlement calls: got %d, want 1", h.billingCmd.regenerateCalls)
	}
	// Status stays PENDING_SETTLEMENT
	if h.repo.updatedStatus != MoveOutStatusPendingSettlement {
		t.Errorf("status: got %q, want PENDING_SETTLEMENT (stays)", h.repo.updatedStatus)
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
	h.billingCmd.generateFn = func(_ context.Context, _ uuid.UUID, _ time.Time, _ RentMode) (*SettlementBillResult, error) {
		return nil, respond.ErrBadRequest.WithMessage("ไม่พบข้อมูลมิเตอร์ย้ายออก")
	}

	_, err := h.svc.GenerateSettlement(context.Background(), uuid.New(), "")
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

// --- C6/D5: Atomicity — partial failure must not leave state behind ---

// RecordExitMeter does 3 steps in one tx:
//   1. SetActualDate
//   2. CreateExitForMoveOut (meter port)
//   3. AdvanceToSettlement + repo.Update
// If step 2 fails, step 3 must NOT execute. With noopTxManager this
// verifies behavioral rollback: repo.Update is never called, so no
// state mutation is persisted.
func TestRecordExitMeter_MeterFailure_NoStateChange(t *testing.T) {
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
	// Inject failure at step 2 — meter write fails
	h.meterCmd.createExitFn = func(_ context.Context, _ uuid.UUID, _ time.Time, _, _ int) error {
		return errors.New("disk full: cannot write meter reading")
	}

	_, err := h.svc.RecordExitMeter(context.Background(), uuid.New(), RecordExitMeterRequest{
		ActualMoveOutDate:  "2026-04-15",
		ElectricityCurrent: 1135,
		WaterCurrent:       118,
	})

	// Error must surface
	if err == nil {
		t.Fatal("expected error from meter failure, got nil")
	}
	// Meter port was attempted
	if h.meterCmd.createExitCalls != 1 {
		t.Errorf("CreateExitForMoveOut calls: got %d, want 1", h.meterCmd.createExitCalls)
	}
	// Step 3 must NOT have executed — no repo.Update means no status change
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update calls: got %d, want 0 (must not persist partial state)", h.repo.updateCalls)
	}
}

// CloseMoveOutWithUnsettled: PENDING_PAYMENT → COMPLETED with payment_outcome
// untouched. End-contract + mark-vacant fire because we're transitioning to
// COMPLETED for the first time (not the idempotent path).
func TestCloseMoveOutWithUnsettled_FromPendingPayment(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(150000)
	scheduledDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ScheduledMoveOutDate: scheduledDate,
			ActualMoveOutDate:    &scheduledDate,
			Status:               MoveOutStatusPendingPayment,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
			// PaymentOutcome stays nil — this is the unsettled close path
		}, nil
	}

	_, err := h.svc.CloseMoveOutWithUnsettled(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("status: got %q, want COMPLETED", h.repo.updatedStatus)
	}
	// CRITICAL invariant: payment_outcome must NOT be modified by this path.
	if h.repo.updatedNotice.PaymentOutcome != nil {
		t.Errorf("payment_outcome must stay nil, got %v", *h.repo.updatedNotice.PaymentOutcome)
	}
	if h.repo.updatedNotice.ClosedAt == nil {
		t.Error("closed_at must be set on first close")
	}
	if h.contractCmd.endCalls != 1 {
		t.Errorf("EndContract calls: got %d, want 1", h.contractCmd.endCalls)
	}
	if h.roomCmd.vacantCalls != 1 {
		t.Errorf("MarkVacant calls: got %d, want 1", h.roomCmd.vacantCalls)
	}
}

// READY_TO_CLOSE + nil → COMPLETED. The canonical Step 4 entry path
// (operator hit "ปิดงาน (ยังไม่ชำระ)" from a previously-skipped notice).
func TestCloseMoveOutWithUnsettled_FromReadyToClose(t *testing.T) {
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	netAmount := int64(0)
	scheduledDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ContractID:           contractID,
			ScheduledMoveOutDate: scheduledDate,
			ActualMoveOutDate:    &scheduledDate,
			Status:               MoveOutStatusReadyToClose,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
		}, nil
	}

	_, err := h.svc.CloseMoveOutWithUnsettled(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("status: got %q, want COMPLETED", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.PaymentOutcome != nil {
		t.Error("payment_outcome must stay nil")
	}
	if h.contractCmd.endCalls != 1 {
		t.Errorf("EndContract calls: got %d, want 1", h.contractCmd.endCalls)
	}
}

// Idempotency: COMPLETED + nil → no-op. The contract is already ended and
// the room already vacant from the original close, so re-running those side
// effects against ENDED/VACANT state would be wrong (or error). The service
// must short-circuit before touching them.
func TestCloseMoveOutWithUnsettled_IdempotentOnCompleted(t *testing.T) {
	contractID := uuid.New()
	roomID := uuid.New()
	billID := uuid.New()
	original := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)

	h := newTestHarness(roomID, contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			ContractID:       contractID,
			Status:           MoveOutStatusCompleted,
			SettlementBillID: &billID,
			ClosedAt:         &original,
			// PaymentOutcome nil — closed-with-unsettled
		}, nil
	}

	_, err := h.svc.CloseMoveOutWithUnsettled(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error on idempotent re-call: %v", err)
	}
	// No mutation should be persisted on the idempotent path
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update calls: got %d, want 0 (idempotent)", h.repo.updateCalls)
	}
	if h.contractCmd.endCalls != 0 {
		t.Errorf("EndContract calls: got %d, want 0 (idempotent — contract already ended)", h.contractCmd.endCalls)
	}
	if h.roomCmd.vacantCalls != 0 {
		t.Errorf("MarkVacant calls: got %d, want 0 (idempotent — room already vacant)", h.roomCmd.vacantCalls)
	}
}

// Settled notices must NOT use this path — they should go through CloseMoveOut.
// Domain guard rejects via CanCloseWithUnsettled; service surfaces as AppError.
func TestCloseMoveOutWithUnsettled_SettledRejects(t *testing.T) {
	billID := uuid.New()
	outcome := PaymentOutcomePaidExtra
	scheduledDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	h := newTestHarness(uuid.New(), uuid.New())
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:                   id,
			ScheduledMoveOutDate: scheduledDate,
			ActualMoveOutDate:    &scheduledDate,
			Status:               MoveOutStatusReadyToClose,
			SettlementBillID:     &billID,
			PaymentOutcome:       &outcome,
		}, nil
	}

	_, err := h.svc.CloseMoveOutWithUnsettled(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for settled notice, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected AppError, got %T: %v", err, err)
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("repo.Update should not be called on guard rejection; got %d", h.repo.updateCalls)
	}
}

// ZERO_BALANCE invariant must be enforced on the post-close back-fill path
// too — operator opens a closed-with-unsettled notice with net_amount=0,
// FE form has stale CASH state from a prior outcome guess, hits submit. The
// service must force method=nil regardless of the entry status. This pins
// the invariant for the COMPLETED+nil entry specifically; the PENDING_PAYMENT
// case is covered by TestRecordPaymentOutcome_NormalizesZeroBalance.
func TestRecordPaymentOutcome_NormalizesZeroBalanceOnCompletedBackfill(t *testing.T) {
	billID := uuid.New()
	zero := int64(0)
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusCompleted,
			SettlementBillID: &billID,
			NetAmount:        &zero,
			// PaymentOutcome nil — closed-with-unsettled
		}, nil
	}

	req := RecordPaymentOutcomeRequest{
		PaymentOutcome: "ZERO_BALANCE",
		PaymentMethod:  "CASH", // stale — must be normalized away
	}
	_, err := h.svc.RecordPaymentOutcome(context.Background(), uuid.New(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.repo.updatedNotice.PaymentMethod != nil {
		t.Errorf("payment_method must be nil for ZERO_BALANCE on COMPLETED back-fill; got %v", *h.repo.updatedNotice.PaymentMethod)
	}
	if h.repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("status must stay COMPLETED on back-fill; got %s", h.repo.updatedStatus)
	}
}

// Phase-2 RecordPayment back-fill on COMPLETED + nil: status stays COMPLETED,
// payment fields are filled. The contract is already ended and the room
// already vacant from the original close, so RecordPayment must not invoke
// EndContract / MarkVacant — it touches only the notice row.
func TestRecordPaymentOutcome_BackfillOnCompleted(t *testing.T) {
	billID := uuid.New()
	netAmount := int64(150000)
	h := newTestHarness(uuid.New(), uuid.New())

	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return &MoveOutNotice{
			ID:               id,
			Status:           MoveOutStatusCompleted,
			SettlementBillID: &billID,
			NetAmount:        &netAmount,
		}, nil
	}

	req := RecordPaymentOutcomeRequest{
		PaymentOutcome: "PAID_EXTRA",
		PaymentMethod:  "TRANSFER",
		PaymentNote:    "บันทึกหลังปิดงาน",
	}
	_, err := h.svc.RecordPaymentOutcome(context.Background(), uuid.New(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Status must stay COMPLETED — we're not reopening anything
	if h.repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("status: got %q, want COMPLETED (stays)", h.repo.updatedStatus)
	}
	if h.repo.updatedNotice.PaymentOutcome == nil || *h.repo.updatedNotice.PaymentOutcome != PaymentOutcomePaidExtra {
		t.Error("payment_outcome must be set")
	}
	if h.repo.updatedNotice.PaymentMethod == nil || *h.repo.updatedNotice.PaymentMethod != domain.PaymentMethodTransfer {
		t.Errorf("payment_method: got %v, want TRANSFER", h.repo.updatedNotice.PaymentMethod)
	}
	// RecordPayment must NOT touch contract/room — those were already handled
	// by the original close. Re-running them would either error or no-op
	// silently against ENDED/VACANT state.
	if h.contractCmd.endCalls != 0 {
		t.Errorf("EndContract must not be called on COMPLETED back-fill; got %d", h.contractCmd.endCalls)
	}
	if h.roomCmd.vacantCalls != 0 {
		t.Errorf("MarkVacant must not be called on COMPLETED back-fill; got %d", h.roomCmd.vacantCalls)
	}
}

// CloseMoveOut does 3 steps in one tx:
//   1. notice.Close + repo.Update
//   2. contractCmd.EndContract
//   3. roomCmd.MarkVacant
// If step 3 fails, the entire tx should rollback. With noopTxManager we
// verify the service returns an error (the real TxManager would undo
// steps 1–2 via PostgreSQL ROLLBACK).
func TestCloseMoveOut_MarkVacantFailure_ReturnsError(t *testing.T) {
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
			ActualMoveOutDate:    &scheduledDate,
			Status:               MoveOutStatusReadyToClose,
			SettlementBillID:     &billID,
			NetAmount:            &netAmount,
			PaymentOutcome:       &outcome,
		}, nil
	}
	// Inject failure at step 3 — room port fails
	h.roomCmd.vacantFn = func(_ context.Context, _ uuid.UUID) error {
		return errors.New("room service unavailable")
	}

	_, err := h.svc.CloseMoveOut(context.Background(), uuid.New())

	// Error must surface
	if err == nil {
		t.Fatal("expected error from MarkVacant failure, got nil")
	}
	// In real Postgres the ROLLBACK undoes steps 1–2.
	// Here we at least verify the error propagated (service didn't swallow it).
}

// --- CorrectSettlement (Phase 2.1E-B) ---
//
// Service-layer tests pin the orchestration contract: billing port called
// with the right inputs, notice domain methods composed in the right order,
// fields end up in the right post-state, guard rejection blocks the call.
// Real-Postgres TX invariants (row lock contention, DEFERRABLE FK at COMMIT,
// audit rollback) belong in Phase 2.1E-C integration tests — not here.

// finalizedSettlementNotice builds a PENDING_PAYMENT notice with a FINALIZED
// settlement bill attached + payment outcome recorded. The standard
// "ready to be corrected" fixture for the orchestrator tests.
func finalizedSettlementNotice(noticeID, contractID, billID uuid.UUID) *MoveOutNotice {
	moveOut := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	netAmount := int64(120000) // 1200 baht — original (about to be replaced)
	outcome := PaymentOutcomePaidExtra
	method := domain.PaymentMethodCash
	return &MoveOutNotice{
		ID:                noticeID,
		ContractID:        contractID,
		Status:            MoveOutStatusPendingPayment,
		ActualMoveOutDate: &moveOut,
		SettlementBillID:  &billID,
		NetAmount:         &netAmount,
		PaymentOutcome:    &outcome,
		PaymentMethod:     &method,
		PaymentNote:       "เก็บแล้ว",
	}
}

func TestCorrectSettlement_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	oldBillID := uuid.New()
	newBillID := uuid.New()
	actor := uuid.New()

	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return finalizedSettlementNotice(id, contractID, oldBillID), nil
	}
	// Stub billing to return a deterministic new bill so we can assert the
	// notice rebinds correctly.
	h.billingCmd.correctFn = func(_ context.Context, _ CorrectSettlementInput) (*SettlementBillResult, error) {
		return &SettlementBillResult{
			BillID:      newBillID,
			NetAmount:   85000, // new net — differs from old (120000) so rebind is observable
			DepositUsed: 415000,
		}, nil
	}

	_, err := h.svc.CorrectSettlement(context.Background(), noticeID,
		CorrectSettlementRequest{CorrectionReason: "ค่าไฟผิด คำนวณใหม่"}, &actor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ── Billing port called once with the right shape ──
	if h.billingCmd.correctCalls != 1 {
		t.Fatalf("CorrectSettlement calls: got %d, want 1", h.billingCmd.correctCalls)
	}
	if h.billingCmd.correctInput.ExistingBillID != oldBillID {
		t.Errorf("input.ExistingBillID = %v, want %v", h.billingCmd.correctInput.ExistingBillID, oldBillID)
	}
	if h.billingCmd.correctInput.ContractID != contractID {
		t.Errorf("input.ContractID = %v, want %v", h.billingCmd.correctInput.ContractID, contractID)
	}
	if h.billingCmd.correctInput.CorrectionReason != "ค่าไฟผิด คำนวณใหม่" {
		t.Errorf("input.CorrectionReason = %q", h.billingCmd.correctInput.CorrectionReason)
	}
	if h.billingCmd.correctInput.RentMode != "" {
		t.Errorf("input.RentMode = %q, want \"\" (let billing default from old bill)", h.billingCmd.correctInput.RentMode)
	}
	if h.billingCmd.correctInput.Actor == nil || *h.billingCmd.correctInput.Actor != actor {
		t.Errorf("input.Actor = %v, want %v (admin attribution flows through)", h.billingCmd.correctInput.Actor, actor)
	}

	// ── Notice state flipped correctly ──
	if h.repo.updateCalls != 1 {
		t.Fatalf("notice Update calls: got %d, want 1", h.repo.updateCalls)
	}
	got := h.repo.updatedNotice
	if got.Status != MoveOutStatusPendingSettlement {
		t.Errorf("status = %s, want PENDING_SETTLEMENT (workflow rewound)", got.Status)
	}
	if got.SettlementBillID == nil || *got.SettlementBillID != newBillID {
		t.Errorf("settlement_bill_id = %v, want %v (rebind to new bill)", got.SettlementBillID, newBillID)
	}
	if got.NetAmount == nil || *got.NetAmount != 85000 {
		t.Errorf("net_amount = %v, want 85000 (recomputed)", got.NetAmount)
	}
	// Payment metadata wiped (audit honesty — design lock decision #6)
	if got.PaymentOutcome != nil {
		t.Errorf("payment_outcome = %v, want nil (cleared)", got.PaymentOutcome)
	}
	if got.PaymentMethod != nil {
		t.Errorf("payment_method = %v, want nil (cleared)", got.PaymentMethod)
	}
	if got.PaymentNote != "" {
		t.Errorf("payment_note = %q, want \"\" (cleared)", got.PaymentNote)
	}
}

func TestCorrectSettlement_RejectsCompleted(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	billID := uuid.New()

	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		// COMPLETED notice — blocked in v1 per design lock decision #1
		// (Phase 2.1F backlog: reverting contract.ENDED + room.VACANT).
		n := finalizedSettlementNotice(id, contractID, billID)
		n.Status = MoveOutStatusCompleted
		return n, nil
	}

	_, err := h.svc.CorrectSettlement(context.Background(), noticeID,
		CorrectSettlementRequest{CorrectionReason: "should not pass"}, nil)
	if err == nil {
		t.Fatal("expected guard rejection, got nil")
	}
	appErr, ok := respond.Is(err)
	if !ok {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("HTTP status = %d, want 400", appErr.HTTPStatus)
	}

	// Billing must NOT be called when the workflow guard rejects.
	if h.billingCmd.correctCalls != 0 {
		t.Errorf("billing.CorrectSettlement calls = %d, want 0 (guarded before billing)", h.billingCmd.correctCalls)
	}
	// Notice must NOT be Updated either — no half-applied state.
	if h.repo.updateCalls != 0 {
		t.Errorf("notice Update calls = %d, want 0 (no mutation on guard reject)", h.repo.updateCalls)
	}
}

func TestCorrectSettlement_BillingErrorPropagates(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	billID := uuid.New()

	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		return finalizedSettlementNotice(id, contractID, billID), nil
	}
	billErr := respond.ErrBadRequest.WithMessage("บิลถูกชำระแล้ว") // mirrors billing CanCorrect PAID reject
	h.billingCmd.correctFn = func(_ context.Context, _ CorrectSettlementInput) (*SettlementBillResult, error) {
		return nil, billErr
	}

	_, err := h.svc.CorrectSettlement(context.Background(), noticeID,
		CorrectSettlementRequest{CorrectionReason: "ไม่สำเร็จ"}, nil)
	if err == nil {
		t.Fatal("expected error from billing, got nil")
	}
	if !errors.Is(err, billErr) {
		// AppError equality via errors.Is — surface the billing reject
		// verbatim so the FE shows the right Thai message.
		t.Errorf("err = %v, want billing's AppError to propagate", err)
	}

	// Billing called once (where it failed).
	if h.billingCmd.correctCalls != 1 {
		t.Errorf("billing.CorrectSettlement calls = %d, want 1", h.billingCmd.correctCalls)
	}
	// Notice Update must NOT run — the orchestrator returns before reaching
	// the notice rewind block, so no partial state escapes the tx closure.
	if h.repo.updateCalls != 0 {
		t.Errorf("notice Update calls = %d, want 0 (billing error must short-circuit before notice rewind)", h.repo.updateCalls)
	}
}

func TestCorrectSettlement_RejectsMissingSettlementBill(t *testing.T) {
	// The status guard (CanDowngradeToPendingSettlement) accepts a
	// PENDING_PAYMENT notice. The second guard (SettlementBillID != nil)
	// catches the unreachable-but-defensive case where a notice somehow
	// reaches PENDING_PAYMENT without a bill attached. Pinning this here
	// keeps the guard from rotting into dead code on future refactors.
	noticeID := uuid.New()
	contractID := uuid.New()

	h := newTestHarness(uuid.New(), contractID)
	h.repo.findForUpdateFn = func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
		n := finalizedSettlementNotice(id, contractID, uuid.Nil)
		n.SettlementBillID = nil
		return n, nil
	}

	_, err := h.svc.CorrectSettlement(context.Background(), noticeID,
		CorrectSettlementRequest{CorrectionReason: "should not pass"}, nil)
	if err == nil {
		t.Fatal("expected guard rejection on nil settlement_bill_id, got nil")
	}
	if h.billingCmd.correctCalls != 0 {
		t.Errorf("billing called %d times, want 0 (guard fires before billing)", h.billingCmd.correctCalls)
	}
	if h.repo.updateCalls != 0 {
		t.Errorf("notice Update calls = %d, want 0 (no mutation on guard reject)", h.repo.updateCalls)
	}
}
