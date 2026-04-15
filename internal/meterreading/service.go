package meterreading

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"nana/internal/contract"
	"nana/internal/shared/database"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// isValidBillingMonth validates "YYYY-MM" format.
func isValidBillingMonth(s string) bool {
	if len(s) != 7 || s[4] != '-' {
		return false
	}
	_, err := time.Parse("2006-01", s)
	return err == nil
}

type MeterReadingService interface {
	List(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req CreateRequest) (*MeterReadingWithRoom, error)
	CreateExitReading(ctx context.Context, apartmentID uuid.UUID, req ExitCreateRequest) (*MeterReadingWithRoom, error)
	BatchCreate(ctx context.Context, apartmentID uuid.UUID, req BatchCreateRequest) ([]MeterReadingWithRoom, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateRequest) (*MeterReadingWithRoom, error)
	GetLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	GetLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error)
	GetRoomHistory(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReadingWithTenant, int64, error)
	GetBaselines(ctx context.Context, apartmentID uuid.UUID) (map[uuid.UUID]RoomBaseline, error)

	// --- Move-out workflow ports ---

	// CreateExitForMoveOut creates an EXIT reading as part of the move-out workflow.
	// Must be called within the caller's transaction context.
	CreateExitForMoveOut(ctx context.Context, roomID uuid.UUID, readingDate time.Time, elecCurrent, waterCurrent int) error

	// DeleteExitByRoomID soft-deletes the room's active EXIT reading.
	DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error
}

type meterReadingService struct {
	repo      MeterReadingRepository
	rooms     RoomQuerier
	contracts ContractQuerier
	moveOuts  MoveOutChecker
	tx        database.TxManager
}

func NewMeterReadingService(
	repo MeterReadingRepository,
	rooms RoomQuerier,
	contracts ContractQuerier,
	moveOuts MoveOutChecker,
	tx database.TxManager,
) MeterReadingService {
	return &meterReadingService{
		repo:      repo,
		rooms:     rooms,
		contracts: contracts,
		moveOuts:  moveOuts,
		tx:        tx,
	}
}

func (s *meterReadingService) List(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error) {
	return s.repo.FindAll(ctx, apartmentID, params)
}

func (s *meterReadingService) GetByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error) {
	reading, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์")
	}
	return reading, nil
}

func (s *meterReadingService) Create(ctx context.Context, apartmentID uuid.UUID, req CreateRequest) (*MeterReadingWithRoom, error) {
	roomID, err := uuid.Parse(req.RoomID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รหัสห้องไม่ถูกต้อง")
	}
	if !isValidBillingMonth(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบเดือนไม่ถูกต้อง (YYYY-MM)")
	}

	// Validate room belongs to apartment
	r, err := s.rooms.FindByID(ctx, roomID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}
	if r.ApartmentID != apartmentID {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้")
	}

	// Fetch latest for auto-populate
	latest := s.findLatestOrNil(ctx, roomID)

	// Domain: create + validate
	reading, err := NewReading(roomID, req.BillingMonth, req.ElectricityCurrent, req.WaterCurrent, latest, req.ReplacedFlags(), req.RolloverFlags())
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	// Compute anomaly before persist (1 INSERT, not create+update)
	baselines, _ := s.getBaselinesByRoomIDs(ctx, []uuid.UUID{roomID})
	if bl, ok := baselines[roomID]; ok {
		reading.ComputeAnomalies(bl)
	}

	// Persist
	if err := s.repo.Create(ctx, reading); err != nil {
		return nil, s.mapCreateError(err)
	}

	return s.repo.FindByID(ctx, reading.ID)
}

