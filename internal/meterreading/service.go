package meterreading

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nana/internal/shared/database"
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
}

type meterReadingService struct {
	repo  MeterReadingRepository
	rooms RoomQuerier
	tx    database.TxManager
}

func NewMeterReadingService(
	repo MeterReadingRepository,
	rooms RoomQuerier,
	tx database.TxManager,
) MeterReadingService {
	return &meterReadingService{
		repo:  repo,
		rooms: rooms,
		tx:    tx,
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
	reading, err := NewReading(roomID, readingDate, req.ElectricityCurrent, req.WaterCurrent, latest, req.IsMeterReplaced)
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

	// Transaction: create all readings
	createdIDs := make([]uuid.UUID, len(req.Items))
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		for i, item := range req.Items {
			latest := s.findLatestOrNil(txCtx, roomIDs[i])

			reading, err := NewReading(roomIDs[i], readingDate, item.ElectricityCurrent, item.WaterCurrent, latest, item.IsMeterReplaced)
			if err != nil {
				return respond.ErrBadRequest.WithMessage(fmt.Sprintf("รายการที่ %d: %s", i+1, err.Error()))
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
	if err := reading.ApplyUpdate(req.ElectricityCurrent, req.WaterCurrent); err != nil {
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
