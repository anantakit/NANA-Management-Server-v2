package contract

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ContractRepository interface {
	FindAll(ctx context.Context, params ContractListParams) ([]ContractWithRelations, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*ContractWithRelations, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*Contract, error)
	Create(ctx context.Context, contract *Contract) error
	Update(ctx context.Context, contract *Contract) error
	Delete(ctx context.Context, id uuid.UUID) error
	HasActiveByRoomID(ctx context.Context, roomID uuid.UUID) (bool, error)
	HasActiveByTenantID(ctx context.Context, tenantID uuid.UUID) (bool, error)
	FindActiveContractStartDatesByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]time.Time, error)
	FindByRoomIDWithTenants(ctx context.Context, roomID uuid.UUID) ([]ContractTenantSummary, error)
}

type contractRepository struct {
	db *gorm.DB
}

func NewContractRepository(db *gorm.DB) ContractRepository {
	return &contractRepository{db: db}
}

func (r *contractRepository) FindAll(ctx context.Context, params ContractListParams) ([]ContractWithRelations, int64, error) {
	var total int64

	query := database.DB(ctx, r.db).
		Model(&Contract{}).
		Joins("JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("JOIN rooms ON rooms.id = contracts.room_id AND rooms.deleted_at IS NULL").
		Joins("JOIN apartments ON apartments.id = rooms.apartment_id AND apartments.deleted_at IS NULL").
		Where("contracts.deleted_at IS NULL")

	if params.Status != "" {
		query = query.Where("contracts.status = ?", params.Status)
	}
	if params.ApartmentID != "" {
		query = query.Where("rooms.apartment_id = ?", params.ApartmentID)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(tenants.full_name ILIKE ? OR rooms.number ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"start_date", "monthly_rent", "status", "created_at"}, "created_at")
	orderClause := fmt.Sprintf("contracts.%s %s", col, order)

	type joinRow struct {
		Contract
		TenantName    string    `gorm:"column:tenant_name"`
		TenantPhone   string    `gorm:"column:tenant_phone"`
		RoomNumber    string    `gorm:"column:room_number"`
		ApartmentID   uuid.UUID `gorm:"column:apt_id"`
		ApartmentName string    `gorm:"column:apt_name"`
	}

	var rows []joinRow
	err := query.
		Select(`contracts.*,
			tenants.full_name AS tenant_name,
			tenants.phone AS tenant_phone,
			rooms.number AS room_number,
			apartments.id AS apt_id,
			apartments.name AS apt_name`).
		Order(orderClause).
		Offset(params.Offset()).
		Limit(params.Limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	result := make([]ContractWithRelations, len(rows))
	for i, row := range rows {
		result[i] = ContractWithRelations{
			Contract:      row.Contract,
			TenantName:    row.TenantName,
			TenantPhone:   row.TenantPhone,
			RoomNumber:    row.RoomNumber,
			ApartmentID:   row.ApartmentID,
			ApartmentName: row.ApartmentName,
		}
	}
	return result, total, nil
}

func (r *contractRepository) FindByID(ctx context.Context, id uuid.UUID) (*ContractWithRelations, error) {
	type joinRow struct {
		Contract
		TenantName    string    `gorm:"column:tenant_name"`
		TenantPhone   string    `gorm:"column:tenant_phone"`
		RoomNumber    string    `gorm:"column:room_number"`
		ApartmentID   uuid.UUID `gorm:"column:apt_id"`
		ApartmentName string    `gorm:"column:apt_name"`
	}

	var row joinRow
	err := database.DB(ctx, r.db).
		Model(&Contract{}).
		Select(`contracts.*,
			tenants.full_name AS tenant_name,
			tenants.phone AS tenant_phone,
			rooms.number AS room_number,
			apartments.id AS apt_id,
			apartments.name AS apt_name`).
		Joins("JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("JOIN rooms ON rooms.id = contracts.room_id AND rooms.deleted_at IS NULL").
		Joins("JOIN apartments ON apartments.id = rooms.apartment_id AND apartments.deleted_at IS NULL").
		Where("contracts.id = ? AND contracts.deleted_at IS NULL", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}

	result := ContractWithRelations{
		Contract:      row.Contract,
		TenantName:    row.TenantName,
		TenantPhone:   row.TenantPhone,
		RoomNumber:    row.RoomNumber,
		ApartmentID:   row.ApartmentID,
		ApartmentName: row.ApartmentName,
	}
	return &result, nil
}

func (r *contractRepository) FindByIDSimple(ctx context.Context, id uuid.UUID) (*Contract, error) {
	var m Contract
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *contractRepository) Create(ctx context.Context, contract *Contract) error {
	return database.DB(ctx, r.db).Create(contract).Error
}

func (r *contractRepository) Update(ctx context.Context, contract *Contract) error {
	return database.DB(ctx, r.db).Model(contract).Select("*").Omit("deleted_at").Updates(contract).Error
}

func (r *contractRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&Contract{}, "id = ?", id).Error
}

func (r *contractRepository) HasActiveByRoomID(ctx context.Context, roomID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Model(&Contract{}).
		Where("room_id = ? AND status = ?", roomID, ContractStatusActive).
		Count(&count).Error
	return count > 0, err
}

func (r *contractRepository) HasActiveByTenantID(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Model(&Contract{}).
		Where("tenant_id = ? AND status = ?", tenantID, ContractStatusActive).
		Count(&count).Error
	return count > 0, err
}

// FindActiveContractStartDatesByRoomIDs returns the start date of the current active contract per room.
// If a room has multiple active contracts (data anomaly), picks the latest start_date.
// Rooms without an active contract are omitted from the result.
func (r *contractRepository) FindActiveContractStartDatesByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]time.Time{}, nil
	}

	type row struct {
		RoomID    uuid.UUID `gorm:"column:room_id"`
		StartDate time.Time `gorm:"column:start_date"`
	}

	// DISTINCT ON picks one row per room_id; ORDER BY start_date DESC = latest contract wins
	var rows []row
	err := database.DB(ctx, r.db).
		Raw(`SELECT DISTINCT ON (room_id) room_id, start_date
			 FROM contracts
			 WHERE room_id IN ? AND status = ? AND deleted_at IS NULL
			 ORDER BY room_id, start_date DESC`,
			roomIDs, ContractStatusActive).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make(map[uuid.UUID]time.Time, len(rows))
	for _, r := range rows {
		result[r.RoomID] = r.StartDate
	}
	return result, nil
}

// FindByRoomIDWithTenants returns all contracts for a room with tenant names, ordered by start_date DESC.
// Used by meter reading history for tenant segmentation overlay.
func (r *contractRepository) FindByRoomIDWithTenants(ctx context.Context, roomID uuid.UUID) ([]ContractTenantSummary, error) {
	type row struct {
		TenantName string         `gorm:"column:tenant_name"`
		StartDate  time.Time      `gorm:"column:start_date"`
		EndDate    *time.Time     `gorm:"column:end_date"`
		Status     ContractStatus `gorm:"column:status"`
	}

	var rows []row
	err := database.DB(ctx, r.db).
		Model(&Contract{}).
		Select(`tenants.full_name AS tenant_name,
			contracts.start_date,
			contracts.end_date,
			contracts.status`).
		Joins("JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("contracts.room_id = ? AND contracts.deleted_at IS NULL", roomID).
		Order("contracts.start_date DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make([]ContractTenantSummary, len(rows))
	for i, row := range rows {
		result[i] = ContractTenantSummary{
			TenantName: row.TenantName,
			StartDate:  row.StartDate,
			EndDate:    row.EndDate,
			Status:     row.Status,
		}
	}
	return result, nil
}