func (s *meterReadingService) CreateExitReading(ctx context.Context, apartmentID uuid.UUID, req ExitCreateRequest) (*MeterReadingWithRoom, error) {
	roomID, err := uuid.Parse(req.RoomID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รหัสห้องไม่ถูกต้อง")
	}
	readingDate, err := time.Parse("2006-01-02", req.ReadingDateActual)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ไม่ถูกต้อง (YYYY-MM-DD)")
	}

	// Validate room belongs to apartment
	r, err := s.rooms.FindByID(ctx, roomID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}
	if r.ApartmentID != apartmentID {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้")
	}

	// Validate room has a pending move-out notice
	pendingRooms, err := s.moveOuts.FindRoomIDsWithPendingNotice(ctx, []uuid.UUID{roomID})
	if err != nil {
		return nil, fmt.Errorf("check move-out notice: %w", err)
	}
	if !pendingRooms[roomID] {
		return nil, respond.ErrBadRequest.WithMessage("ห้องนี้ไม่มีการแจ้งย้ายออก ไม่สามารถจดมิเตอร์ย้ายออกได้")
	}

	// Reject if EXIT reading already exists for this move-out (latest is EXIT)
	latest := s.findLatestOrNil(ctx, roomID)
	if latest != nil && latest.IsExit() {
		return nil, respond.ErrConflict.WithMessage("ห้องนี้มีข้อมูลมิเตอร์ย้ายออกแล้ว")
	}

	// Reject if MONTHLY reading exists for the same month as the EXIT date
	exitMonth := toMonth(readingDate)
	hasMonthly, err := s.repo.HasMonthlyByRoomAndMonth(ctx, roomID, exitMonth)
	if err != nil {
		return nil, fmt.Errorf("check monthly reading: %w", err)
	}
	if hasMonthly {
		return nil, respond.ErrConflict.WithMessage("มีข้อมูลมิเตอร์รายเดือนของห้องนี้ในเดือนเดียวกันแล้ว กรุณาลบก่อนจดมิเตอร์ย้ายออก")
	}

	// Domain: create EXIT reading
	reading, err := NewExitReading(roomID, readingDate, req.ElectricityCurrent, req.WaterCurrent, latest, req.ReplacedFlags(), req.RolloverFlags())
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	// Persist
	if err := s.repo.Create(ctx, reading); err != nil {
		return nil, s.mapCreateError(err)
	}

	return s.repo.FindByID(ctx, reading.ID)
}

func (s *meterReadingService) BatchCreate(ctx context.Context, apartmentID uuid.UUID, req BatchCreateRequest) ([]MeterReadingWithRoom, error) {
	if !isValidBillingMonth(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบเดือนไม่ถูกต้อง (YYYY-MM)")
	}

	// Validate all rooms before transaction
	roomIDs := make([]uuid.UUID, len(req.Items))
	for i, item := range req.Items {
		roomID, err := uuid.Parse(item.RoomID)
		if err != nil {
			return nil, respond.ErrBadRequest.WithMessage(fmt.Sprintf("รหัสห้องไม่ถูกต้อง (รายการที่ %d)", i+1))
		}
		r, err := s.rooms.FindByID(ctx, roomID)
		if err != nil {
			return nil, respond.ErrNotFound.WithMessage(fmt.Sprintf("ไม่พบห้อง (รายการที่ %d)", i+1))
		}
		if r.ApartmentID != apartmentID {
			return nil, respond.ErrNotFound.WithMessage(fmt.Sprintf("ห้องไม่อยู่ในอาคารนี้ (รายการที่ %d)", i+1))
		}
		roomIDs[i] = roomID
	}

	// Reject rooms with pending move-out notices (monthly batch = MONTHLY only)
	pendingRooms, err := s.moveOuts.FindRoomIDsWithPendingNotice(ctx, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("check move-out notices: %w", err)
	}
	for i, roomID := range roomIDs {
		if pendingRooms[roomID] {
			return nil, respond.ErrBadRequest.WithMessage(fmt.Sprintf("ห้องรายการที่ %d มีการแจ้งย้ายออก ไม่สามารถจดมิเตอร์รายเดือนได้", i+1))
		}
	}

	// Compute baselines from historical readings (before transaction, using roomIDs from request)
	baselines, _ := s.getBaselinesByRoomIDs(ctx, roomIDs)

	// Transaction: create all readings
	createdIDs := make([]uuid.UUID, len(req.Items))
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		for i, item := range req.Items {
			latest := s.findLatestOrNil(txCtx, roomIDs[i])

			reading, err := NewReading(roomIDs[i], req.BillingMonth, item.ElectricityCurrent, item.WaterCurrent, latest, item.ReplacedFlags(), item.RolloverFlags())
			if err != nil {
				return respond.ErrBadRequest.WithMessage(fmt.Sprintf("รายการที่ %d: %s", i+1, err.Error()))
			}

			// Compute anomaly before persist (1 INSERT)
			if bl, ok := baselines[roomIDs[i]]; ok {
				reading.ComputeAnomalies(bl)
			}

			if err := s.repo.Create(txCtx, reading); err != nil {
				if isDuplicateKeyError(err) {
					return respond.ErrConflict.WithMessage(fmt.Sprintf("มีข้อมูลมิเตอร์ของห้องนี้ในเดือนนี้แล้ว (รายการที่ %d)", i+1))
				}
				return fmt.Errorf("create meter reading %d: %w", i+1, err)
			}
			createdIDs[i] = reading.ID
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Re-fetch with relations
	result := make([]MeterReadingWithRoom, len(createdIDs))
	for i, id := range createdIDs {
		r, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("fetch created meter reading: %w", err)
		}
		result[i] = *r
	}
	return result, nil
}

