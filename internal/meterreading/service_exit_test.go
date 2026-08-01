package meterreading

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/contract"
	"nana/internal/room"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Hand-written mocks (compile-time interface checks) ---

// mockMeterRepo implements MeterReadingRepository for service tests.
type mockMeterRepo struct {
	findLatestByRoomIDFn       func(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	hasMonthlyByRoomAndMonthFn func(ctx context.Context, roomID uuid.UUID, month string) (bool, error)
	createFn                   func(ctx context.Context, reading *MeterReading) error
	findByIDFn                 func(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)

	// track calls
	createdReading *MeterReading
}

var _ MeterReadingRepository = (*mockMeterRepo)(nil)

func (m *mockMeterRepo) FindAll(_ context.Context, _ uuid.UUID, _ ListParams) ([]MeterReadingWithRoom, int64, error) {
	return nil, 0, nil
}
func (m *mockMeterRepo) FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	// Default: return minimal response using created reading
	if m.createdReading != nil {
		return &MeterReadingWithRoom{MeterReading: *m.createdReading, RoomNumber: "101"}, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterRepo) FindByIDSimple(_ context.Context, _ uuid.UUID) (*MeterReading, error) {
	return nil, nil
}
func (m *mockMeterRepo) FindByRoomID(_ context.Context, _ uuid.UUID, _ pagination.PaginationParams) ([]MeterReading, int64, error) {
	return nil, 0, nil
}
func (m *mockMeterRepo) FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	if m.findLatestByRoomIDFn != nil {
		return m.findLatestByRoomIDFn(ctx, roomID)
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterRepo) FindLatestPerRoom(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]MeterReadingWithRoom, error) {
	return map[uuid.UUID]MeterReadingWithRoom{}, nil
}
func (m *mockMeterRepo) FindLatestByRoomIDBeforeDate(_ context.Context, _ uuid.UUID, _ time.Time, _ *uuid.UUID) (*MeterReading, error) {
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterRepo) FindRecentByRoomIDs(_ context.Context, _ []uuid.UUID, _ int) (map[uuid.UUID][]MeterReading, error) {
	return map[uuid.UUID][]MeterReading{}, nil
}
func (m *mockMeterRepo) HasConsumptionMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (bool, error) {
	if m.hasMonthlyByRoomAndMonthFn != nil {
		return m.hasMonthlyByRoomAndMonthFn(ctx, roomID, month)
	}
	return false, nil
}
func (m *mockMeterRepo) FindMonthlyByRoomsAndMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*MeterReading, error) {
	return map[uuid.UUID]*MeterReading{}, nil
}
func (m *mockMeterRepo) FindConsumptionMonthlyByRoomsAndMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*MeterReading, error) {
	return map[uuid.UUID]*MeterReading{}, nil
}
func (m *mockMeterRepo) FindConsumptionMonthlyByRoomAndMonth(_ context.Context, _ uuid.UUID, _ string) (*MeterReading, error) {
	return nil, nil
}
func (m *mockMeterRepo) Create(ctx context.Context, reading *MeterReading) error {
	m.createdReading = reading
	if m.createFn != nil {
		return m.createFn(ctx, reading)
	}
	return nil
}
func (m *mockMeterRepo) Update(_ context.Context, _ *MeterReading) error { return nil }
func (m *mockMeterRepo) FindExitByRoomID(_ context.Context, _ uuid.UUID) (*MeterReading, error) {
	return nil, gorm.ErrRecordNotFound
}
func (m *mockMeterRepo) DeleteExitByRoomID(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockMeterRepo) FindReplacementAnchorsByRoomAndMonth(_ context.Context, _ uuid.UUID, _ string) ([]*MeterReading, error) {
	return nil, nil
}
func (m *mockMeterRepo) FindReplacementAnchorsByRoomsAndMonth(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID][]*MeterReading, error) {
	return nil, nil
}

