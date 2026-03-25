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

type TenantRepository interface {
	FindAll(ctx context.Context, params dto.PaginationParams) ([]domain.Tenant, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
	Create(ctx context.Context, tenant *domain.Tenant) error
	Update(ctx context.Context, tenant *domain.Tenant) error
	Delete(ctx context.Context, id uuid.UUID) error
	ExistsByIDCard(ctx context.Context, idCard string) (bool, error)
	ExistsByIDCardExcluding(ctx context.Context, idCard string, excludeID uuid.UUID) (bool, error)
}

type tenantRepository struct {
	db *gorm.DB
}

func NewTenantRepository(db *gorm.DB) TenantRepository {
	return &tenantRepository{db: db}
}

func (r *tenantRepository) FindAll(ctx context.Context, params dto.PaginationParams) ([]domain.Tenant, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).Model(&model.Tenant{})

	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("full_name ILIKE ? OR id_card ILIKE ? OR phone ILIKE ?", search, search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := dto.SafeSort(params.Sort, params.Order, []string{"full_name", "id_card", "created_at"}, "full_name")
	if params.Sort == "" {
		order = "asc"
	}
	orderClause := fmt.Sprintf("%s %s", col, order)

	var models []model.Tenant
	if err := query.Order(orderClause).Offset(params.Offset()).Limit(params.Limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]domain.Tenant, len(models))
	for i, m := range models {
		result[i] = m.ToDomain()
	}
	return result, total, nil
}

func (r *tenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	var m model.Tenant
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	d := m.ToDomain()
	return &d, nil
}

func (r *tenantRepository) Create(ctx context.Context, tenant *domain.Tenant) error {
	m := model.TenantFromDomain(*tenant)
	if err := database.DB(ctx, r.db).Create(&m).Error; err != nil {
		return err
	}
	*tenant = m.ToDomain()
	return nil
}

func (r *tenantRepository) Update(ctx context.Context, tenant *domain.Tenant) error {
	m := model.TenantFromDomain(*tenant)
	if err := database.DB(ctx, r.db).Model(&m).Select("*").Omit("deleted_at").Updates(&m).Error; err != nil {
		return err
	}
	*tenant = m.ToDomain()
	return nil
}

func (r *tenantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&model.Tenant{}, "id = ?", id).Error
}

func (r *tenantRepository) ExistsByIDCard(ctx context.Context, idCard string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&model.Tenant{}).Where("id_card = ?", idCard).Count(&count).Error
	return count > 0, err
}

func (r *tenantRepository) ExistsByIDCardExcluding(ctx context.Context, idCard string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&model.Tenant{}).Where("id_card = ? AND id != ?", idCard, excludeID).Count(&count).Error
	return count > 0, err
}
