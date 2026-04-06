package meterreading

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// temporalDateExpr returns a SQL expression that converts both reading types to a comparable date.
// MONTHLY → last day of billing_month (e.g. "2026-04" → 2026-04-30)
// EXIT → reading_date_actual as-is
// This ensures MONTHLY sorts AFTER EXIT readings in the same month,
// because a MONTHLY reading represents end-of-month coverage.
func temporalDateExpr(prefix string) string {
	if prefix != "" {
		prefix += "."
	}
	return fmt.Sprintf(
		`CASE WHEN %sreading_type = 'EXIT' THEN %sreading_date_actual `+
			`ELSE (TO_DATE(%sbilling_month, 'YYYY-MM') + INTERVAL '1 month - 1 day')::date END`,
		prefix, prefix, prefix,
	)
}

type MeterReadingRepository interface {
	FindAll(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*MeterReading, error)
	FindByRoomID(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error)
	FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	FindLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error)
	FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error)
	HasMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (bool, error)
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*MeterReading, error)
	Create(ctx context.Context, reading *MeterReading) error
	Update(ctx context.Context, reading *MeterReading) error
	DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error
}

type meterReadingRepository struct {
	db *gorm.DB
}

func NewMeterReadingRepository(db *gorm.DB) MeterReadingRepository {
	return &meterReadingRepository{db: db}
}

func (r *meterReadingRepository) FindAll(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error) {
	var total int64

	query := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Joins("JOIN rooms ON rooms.id = meter_readings.room_id AND rooms.deleted_at IS NULL").
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("rooms.apartment_id = ? AND meter_readings.deleted_at IS NULL", apartmentID)

	if params.Month != "" {
		query = query.Where("meter_readings.billing_month = ?", params.Month)
	}
	if params.ReadingType != "" {
		query = query.Where("meter_readings.reading_type = ?", params.ReadingType)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(rooms.number ILIKE ? OR tenants.full_name ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"billing_month", "room_number", "created_at"}, "billing_month")
	var orderCol string
	switch col {
	case "room_number":
		orderCol = "rooms.number"
	case "billing_month":
		orderCol = temporalDateExpr("meter_readings")
	default:
		orderCol = "meter_readings." + col
	}
	orderClause := fmt.Sprintf("%s %s", orderCol, order)

	type joinRow struct {
		MeterReading
		RoomNumber string `gorm:"column:room_number"`
		Floor      int    `gorm:"column:room_floor"`
		TenantName string `gorm:"column:tenant_name"`
	}

	var rows []joinRow
	err := query.
		Select(`meter_readings.*,
			rooms.number AS room_number,
			rooms.floor AS room_floor,
			COALESCE(tenants.full_name, '') AS tenant_name`).
		Order(orderClause).
		Offset(params.Offset()).
		Limit(params.Limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	result := make([]MeterReadingWithRoom, len(rows))
	for i, row := range rows {
		result[i] = MeterReadingWithRoom{
			MeterReading: row.MeterReading,
			RoomNumber:   row.RoomNumber,
			Floor:        row.Floor,
			TenantName:   row.TenantName,
		}
	}
	return result, total, nil
}

func (r *meterReadingRepository) FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error) {
	type joinRow struct {
		MeterReading
		RoomNumber string `gorm:"column:room_number"`
		Floor      int    `gorm:"column:room_floor"`
		TenantName string `gorm:"column:tenant_name"`
	}

	var row joinRow
	err := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Select(`meter_readings.*,
			rooms.number AS room_number,
			rooms.floor AS room_floor,
			COALESCE(tenants.full_name, '') AS tenant_name`).
		Joins("JOIN rooms ON rooms.id = meter_readings.room_id AND rooms.deleted_at IS NULL").
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("meter_readings.id = ? AND meter_readings.deleted_at IS NULL", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}

	result := MeterReadingWithRoom{
		MeterReading: row.MeterReading,
		RoomNumber:   row.RoomNumber,
		Floor:        row.Floor,
		TenantName:   row.TenantName,
	}
	return &result, nil
}