func (s *meterReadingService) Update(ctx context.Context, id uuid.UUID, req UpdateRequest) (*MeterReadingWithRoom, error) {
	reading, err := s.repo.FindByIDSimple(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์")
	}

	// Guard: only latest reading can be updated
	latest, err := s.repo.FindLatestByRoomID(ctx, reading.RoomID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์ล่าสุด")
	}
	if err := reading.CanUpdate(latest.ID); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	// Domain: mutate + validate
	if err := reading.ApplyUpdate(req.ElectricityCurrent, req.WaterCurrent, req.ReplacedFlags(), req.RolloverFlags()); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	// Persist
	if err := s.repo.Update(ctx, reading); err != nil {
		return nil, fmt.Errorf("update meter reading: %w", err)
	}

	return s.repo.FindByID(ctx, reading.ID)
}

func (s *meterReadingService) GetLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	reading, err := s.repo.FindLatestByRoomID(ctx, roomID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, respond.ErrNotFound.WithMessage("ยังไม่มีข้อมูลมิเตอร์ของห้องนี้")
		}
		return nil, fmt.Errorf("get latest meter reading: %w", err)
	}
	return reading, nil
}

func (s *meterReadingService) GetLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error) {
	reading, err := s.repo.FindLatestByRoomIDBeforeDate(ctx, roomID, before, excludeID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์ก่อนวันที่ระบุ")
		}
		return nil, fmt.Errorf("get latest meter reading before date: %w", err)
	}
	return reading, nil
}

func (s *meterReadingService) GetRoomHistory(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReadingWithTenant, int64, error) {
	readings, total, err := s.repo.FindByRoomID(ctx, roomID, params)
	if err != nil {
		return nil, 0, err
	}

	contracts, _ := s.contracts.FindByRoomIDWithTenants(ctx, roomID)

	result := make([]MeterReadingWithTenant, len(readings))
	for i, r := range readings {
		month := readingMonth(r)
		name, startDate, isCurrent := matchTenantForReading(month, contracts)
		result[i] = MeterReadingWithTenant{
			MeterReading:      r,
			TenantName:        name,
			ContractStartDate: startDate,
			IsCurrentTenant:   isCurrent,
		}
	}
	return result, total, nil
}

// matchTenantForReading finds which contract covers a given billing month.
// billingMonth is treated as whole-month coverage — mid-month start/end counts.
func matchTenantForReading(billingMonth string, contracts []contract.ContractTenantSummary) (string, time.Time, bool) {
	// Defensive sort: ensure newest-first (clone to avoid mutating caller's slice)
	sorted := slices.Clone(contracts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartDate.After(sorted[j].StartDate)
	})
	for _, c := range sorted {
		startMonth := toMonth(c.StartDate)
		if isBeforeMonth(billingMonth, startMonth) {
			continue // before contract
		}
		if c.Status == contract.ContractStatusActive {
			return c.TenantName, c.StartDate, true // no end
		}
		// ENDED/TERMINATED — check end month
		if c.EndDate != nil && isBeforeMonth(toMonth(*c.EndDate), billingMonth) {
			continue // after contract ended
		}
		return c.TenantName, c.StartDate, false
	}
	return "", time.Time{}, false
}

// DeleteExitByRoomID delegates to repo — satisfies moveout.MeterReadingCommander.
func (s *meterReadingService) DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error {
	return s.repo.DeleteExitByRoomID(ctx, roomID)
}

