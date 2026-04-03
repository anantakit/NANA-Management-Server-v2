package meterreading

import (
	"context"
	"fmt"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MeterReadingRepository interface {
	FindAll(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*MeterReading, error)
	FindByRoomID(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error)
	FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error)
	Create(ctx context.Context, reading *MeterReading) error
	Update(ctx context.Context, reading *MeterReading) error
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
		query = query.Where("TO_CHAR(meter_readings.reading_date, 'YYYY-MM') = ?", params.Month)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(rooms.number ILIKE ? OR tenants.full_name ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"reading_date", "room_number", "created_at"}, "reading_date")
	orderCol := "meter_readings." + col
	if col == "room_number" {
		orderCol = "rooms.number"
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

	var readings []MeterReading
	err := query.
		Order("reading_date DESC, updated_at DESC").
		Offset(params.Offset()).
		Limit(params.Limit).
		Find(&readings).Error
	if err != nil {
		return nil, 0, err
	}
	return readings, total, nil
}

func (r *meterReadingRepository) FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Order("reading_date DESC, created_at DESC").
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *meterReadingRepository) FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID][]MeterReading{}, nil
	}

	var readings []MeterReading
	subQuery := database.DB(ctx, r.db).
		Table("meter_readings").
		Select("*, ROW_NUMBER() OVER (PARTITION BY room_id ORDER BY reading_date DESC, created_at DESC) AS rn").
		Where("room_id IN ? AND deleted_at IS NULL", roomIDs)

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

func (r *meterReadingRepository) Create(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Create(reading).Error
}

func (r *meterReadingRepository) Update(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Model(reading).Select("*").Omit("deleted_at").Updates(reading).Error
}
