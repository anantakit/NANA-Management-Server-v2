package room

import (
	"context"
	"fmt"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RoomRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]Room, int64, error)
	FindByApartmentIDWithContracts(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]RoomWithContract, int64, error)
	FindByIDWithContract(ctx context.Context, id uuid.UUID) (*RoomWithContract, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Room, error)
	FindRoomIDsByApartment(ctx context.Context, apartmentID uuid.UUID) ([]uuid.UUID, error)
	Create(ctx context.Context, room *Room) error
	Update(ctx context.Context, room *Room) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status RoomStatus) error
	MarkOccupied(ctx context.Context, id uuid.UUID) error
	MarkVacant(ctx context.Context, id uuid.UUID) error
	Delete(ctx context.Context, id uuid.UUID) error
	ExistsByNumber(ctx context.Context, apartmentID uuid.UUID, number string) (bool, error)
	ExistsByNumberExcluding(ctx context.Context, apartmentID uuid.UUID, number string, excludeID uuid.UUID) (bool, error)
}

type roomRepository struct {
	db *gorm.DB
}

func NewRoomRepository(db *gorm.DB) RoomRepository {
	return &roomRepository{db: db}
}

// Display-read constants for cross-feature JOINs.
// Import cycle prevents referencing contract.ContractStatusActive / moveout.MoveOutStatusPending
// directly (contract + moveout depend on room via ports). These mirror the owner's domain values —
// if those ever change, update here. See cross-feature-patterns.md §3c "Display Read".
const joinContractStatusActive = "ACTIVE"

// Terminal move-out statuses excluded from display JOINs (level-1 logic leak).
var joinMoveOutTerminalStatuses = []string{"COMPLETED", "CANCELLED"}

func (r *roomRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]Room, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).Model(&Room{}).Where("apartment_id = ?", apartmentID)

	if params.Search != "" {
		query = query.Where("number ILIKE ?", "%"+params.Search+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"number", "floor", "type", "status", "base_rent", "created_at"}, "number")
	if params.Sort == "" {
		order = "asc"
	}
	orderClause := fmt.Sprintf("%s %s", col, order)

	var rooms []Room
	if err := query.Order(orderClause).Offset(params.Offset()).Limit(params.Limit).Find(&rooms).Error; err != nil {
		return nil, 0, err
	}
	return rooms, total, nil
}