// CreateExitForMoveOut creates an EXIT reading as part of the move-out workflow.
// Skips apartment-room ownership validation (caller already verified).
// Rejects duplicate EXIT and same-month MONTHLY, same as the public endpoint.
func (s *meterReadingService) CreateExitForMoveOut(ctx context.Context, roomID uuid.UUID, readingDate time.Time, elecCurrent, waterCurrent int) error {
	// Reject if EXIT reading already exists
	latest := s.findLatestOrNil(ctx, roomID)
	if latest != nil && latest.IsExit() {
		return respond.ErrConflict.WithMessage("ห้องนี้มีข้อมูลมิเตอร์ย้ายออกแล้ว")
	}

	// Reject if MONTHLY exists for the same month
	exitMonth := toMonth(readingDate)
	hasMonthly, err := s.repo.HasMonthlyByRoomAndMonth(ctx, roomID, exitMonth)
	if err != nil {
		return fmt.Errorf("check monthly reading: %w", err)
	}
	if hasMonthly {
		return respond.ErrConflict.WithMessage("มีข้อมูลมิเตอร์รายเดือนของห้องนี้ในเดือนเดียวกันแล้ว")
	}

	reading, err := NewExitReading(roomID, readingDate, elecCurrent, waterCurrent, latest, MeterReplacedFlags{}, MeterRolloverFlags{})
	if err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Create(ctx, reading); err != nil {
		return s.mapCreateError(err)
	}
	return nil
}

// --- private helpers (orchestration support, no business logic) ---

func (s *meterReadingService) findLatestOrNil(ctx context.Context, roomID uuid.UUID) *MeterReading {
	latest, err := s.repo.FindLatestByRoomID(ctx, roomID)
	if err != nil {
		return nil // first reading
	}
	return latest
}

func (s *meterReadingService) mapCreateError(err error) error {
	if isDuplicateKeyError(err) {
		return respond.ErrConflict.WithMessage("มีข้อมูลมิเตอร์ของห้องนี้ในเดือน/วันนี้แล้ว")
	}
	return fmt.Errorf("create meter reading: %w", err)
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "23505")
}

// --- Baseline computation ---

func (s *meterReadingService) GetBaselines(ctx context.Context, apartmentID uuid.UUID) (map[uuid.UUID]RoomBaseline, error) {
	roomIDs, err := s.rooms.FindRoomIDsByApartment(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("find room IDs: %w", err)
	}
	return s.getBaselinesByRoomIDs(ctx, roomIDs)
}

// getBaselinesByRoomIDs is the internal helper shared by GetBaselines (endpoint) and Create/BatchCreate.
// Baseline uses only MONTHLY readings after current tenant's contract start date.
// No active contract → baseline null (no tenant to compare against).
func (s *meterReadingService) getBaselinesByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]RoomBaseline, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]RoomBaseline{}, nil
	}

	historyMap, err := s.repo.FindRecentByRoomIDs(ctx, roomIDs, 6)
	if err != nil {
		return nil, fmt.Errorf("find recent readings: %w", err)
	}

	// Fetch current tenant's contract start dates
	startDates, err := s.contracts.FindActiveContractStartDatesByRoomIDs(ctx, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("find contract start dates: %w", err)
	}

	result := make(map[uuid.UUID]RoomBaseline, len(roomIDs))
	for _, roomID := range roomIDs {
		// No active contract → baseline null (no tenant to compare against)
		startDate, hasContract := startDates[roomID]
		if !hasContract {
			result[roomID] = RoomBaseline{} // all false/zero = null baseline
			continue
		}

		// Filter to readings from current tenant only
		readings := historyMap[roomID]
		filtered := readings[:0:0]
		startMonth := toMonth(startDate)
		for _, r := range readings {
			rm := readingMonth(r)
			if !isBeforeMonth(rm, startMonth) {
				filtered = append(filtered, r)
			}
		}

		result[roomID] = computeBaseline(filtered)
	}
	return result, nil
}

// computeBaseline computes median usage from historical readings.
// Readings come from repo sorted desc — we don't re-sort.
// Median: odd → middle element, even → lower middle (integer, no float average).
func computeBaseline(readings []MeterReading) RoomBaseline {
	var bl RoomBaseline

	var elecUsages, waterUsages []int
	for _, r := range readings {
		elecUsages = append(elecUsages, r.ElectricityUsed())
		waterUsages = append(waterUsages, r.WaterUsed())
	}

	if len(elecUsages) >= 3 {
		bl.ElectricityHasEnoughData = true
		bl.ElectricityBaseline = median(elecUsages)
	}
	if len(waterUsages) >= 3 {
		bl.WaterHasEnoughData = true
		bl.WaterBaseline = median(waterUsages)
	}
	return bl
}

// median returns the true statistical median (integer).
// Odd:  [10,20,30]    → 20 (middle element, index len/2)
// Even: [10,20,30,40] → 25 ((20+30)/2, integer division)
func median(values []int) int {
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
