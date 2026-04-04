package billingconfig

import (
	"context"

	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BillingConfigRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]BillingConfig, error)
	FindByID(ctx context.Context, id uuid.UUID) (*BillingConfig, error)
	FindByApartmentAndFeeType(ctx context.Context, apartmentID uuid.UUID, feeType FeeType) (*BillingConfig, error)
	Create(ctx context.Context, config *BillingConfig) error
	Update(ctx context.Context, config *BillingConfig) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type billingConfigRepository struct {
	db *gorm.DB
}

var _ BillingConfigRepository = (*billingConfigRepository)(nil)

func NewBillingConfigRepository(db *gorm.DB) BillingConfigRepository {
	return &billingConfigRepository{db: db}
}

func (r *billingConfigRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]BillingConfig, error) {
	var configs []BillingConfig
	err := database.DB(ctx, r.db).
		Where("apartment_id = ?", apartmentID).
		Order("fee_type ASC").
		Find(&configs).Error
	return configs, err
}

func (r *billingConfigRepository) FindByID(ctx context.Context, id uuid.UUID) (*BillingConfig, error) {
	var m BillingConfig
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *billingConfigRepository) FindByApartmentAndFeeType(ctx context.Context, apartmentID uuid.UUID, feeType FeeType) (*BillingConfig, error) {
	var m BillingConfig
	err := database.DB(ctx, r.db).
		Where("apartment_id = ? AND fee_type = ?", apartmentID, feeType).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *billingConfigRepository) Create(ctx context.Context, config *BillingConfig) error {
	return database.DB(ctx, r.db).Create(config).Error
}

func (r *billingConfigRepository) Update(ctx context.Context, config *BillingConfig) error {
	return database.DB(ctx, r.db).Model(config).Select("*").Omit("deleted_at").Updates(config).Error
}

func (r *billingConfigRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&BillingConfig{}, "id = ?", id).Error
}