func (m *mockMeterRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockMeterRepo) FindBaselineCorrectionByID(_ context.Context, _, _, _ uuid.UUID) (*MeterReading, error) {
	return nil, gorm.ErrRecordNotFound
}

func (m *mockMeterRepo) FindLatestBaselineCorrectionByRoomID(_ context.Context, _ uuid.UUID) (*MeterReading, error) {
	return nil, gorm.ErrRecordNotFound
}

func (m *mockMeterRepo) FindPendingBaselineCorrectionsByRoomID(_ context.Context, _ uuid.UUID) ([]MeterReading, error) {
	return nil, nil
}

// mockRoomQuerier implements RoomQuerier.
type mockRoomQuerier struct {
	room *room.Room
}

var _ RoomQuerier = (*mockRoomQuerier)(nil)

func (m *mockRoomQuerier) FindByID(_ context.Context, _ uuid.UUID) (*room.Room, error) {
	if m.room != nil {
		return m.room, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (m *mockRoomQuerier) FindRoomIDsByApartment(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// mockMoveOutChecker implements MoveOutChecker.
type mockMoveOutChecker struct {
	pendingRoomIDs map[uuid.UUID]bool
}

var _ MoveOutChecker = (*mockMoveOutChecker)(nil)

func (m *mockMoveOutChecker) FindRoomIDsWithPendingNotice(_ context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if m.pendingRoomIDs == nil {
		return map[uuid.UUID]bool{}, nil
	}
	result := make(map[uuid.UUID]bool)
	for _, id := range roomIDs {
		if m.pendingRoomIDs[id] {
			result[id] = true
		}
	}
	return result, nil
}
func (m *mockMoveOutChecker) HasCompletedMoveOut(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

// mockContractQuerier implements ContractQuerier (unused in EXIT tests, but required).
type mockContractQuerier struct{}

var _ ContractQuerier = (*mockContractQuerier)(nil)

func (m *mockContractQuerier) FindActiveContractStartDatesByRoomIDs(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	return map[uuid.UUID]time.Time{}, nil
}
func (m *mockContractQuerier) FindByRoomIDWithTenants(_ context.Context, _ uuid.UUID) ([]contract.ContractTenantSummary, error) {
	return nil, nil
}
func (m *mockContractQuerier) FindContractIDByRoomAndMonth(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (m *mockContractQuerier) FindActiveContractIDByRoomID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// mockBillingApplied implements BillingApplicationChecker (no-op for EXIT tests).
type mockBillingApplied struct{}

var _ BillingApplicationChecker = (*mockBillingApplied)(nil)

func (m *mockBillingApplied) HasNonVoidAdjustmentLine(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

func (m *mockBillingApplied) HasNonVoidAdjustmentLineForUtility(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
	return false, nil
}

// mockTxManager runs fn directly (no actual transaction).
type mockTxManager struct{}

func (m *mockTxManager) RunInTx(_ context.Context, fn func(ctx context.Context) error) error {
	return fn(context.Background())
}

// --- Test fixture ---

var (
	testApartmentID = uuid.New()
	testRoomID      = uuid.New()
)

func newTestRoom() *room.Room {
	return &room.Room{
		ID:          testRoomID,
		ApartmentID: testApartmentID,
	}
}

func newTestService(repo *mockMeterRepo, moveOuts *mockMoveOutChecker) MeterReadingService {
	return NewMeterReadingService(
		repo,
		&mockRoomQuerier{room: newTestRoom()},
		&mockContractQuerier{},
		moveOuts,
		&mockBillingApplied{},
		&mockTxManager{},
	)
}

func exitReq(date string, elec, water int) ExitCreateRequest {
	return ExitCreateRequest{
		RoomID:             testRoomID.String(),
		ReadingDateActual:  date,
		ElectricityCurrent: elec,
		WaterCurrent:       water,
	}
}

// --- CreateExitReading tests ---

func TestCreateExitReading_HappyPath(t *testing.T) {
	repo := &mockMeterRepo{}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("2026-04-15", 200, 80))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.createdReading == nil {
		t.Fatal("expected reading to be created")
	}
	if repo.createdReading.ReadingType != ReadingTypeExit {
		t.Errorf("ReadingType = %s, want EXIT", repo.createdReading.ReadingType)
	}
	if repo.createdReading.BillingMonth != nil {
		t.Error("BillingMonth should be nil for EXIT")
	}
}

func TestCreateExitReading_RejectNoPendingMoveOut(t *testing.T) {
	repo := &mockMeterRepo{}
	moveOuts := &mockMoveOutChecker{} // no pending rooms
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("2026-04-15", 200, 80))
	if err == nil {
		t.Fatal("expected error for no pending move-out")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", appErr.HTTPStatus)
	}
}

func TestCreateExitReading_RejectDuplicateExit(t *testing.T) {
	// Latest reading is already EXIT → reject
	exitLatest := makeExitLatest(testRoomID, "2026-04-10", 180, 70)
	repo := &mockMeterRepo{
		findLatestByRoomIDFn: func(_ context.Context, _ uuid.UUID) (*MeterReading, error) {
			return exitLatest, nil
		},
	}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("2026-04-15", 200, 80))
	if err == nil {
		t.Fatal("expected error for duplicate EXIT")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409 (conflict)", appErr.HTTPStatus)
	}
}

func TestCreateExitReading_RejectMonthlyExistsInSameMonth(t *testing.T) {
	// MONTHLY reading exists for same month → reject
	repo := &mockMeterRepo{
		hasMonthlyByRoomAndMonthFn: func(_ context.Context, _ uuid.UUID, month string) (bool, error) {
			if month == "2026-04" {
				return true, nil
			}
			return false, nil
		},
	}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("2026-04-15", 200, 80))
	if err == nil {
		t.Fatal("expected error for MONTHLY same month")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409 (conflict)", appErr.HTTPStatus)
	}
}

func TestCreateExitReading_AutoPopulateFromLatestMonthly(t *testing.T) {
	// Latest is a MONTHLY → EXIT should inherit previous values
	monthlyLatest := makeLatest(testRoomID, "2026-03", 500, 200)
	repo := &mockMeterRepo{
		findLatestByRoomIDFn: func(_ context.Context, _ uuid.UUID) (*MeterReading, error) {
			return monthlyLatest, nil
		},
	}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("2026-04-15", 600, 250))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	created := repo.createdReading
	if created.ElectricityPrevious != 500 {
		t.Errorf("ElectricityPrevious = %d, want 500", created.ElectricityPrevious)
	}
	if created.WaterPrevious != 200 {
		t.Errorf("WaterPrevious = %d, want 200", created.WaterPrevious)
	}
}

func TestCreateExitReading_InvalidDate(t *testing.T) {
	repo := &mockMeterRepo{}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	_, err := svc.CreateExitReading(context.Background(), testApartmentID, exitReq("not-a-date", 200, 80))
	if err == nil {
		t.Fatal("expected error for invalid date")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", appErr.HTTPStatus)
	}
}

// --- BatchCreate move-out exclusion test ---

func TestBatchCreate_RejectRoomWithPendingMoveOut(t *testing.T) {
	repo := &mockMeterRepo{}
	moveOuts := &mockMoveOutChecker{pendingRoomIDs: map[uuid.UUID]bool{testRoomID: true}}
	svc := newTestService(repo, moveOuts)

	req := BatchCreateRequest{
		BillingMonth: "2026-04",
		Items: []BatchCreateItem{
			{RoomID: testRoomID.String(), ElectricityCurrent: 200, WaterCurrent: 80},
		},
	}
	_, err := svc.BatchCreate(context.Background(), testApartmentID, req)
	if err == nil {
		t.Fatal("expected error for room with pending move-out")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", appErr.HTTPStatus)
	}
}