func (r *meterReadingRepository) FindByIDSimple(ctx context.Context, id uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *meterReadingRepository) FindByRoomID(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Where("room_id = ? AND deleted_at IS NULL", roomID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortExpr := temporalDateExpr("")
	var readings []MeterReading
	err := query.
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr)).
		Offset(params.Offset()).
		Limit(params.Limit).
		Find(&readings).Error
	if err != nil {
		return nil, 0, err
	}
	return readings, total, nil
}

func (r *meterReadingRepository) FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	sortExpr := temporalDateExpr("")
	var m MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr)).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// FindLatestByRoomIDBeforeDate finds the latest reading strictly before a given date.
// Used as settlement baseline for EXIT billing.
// excludeID prevents the EXIT reading itself from being returned as its own baseline.
func (r *meterReadingRepository) FindLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error) {
	sortExpr := temporalDateExpr("")
	q := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Where(fmt.Sprintf("%s < ?", sortExpr), before).
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr))

	if excludeID != nil {
		q = q.Where("id != ?", *excludeID)
	}

	var m MeterReading
	if err := q.First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindRecentByRoomIDs fetches the last N MONTHLY readings per room for baseline computation.
// EXIT readings are excluded — they represent partial-period usage, not regular patterns.
func (r *meterReadingRepository) FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID][]MeterReading{}, nil
	}

	var readings []MeterReading
	subQuery := database.DB(ctx, r.db).
		Table("meter_readings").
		Select("*, ROW_NUMBER() OVER (PARTITION BY room_id ORDER BY billing_month DESC, created_at DESC) AS rn").
		Where("room_id IN ? AND reading_type = 'MONTHLY' AND deleted_at IS NULL", roomIDs)

	err := database.DB(ctx, r.db).
		Table("(?) AS sub", subQuery).
		Where("rn <= ?", limit).
		Order("room_id, rn").
		Find(&readings).Error
	if err != nil {
		return nil, err
	}

	result := make(map[uuid.UUID][]MeterReading, len(roomIDs))
	for _, reading := range readings {
		result[reading.RoomID] = append(result[reading.RoomID], reading)
	}
	return result, nil
}

// HasMonthlyByRoomAndMonth checks if a MONTHLY reading already exists for this room and month.
// Used to prevent creating EXIT readings when a MONTHLY for the same period already exists.
func (r *meterReadingRepository) HasMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Where("room_id = ? AND reading_type = 'MONTHLY' AND billing_month = ? AND deleted_at IS NULL", roomID, month).
		Count(&count).Error
	return count > 0, err
}

// FindMonthlyByRoomsAndMonth bulk-fetches MONTHLY readings for multiple rooms in a single query.
// Returns map[roomID]*MeterReading. DB UNIQUE constraint guarantees ≤1 row per room+month.
func (r *meterReadingRepository) FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*MeterReading, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]*MeterReading{}, nil
	}
	var readings []MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id IN ? AND reading_type = ? AND billing_month = ? AND deleted_at IS NULL",
			roomIDs, ReadingTypeMonthly, month).
		Find(&readings).Error
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]*MeterReading, len(readings))
	for i := range readings {
		result[readings[i].RoomID] = &readings[i]
	}
	return result, nil
}

func (r *meterReadingRepository) Create(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Create(reading).Error
}

func (r *meterReadingRepository) Update(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Model(reading).Select("*").Omit("deleted_at").Updates(reading).Error
}

// DeleteExitByRoomID soft-deletes any active EXIT reading for a room.
// Idempotent: no-op if none exist. Used by move-out cancel to revert exit-meter prep
// so the workflow can be restarted cleanly (avoids unique-index collision on retry).
func (r *meterReadingRepository) DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error {
	return database.DB(ctx, r.db).
		Where("room_id = ? AND reading_type = ? AND deleted_at IS NULL", roomID, ReadingTypeExit).
		Delete(&MeterReading{}).Error
}
