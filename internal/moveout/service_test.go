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
	listActiveFn    func(ctx context.Context, params MoveOutQueueParams) ([]NoticeWithMeterFlag, error)
	listHistoryFn   func(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error)

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
func (m *mockMoveOutRepo) ListActiveWithMeterFlag(ctx context.Context, params MoveOutQueueParams) ([]NoticeWithMeterFlag, error) {
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
	m.updatedStatus = notice.Status // snapshot value, avoid pointer-mutation races
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

// noopTxManager runs the callback with the same context — sufficient for mock repos
// that ignore transaction state.
//
// Limitation: does NOT simulate rollback. If a future test needs to verify that a
// failing callback reverts earlier mutations, the mock must be extended with a
// rollback flag (e.g. clear tracked state on error) — current mocks accumulate
// state regardless of callback outcome. Tests today only assert pre-failure counters
// (e.g. updateCalls == 0 when domain validation fails before Update), which is
// still correct because the service fails fast before the first mutation.
type noopTxManager struct{}

func (noopTxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// --- Tests ---

func TestMoveOutService_Cancel_SoftDeletesExitReading(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()

	repo := &mockMoveOutRepo{
		findForUpdateFn: func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
			return &MoveOutNotice{
				ID:         id,
				ContractID: contractID,
				Status:     MoveOutStatusPending,
			}, nil
		},
	}
	contracts := &mockContractQuerier{
		findFn: func(_ context.Context, id uuid.UUID) (*contract.Contract, error) {
			return &contract.Contract{ID: id, RoomID: roomID}, nil
		},
	}
	meter := &mockMeterCommander{}

	svc := NewMoveOutService(
		repo,
		contracts,
		&mockContractCommander{},
		&mockRoomCommander{},
		meter,
		noopTxManager{},
	)

	_, err := svc.Cancel(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("Cancel: unexpected error: %v", err)
	}

	// Notice must transition to CANCELLED
	if repo.updateCalls != 1 {
		t.Errorf("Update calls: got %d, want 1", repo.updateCalls)
	}
	if repo.updatedStatus != MoveOutStatusCancelled {
		t.Errorf("notice status: got %q, want CANCELLED", repo.updatedStatus)
	}

	// EXIT reading must be soft-deleted for the contract's room
	if meter.calls != 1 {
		t.Errorf("DeleteExitByRoomID calls: got %d, want 1", meter.calls)
	}
	if meter.deletedRoomID != roomID {
		t.Errorf("DeleteExitByRoomID roomID: got %v, want %v", meter.deletedRoomID, roomID)
	}

	// Re-fetch with relations must use the correct id
	if repo.findByIDCalledWith != noticeID {
		t.Errorf("FindByID called with %v, want %v", repo.findByIDCalledWith, noticeID)
	}
}

// Cancelling a notice that is not PENDING must surface a business error
// (AppError → 400), NOT an opaque wrapped error (→ 500). This guards the
// respond.Is() propagation guard inside the tx closure.
func TestMoveOutService_Cancel_NonPendingReturnsAppError(t *testing.T) {
	repo := &mockMoveOutRepo{
		findForUpdateFn: func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
			return &MoveOutNotice{
				ID:     id,
				Status: MoveOutStatusCompleted, // already done, cannot cancel
			}, nil
		},
	}

	svc := NewMoveOutService(
		repo,
		&mockContractQuerier{
			findFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) {
				return nil, errMockUnused // should not be reached
			},
		},
		&mockContractCommander{},
		&mockRoomCommander{},
		&mockMeterCommander{},
		noopTxManager{},
	)

	_, err := svc.Cancel(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("Cancel: expected error, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("expected *respond.AppError, got %T: %v", err, err)
	}

	// Update must NOT have been called
	if repo.updateCalls != 0 {
		t.Errorf("Update calls: got %d, want 0 (domain validation should fail first)", repo.updateCalls)
	}
}

// Complete is a critical business-state transition: it ends the contract,
// marks the room vacant, and moves the notice to COMPLETED — all atomically.
// Happy-path test asserts every cross-feature side effect fires exactly once
// with the correct identifiers.
func TestMoveOutService_Complete_HappyPath(t *testing.T) {
	noticeID := uuid.New()
	contractID := uuid.New()
	roomID := uuid.New()
	scheduledDate := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	repo := &mockMoveOutRepo{
		findForUpdateFn: func(_ context.Context, id uuid.UUID) (*MoveOutNotice, error) {
			return &MoveOutNotice{
				ID:                   id,
				ContractID:           contractID,
				ScheduledMoveOutDate: scheduledDate,
				Status:               MoveOutStatusPending,
			}, nil
		},
	}
	contracts := &mockContractQuerier{
		findFn: func(_ context.Context, id uuid.UUID) (*contract.Contract, error) {
			return &contract.Contract{ID: id, RoomID: roomID}, nil
		},
	}
	contractCmd := &mockContractCommander{}
	roomCmd := &mockRoomCommander{}

	svc := NewMoveOutService(
		repo,
		contracts,
		contractCmd,
		roomCmd,
		&mockMeterCommander{},
		noopTxManager{},
	)

	_, err := svc.Complete(context.Background(), noticeID)
	if err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}

	// Notice transition
	if repo.updateCalls != 1 {
		t.Errorf("Update calls: got %d, want 1", repo.updateCalls)
	}
	if repo.updatedStatus != MoveOutStatusCompleted {
		t.Errorf("notice status: got %q, want COMPLETED", repo.updatedStatus)
	}

	// Contract ended with scheduled move-out date
	if contractCmd.endCalls != 1 {
		t.Errorf("EndContract calls: got %d, want 1", contractCmd.endCalls)
	}
	if contractCmd.endContractID != contractID {
		t.Errorf("EndContract id: got %v, want %v", contractCmd.endContractID, contractID)
	}
	if !contractCmd.endContractDate.Equal(scheduledDate) {
		t.Errorf("EndContract date: got %v, want %v", contractCmd.endContractDate, scheduledDate)
	}

	// Room marked vacant
	if roomCmd.vacantCalls != 1 {
		t.Errorf("MarkVacant calls: got %d, want 1", roomCmd.vacantCalls)
	}
	if roomCmd.vacantRoomID != roomID {
		t.Errorf("MarkVacant roomID: got %v, want %v", roomCmd.vacantRoomID, roomID)
	}

	// Response re-fetched by correct id
	if repo.findByIDCalledWith != noticeID {
		t.Errorf("FindByID called with %v, want %v", repo.findByIDCalledWith, noticeID)
	}
}
