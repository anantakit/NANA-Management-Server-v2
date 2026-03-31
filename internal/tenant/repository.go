package tenant

import (
	"context"
	"fmt"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TenantRepository interface {
	FindAll(ctx context.Context, params pagination.PaginationParams) ([]Tenant, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Tenant, error)
	Create(ctx context.Context, tenant *Tenant) error
	Update(ctx context.Context, tenant *Tenant) error
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

func (r *tenantRepository) FindAll(ctx context.Context, params pagination.PaginationParams) ([]Tenant, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).Model(&Tenant{})

	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("full_name ILIKE ? OR id_card ILIKE ? OR phone ILIKE ?", search, search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"full_name", "id_card", "created_at"}, "full_name")
	if params.Sort == "" {
		order = "asc"
	}
	orderClause := fmt.Sprintf("%s %s", col, order)

	var tenants []Tenant
	if err := query.Order(orderClause).Offset(params.Offset()).Limit(params.Limit).Find(&tenants).Error; err != nil {
		return nil, 0, err
	}

	return tenants, total, nil
}

func (r *tenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var t Tenant
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *tenantRepository) Create(ctx context.Context, tenant *Tenant) error {
	return database.DB(ctx, r.db).Create(tenant).Error
}

func (r *tenantRepository) Update(ctx context.Context, tenant *Tenant) error {
	return database.DB(ctx, r.db).Model(tenant).Select("*").Omit("deleted_at").Updates(tenant).Error
}

func (r *tenantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&Tenant{}, "id = ?", id).Error
}

func (r *tenantRepository) ExistsByIDCard(ctx context.Context, idCard string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Tenant{}).Where("id_card = ?", idCard).Count(&count).Error
	return count > 0, err
}

func (r *tenantRepository) ExistsByIDCardExcluding(ctx context.Context, idCard string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Tenant{}).Where("id_card = ? AND id != ?", idCard, excludeID).Count(&count).Error
	return count > 0, err
}