func (r *roomRepository) FindByApartmentIDWithContracts(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]RoomWithContract, int64, error) {
	var total int64
	countQuery := database.DB(ctx, r.db).Model(&Room{}).Where("rooms.apartment_id = ? AND rooms.deleted_at IS NULL", apartmentID)
	if params.Search != "" {
		countQuery = countQuery.Where("rooms.number ILIKE ?", "%"+params.Search+"%")
	}
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"number", "floor", "type", "status", "base_rent", "created_at"}, "number")
	if params.Sort == "" {
		order = "asc"
	}

	type joinRow struct {
		Room
		ContractID             *string `gorm:"column:contract_id"`
		TenantID               *string `gorm:"column:tenant_id_str"`
		TenantName             *string `gorm:"column:tenant_name"`
		TenantPhone            *string `gorm:"column:tenant_phone"`
		MonthlyRent            *int64  `gorm:"column:contract_rent"`
		DepositAmount          *int64  `gorm:"column:contract_deposit"`
		ElectricityRatePerUnit *int64  `gorm:"column:contract_elec_rate"`
		WaterRatePerUnit       *int64  `gorm:"column:contract_water_rate"`
		StartDate              *string `gorm:"column:contract_start"`
		MinMonths              *int    `gorm:"column:contract_min_months"`
		MoveOutNoticeID        *string `gorm:"column:move_out_notice_id"`
		ScheduledMoveOutDate   *string `gorm:"column:scheduled_move_out_date"`
	}

	query := database.DB(ctx, r.db).
		Table("rooms").
		Select(`rooms.*,
			contracts.id::text AS contract_id,
			contracts.tenant_id::text AS tenant_id_str,
			tenants.full_name AS tenant_name,
			tenants.phone AS tenant_phone,
			contracts.monthly_rent AS contract_rent,
			contracts.deposit_amount AS contract_deposit,
			contracts.electricity_rate_per_unit AS contract_elec_rate,
			contracts.water_rate_per_unit AS contract_water_rate,
			TO_CHAR(contracts.start_date, 'YYYY-MM-DD') AS contract_start,
			contracts.min_months AS contract_min_months,
			move_out_notices.id::text AS move_out_notice_id,
			TO_CHAR(move_out_notices.scheduled_move_out_date, 'YYYY-MM-DD') AS scheduled_move_out_date`).
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = ? AND contracts.deleted_at IS NULL", joinContractStatusActive).
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("LEFT JOIN move_out_notices ON move_out_notices.contract_id = contracts.id AND move_out_notices.status NOT IN ? AND move_out_notices.deleted_at IS NULL", joinMoveOutTerminalStatuses).
		Where("rooms.apartment_id = ? AND rooms.deleted_at IS NULL", apartmentID)

	if params.Search != "" {
		query = query.Where("rooms.number ILIKE ?", "%"+params.Search+"%")
	}

	var rows []joinRow
	if err := query.Order(fmt.Sprintf("rooms.%s %s", col, order)).Offset(params.Offset()).Limit(params.Limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	result := make([]RoomWithContract, len(rows))
	for i, row := range rows {
		result[i] = RoomWithContract{
			Room:                   row.Room,
			ContractID:             row.ContractID,
			TenantID:               row.TenantID,
			TenantName:             row.TenantName,
			TenantPhone:            row.TenantPhone,
			MonthlyRent:            row.MonthlyRent,
			DepositAmount:          row.DepositAmount,
			ElectricityRatePerUnit: row.ElectricityRatePerUnit,
			WaterRatePerUnit:       row.WaterRatePerUnit,
			StartDate:              row.StartDate,
			MinMonths:              row.MinMonths,
			MoveOutNoticeID:        row.MoveOutNoticeID,
			ScheduledMoveOutDate:   row.ScheduledMoveOutDate,
		}
	}
	return result, total, nil
}

func (r *roomRepository) FindByIDWithContract(ctx context.Context, id uuid.UUID) (*RoomWithContract, error) {
	type joinRow struct {
		Room
		ContractID             *string `gorm:"column:contract_id"`
		TenantID               *string `gorm:"column:tenant_id_str"`
		TenantName             *string `gorm:"column:tenant_name"`
		TenantPhone            *string `gorm:"column:tenant_phone"`
		MonthlyRent            *int64  `gorm:"column:contract_rent"`
		DepositAmount          *int64  `gorm:"column:contract_deposit"`
		ElectricityRatePerUnit *int64  `gorm:"column:contract_elec_rate"`
		WaterRatePerUnit       *int64  `gorm:"column:contract_water_rate"`
		StartDate              *string `gorm:"column:contract_start"`
		MinMonths              *int    `gorm:"column:contract_min_months"`
		MoveOutNoticeID        *string `gorm:"column:move_out_notice_id"`
		ScheduledMoveOutDate   *string `gorm:"column:scheduled_move_out_date"`
	}

	var row joinRow
	err := database.DB(ctx, r.db).
		Table("rooms").
		Select(`rooms.*,
			contracts.id::text AS contract_id,
			contracts.tenant_id::text AS tenant_id_str,
			tenants.full_name AS tenant_name,
			tenants.phone AS tenant_phone,
			contracts.monthly_rent AS contract_rent,
			contracts.deposit_amount AS contract_deposit,
			contracts.electricity_rate_per_unit AS contract_elec_rate,
			contracts.water_rate_per_unit AS contract_water_rate,
			TO_CHAR(contracts.start_date, 'YYYY-MM-DD') AS contract_start,
			contracts.min_months AS contract_min_months,
			move_out_notices.id::text AS move_out_notice_id,
			TO_CHAR(move_out_notices.scheduled_move_out_date, 'YYYY-MM-DD') AS scheduled_move_out_date`).
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = ? AND contracts.deleted_at IS NULL", joinContractStatusActive).
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("LEFT JOIN move_out_notices ON move_out_notices.contract_id = contracts.id AND move_out_notices.status NOT IN ? AND move_out_notices.deleted_at IS NULL", joinMoveOutTerminalStatuses).
		Where("rooms.id = ? AND rooms.deleted_at IS NULL", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}

	result := RoomWithContract{
		Room:                   row.Room,
		ContractID:             row.ContractID,
		TenantID:               row.TenantID,
		TenantName:             row.TenantName,
		TenantPhone:            row.TenantPhone,
		MonthlyRent:            row.MonthlyRent,
		DepositAmount:          row.DepositAmount,
		ElectricityRatePerUnit: row.ElectricityRatePerUnit,
		WaterRatePerUnit:       row.WaterRatePerUnit,
		StartDate:              row.StartDate,
		MinMonths:              row.MinMonths,
		MoveOutNoticeID:        row.MoveOutNoticeID,
		ScheduledMoveOutDate:   row.ScheduledMoveOutDate,
	}
	return &result, nil
}

