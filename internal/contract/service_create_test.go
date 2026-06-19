package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/tenant"

	"github.com/google/uuid"
)

// service_create_test.go pins the transaction ordering invariant for
// contract.Create: the contract row must be inserted BEFORE the room flips
// to OCCUPIED, and both calls must run inside the same transaction context
// returned by TxManager.RunInTx. The downstream impact is operational —
// a reorder could leave a room OCCUPIED with no contract row (or vice
// versa) and only surface in smoke tests.
//
// Per testing-strategy.md "Cross-feature side-effect order in tx" — this
// is exactly the case the rule says to test (would otherwise fail silently).

// --- Mocks (hand-written; compile-time interface guard below) ---

type mockContractRepo struct {
	createCalls    int
	createErr      error
	createCtx      context.Context
	hasActiveValue bool
}

var _ ContractRepository = (*mockContractRepo)(nil)

func (m *mockContractRepo) FindAll(_ context.Context, _ ContractListParams) ([]ContractWithRelations, int64, error) {
	return nil, 0, nil
}
func (m *mockContractRepo) FindByID(_ context.Context, id uuid.UUID) (*ContractWithRelations, error) {
	return &ContractWithRelations{Contract: Contract{ID: id, Status: ContractStatusActive}}, nil
}
func (m *mockContractRepo) FindByIDSimple(_ context.Context, _ uuid.UUID) (*Contract, error) {
	return nil, nil
}
func (m *mockContractRepo) Create(ctx context.Context, c *Contract) error {
	m.createCalls++
	m.createCtx = ctx
	if m.createErr != nil {
		return m.createErr
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}
func (m *mockContractRepo) Update(_ context.Context, _ *Contract) error { return nil }
func (m *mockContractRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockContractRepo) HasActiveByRoomID(_ context.Context, _ uuid.UUID) (bool, error) {
	return m.hasActiveValue, nil
}
func (m *mockContractRepo) HasActiveByTenantID(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}
func (m *mockContractRepo) FindActiveContractStartDatesByRoomIDs(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	return nil, nil
}
func (m *mockContractRepo) FindByRoomIDWithTenants(_ context.Context, _ uuid.UUID) ([]ContractTenantSummary, error) {
	return nil, nil
}
func (m *mockContractRepo) EndContract(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

type mockRoomQuerier struct {
	room *room.Room
}

var _ RoomQuerier = (*mockRoomQuerier)(nil)

func (m *mockRoomQuerier) FindByID(_ context.Context, _ uuid.UUID) (*room.Room, error) {
	if m.room == nil {
		return nil, errors.New("room not found")
	}
	return m.room, nil
}

type mockRoomCommander struct {
	markCalls       int
	markCtx         context.Context
	markErr         error
	calledAfterRepo bool
	repo            *mockContractRepo
}

var _ RoomCommander = (*mockRoomCommander)(nil)

func (m *mockRoomCommander) MarkOccupied(ctx context.Context, _ uuid.UUID) error {
	m.markCalls++
	m.markCtx = ctx
	if m.repo != nil && m.repo.createCalls > 0 {
		m.calledAfterRepo = true
	}
	return m.markErr
}

type mockTenantQuerier struct {
	t *tenant.Tenant
}

var _ TenantQuerier = (*mockTenantQuerier)(nil)

func (m *mockTenantQuerier) FindByID(_ context.Context, id uuid.UUID) (*tenant.Tenant, error) {
	if m.t == nil {
		return &tenant.Tenant{ID: id}, nil
	}
	return m.t, nil
}

// mockTxManager runs fn with a derived context so the test can assert that
// the same txCtx reached every collaborator (proves propagation, not just
// "fn was called").
type mockTxManager struct {
	runs    int
	lastCtx context.Context
}

var _ database.TxManager = (*mockTxManager)(nil)

func (m *mockTxManager) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	m.runs++
	type txKey struct{}
	txCtx := context.WithValue(ctx, txKey{}, "tx")
	m.lastCtx = txCtx
	return fn(txCtx)
}

// --- Tests ---

func TestContractCreate_RoomMarkOccupiedRunsAfterRepoCreateInSameTxContext(t *testing.T) {
	repo := &mockContractRepo{}
	rooms := &mockRoomQuerier{
		room: &room.Room{ID: uuid.New(), Status: room.RoomStatusVacant},
	}
	roomCmd := &mockRoomCommander{repo: repo}
	tenants := &mockTenantQuerier{}
	tx := &mockTxManager{}

	svc := NewContractService(repo, rooms, roomCmd, tenants, tx)

	req := CreateContractRequest{
		TenantID:               uuid.New().String(),
		RoomID:                 rooms.room.ID.String(),
		StartDate:              "2026-04-01",
		MinMonths:              6,
		MonthlyRent:            3000,
		DepositAmount:          3000,
		ElectricityRatePerUnit: 8,
		WaterRatePerUnit:       18,
	}

	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if tx.runs != 1 {
		t.Errorf("RunInTx invocations = %d, want 1", tx.runs)
	}
	if repo.createCalls != 1 {
		t.Errorf("repo.Create invocations = %d, want 1", repo.createCalls)
	}
	if roomCmd.markCalls != 1 {
		t.Errorf("roomCmd.MarkOccupied invocations = %d, want 1", roomCmd.markCalls)
	}
	if !roomCmd.calledAfterRepo {
		t.Error("roomCmd.MarkOccupied ran BEFORE repo.Create — ordering invariant broken")
	}

	// Same txCtx must reach both collaborators — proves Service propagates
	// the RunInTx callback's ctx, not the outer caller ctx (the bug shape
	// flagged in cross-feature-patterns.md "Context Propagation").
	if repo.createCtx != tx.lastCtx {
		t.Error("repo.Create received a different ctx than tx.RunInTx provided — call would escape the transaction")
	}
	if roomCmd.markCtx != tx.lastCtx {
		t.Error("roomCmd.MarkOccupied received a different ctx than tx.RunInTx provided — call would escape the transaction")
	}
}

func TestContractCreate_RepoErrorAbortsBeforeRoomMark(t *testing.T) {
	repo := &mockContractRepo{createErr: errors.New("boom")}
	rooms := &mockRoomQuerier{
		room: &room.Room{ID: uuid.New(), Status: room.RoomStatusVacant},
	}
	roomCmd := &mockRoomCommander{repo: repo}
	tenants := &mockTenantQuerier{}
	tx := &mockTxManager{}

	svc := NewContractService(repo, rooms, roomCmd, tenants, tx)

	_, err := svc.Create(context.Background(), CreateContractRequest{
		TenantID:               uuid.New().String(),
		RoomID:                 rooms.room.ID.String(),
		StartDate:              "2026-04-01",
		MinMonths:              6,
		MonthlyRent:            3000,
		DepositAmount:          3000,
		ElectricityRatePerUnit: 8,
		WaterRatePerUnit:       18,
	})
	if err == nil {
		t.Fatal("expected error from repo failure, got nil")
	}
	if roomCmd.markCalls != 0 {
		t.Errorf("roomCmd.MarkOccupied should NOT fire when repo.Create fails; calls = %d", roomCmd.markCalls)
	}
}

func TestContractCreate_RoomMarkErrorSurfacesFromTx(t *testing.T) {
	repo := &mockContractRepo{}
	rooms := &mockRoomQuerier{
		room: &room.Room{ID: uuid.New(), Status: room.RoomStatusVacant},
	}
	roomCmd := &mockRoomCommander{repo: repo, markErr: errors.New("mark failed")}
	tenants := &mockTenantQuerier{}
	tx := &mockTxManager{}

	svc := NewContractService(repo, rooms, roomCmd, tenants, tx)

	_, err := svc.Create(context.Background(), CreateContractRequest{
		TenantID:               uuid.New().String(),
		RoomID:                 rooms.room.ID.String(),
		StartDate:              "2026-04-01",
		MinMonths:              6,
		MonthlyRent:            3000,
		DepositAmount:          3000,
		ElectricityRatePerUnit: 8,
		WaterRatePerUnit:       18,
	})
	if err == nil {
		t.Fatal("expected error from roomCmd failure, got nil")
	}
	// Even if MarkOccupied fails, repo.Create must have been called — that's
	// what gives the real TxManager a chance to roll back the insert.
	if repo.createCalls != 1 {
		t.Errorf("repo.Create should have run inside the failing tx; calls = %d", repo.createCalls)
	}
}
