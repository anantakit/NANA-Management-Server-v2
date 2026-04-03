package meterreading

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MeterReadingService interface {
	List(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req CreateRequest) (*MeterReadingWithRoom, error)
	BatchCreate(ctx context.Context, apartmentID uuid.UUID, req BatchCreateRequest) ([]MeterReadingWithRoom, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateRequest) (*MeterReadingWithRoom, error)
	GetLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	GetRoomHistory(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error)
	GetBaselines(ctx context.Context, apartmentID uuid.UUID) (map[uuid.UUID]RoomBaseline, error)
}

type meterReadingService struct {
	repo      MeterReadingRepository
	rooms     RoomQuerier
	contracts ContractQuerier
	tx        database.TxManager
}

func NewMeterReadingService(
	repo MeterReadingRepository,
	rooms RoomQuerier,
	contracts ContractQuerier,
	tx database.TxManager,
) MeterReadingService {
	return &meterReadingService{
		repo:      repo,
		rooms:     rooms,
		contracts: contracts,
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
	readingDate, err := time.Parse("2006-01-02", req.ReadingDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ไม่ถูกต้อง")
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
	reading, err := NewReading(roomID, readingDate, req.ElectricityCurrent, req.WaterCurrent, latest, req.ReplacedFlags(), req.RolloverFlags())
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

func (s *meterReadingService) BatchCreate(ctx context.Context, apartmentID uuid.UUID, req BatchCreateRequest) ([]MeterReadingWithRoom, error) {
	readingDate, err := time.Parse("2006-01-02", req.ReadingDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ไม่ถูกต้อง")
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

	// Compute baselines from historical readings (before transaction, using roomIDs from request)
	baselines, _ := s.getBaselinesByRoomIDs(ctx, roomIDs)

	// Transaction: create all readings
	createdIDs := make([]uuid.UUID, len(req.Items))
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		for i, item := range req.Items {
			latest := s.findLatestOrNil(txCtx, roomIDs[i])

			reading, err := NewReading(roomIDs[i], readingDate, item.ElectricityCurrent, item.WaterCurrent, latest, item.ReplacedFlags(), item.RolloverFlags())
			if err != nil {
				return respond.ErrBadRequest.WithMessage(fmt.Sprintf("รายการที่ %d: %s", i+1, err.Error()))
			}

			// Compute anomaly before persist (1 INSERT)
			if bl, ok := baselines[roomIDs[i]]; ok {
				reading.ComputeAnomalies(bl)
			}

			if err := s.repo.Create(txCtx, reading); err != nil {
				if isDuplicateKeyError(err) {
					return respond.ErrConflict.WithMessage(fmt.Sprintf("มีข้อมูลมิเตอร์ของห้องนี้ในวันที่นี้แล้ว (รายการที่ %d)", i+1))
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

func (s *meterReadingService) GetRoomHistory(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error) {
	return s.repo.FindByRoomID(ctx, roomID, params)
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
		return respond.ErrConflict.WithMessage("มีข้อมูลมิเตอร์ของห้องนี้ในวันที่นี้แล้ว")
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
// Baseline uses only readings after current tenant's contract start date.
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
		for _, r := range readings {
			if !r.ReadingDate.Before(startDate) {
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