func (r *roomRepository) FindByID(ctx context.Context, id uuid.UUID) (*Room, error) {
	var m Room
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindRoomIDsByApartment returns the IDs of all non-deleted rooms in the apartment.
//
// Plucks into []string then parses, because GORM's generic column scan
// reflects into uuid.UUID as [16]byte and never invokes uuid.UUID.Scan —
// silently corrupts values when pgx returns UUIDs in text encoding. Using
// []string + uuid.Parse is driver-agnostic. (Same fix pattern as
// billing.FindApartmentIDByRoomID — see that method's docstring for history.)
func (r *roomRepository) FindRoomIDsByApartment(ctx context.Context, apartmentID uuid.UUID) ([]uuid.UUID, error) {
	var raws []string
	err := database.DB(ctx, r.db).
		Model(&Room{}).
		Where("apartment_id = ?", apartmentID).
		Pluck("id", &raws).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(raws))
	for _, s := range raws {
		id, parseErr := uuid.Parse(s)
		if parseErr != nil {
			return nil, fmt.Errorf("parse room id %q: %w", s, parseErr)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *roomRepository) Create(ctx context.Context, room *Room) error {
	return database.DB(ctx, r.db).Create(room).Error
}

func (r *roomRepository) Update(ctx context.Context, room *Room) error {
	return database.DB(ctx, r.db).Model(room).Select("*").Omit("deleted_at").Updates(room).Error
}

func (r *roomRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status RoomStatus) error {
	return database.DB(ctx, r.db).Model(&Room{}).Where("id = ?", id).Update("status", string(status)).Error
}

func (r *roomRepository) MarkOccupied(ctx context.Context, id uuid.UUID) error {
	return r.UpdateStatus(ctx, id, RoomStatusOccupied)
}

func (r *roomRepository) MarkVacant(ctx context.Context, id uuid.UUID) error {
	return r.UpdateStatus(ctx, id, RoomStatusVacant)
}

func (r *roomRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&Room{}, "id = ?", id).Error
}

func (r *roomRepository) ExistsByNumber(ctx context.Context, apartmentID uuid.UUID, number string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Room{}).Where("apartment_id = ? AND number = ?", apartmentID, number).Count(&count).Error
	return count > 0, err
}

func (r *roomRepository) ExistsByNumberExcluding(ctx context.Context, apartmentID uuid.UUID, number string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Room{}).Where("apartment_id = ? AND number = ? AND id != ?", apartmentID, number, excludeID).Count(&count).Error
	return count > 0, err
}
