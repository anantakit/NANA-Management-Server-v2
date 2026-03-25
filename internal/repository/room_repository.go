package repository

import (
	"context"
	"fmt"

	"nana/internal/database"
	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RoomRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]domain.Room, int64, error)
	FindByApartmentIDWithContracts(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]dto.RoomWithContract, int64, error)
	FindByIDWithContract(ctx context.Context, id uuid.UUID) (*dto.RoomWithContract, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Room, error)
	Create(ctx context.Context, room *domain.Room) error
	Update(ctx context.Context, room *domain.Room) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RoomStatus) error
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

func (r *roomRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]domain.Room, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).Model(&model.Room{}).Where("apartment_id = ?", apartmentID)

	if params.Search != "" {
		query = query.Where("number ILIKE ?", "%"+params.Search+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := dto.SafeSort(params.Sort, params.Order, []string{"number", "floor", "type", "status", "base_rent", "created_at"}, "number")
	if params.Sort == "" {
		order = "asc"
	}
	orderClause := fmt.Sprintf("%s %s", col, order)

	var models []model.Room
	if err := query.Order(orderClause).Offset(params.Offset()).Limit(params.Limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]domain.Room, len(models))
	for i, m := range models {
		result[i] = m.ToDomain()
	}
	return result, total, nil
}

func (r *roomRepository) FindByApartmentIDWithContracts(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]dto.RoomWithContract, int64, error) {
	var total int64
	countQuery := database.DB(ctx, r.db).Model(&model.Room{}).Where("rooms.apartment_id = ? AND rooms.deleted_at IS NULL", apartmentID)
	if params.Search != "" {
		countQuery = countQuery.Where("rooms.number ILIKE ?", "%"+params.Search+"%")
	}
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := dto.SafeSort(params.Sort, params.Order, []string{"number", "floor", "type", "status", "base_rent", "created_at"}, "number")
	if params.Sort == "" {
		order = "asc"
	}

	type joinRow struct {
		model.Room
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
		MoveOutDate            *string `gorm:"column:contract_move_out"`
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
			TO_CHAR(contracts.move_out_date, 'YYYY-MM-DD') AS contract_move_out`).
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("rooms.apartment_id = ? AND rooms.deleted_at IS NULL", apartmentID)

	if params.Search != "" {
		query = query.Where("rooms.number ILIKE ?", "%"+params.Search+"%")
	}

	var rows []joinRow
	if err := query.Order(fmt.Sprintf("rooms.%s %s", col, order)).Offset(params.Offset()).Limit(params.Limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	result := make([]dto.RoomWithContract, len(rows))
	for i, row := range rows {
		result[i] = dto.RoomWithContract{
			Room:                   row.Room.ToDomain(),
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
			MoveOutDate:            row.MoveOutDate,
		}
	}
	return result, total, nil
}

func (r *roomRepository) FindByIDWithContract(ctx context.Context, id uuid.UUID) (*dto.RoomWithContract, error) {
	type joinRow struct {
		model.Room
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
		MoveOutDate            *string `gorm:"column:contract_move_out"`
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
			TO_CHAR(contracts.move_out_date, 'YYYY-MM-DD') AS contract_move_out`).
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("rooms.id = ? AND rooms.deleted_at IS NULL", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}

	result := dto.RoomWithContract{
		Room:                   row.Room.ToDomain(),
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
		MoveOutDate:            row.MoveOutDate,
	}
	return &result, nil
}

func (r *roomRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Room, error) {
	var m model.Room
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	d := m.ToDomain()
	return &d, nil
}

func (r *roomRepository) Create(ctx context.Context, room *domain.Room) error {
	m := model.RoomFromDomain(*room)
	if err := database.DB(ctx, r.db).Create(&m).Error; err != nil {
		return err
	}
	*room = m.ToDomain()
	return nil
}

func (r *roomRepository) Update(ctx context.Context, room *domain.Room) error {
	m := model.RoomFromDomain(*room)
	if err := database.DB(ctx, r.db).Model(&m).Select("*").Omit("deleted_at").Updates(&m).Error; err != nil {
		return err
	}
	*room = m.ToDomain()
	return nil
}

func (r *roomRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.RoomStatus) error {
	return database.DB(ctx, r.db).Model(&model.Room{}).Where("id = ?", id).Update("status", string(status)).Error
}

func (r *roomRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&model.Room{}, "id = ?", id).Error
}

func (r *roomRepository) ExistsByNumber(ctx context.Context, apartmentID uuid.UUID, number string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&model.Room{}).Where("apartment_id = ? AND number = ?", apartmentID, number).Count(&count).Error
	return count > 0, err
}

func (r *roomRepository) ExistsByNumberExcluding(ctx context.Context, apartmentID uuid.UUID, number string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&model.Room{}).Where("apartment_id = ? AND number = ? AND id != ?", apartmentID, number, excludeID).Count(&count).Error
	return count > 0, err
}
