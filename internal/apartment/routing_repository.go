package apartment

import (
	"context"

	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PaymentDestinationRuleRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]PaymentDestinationRule, error)
	FindByID(ctx context.Context, id uuid.UUID) (*PaymentDestinationRule, error)
	Create(ctx context.Context, rule *PaymentDestinationRule) error
	Update(ctx context.Context, rule *PaymentDestinationRule) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type paymentDestinationRuleRepository struct {
	db *gorm.DB
}

func NewPaymentDestinationRuleRepository(db *gorm.DB) PaymentDestinationRuleRepository {
	return &paymentDestinationRuleRepository{db: db}
}

func (r *paymentDestinationRuleRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]PaymentDestinationRule, error) {
	var result []PaymentDestinationRule
	if err := database.DB(ctx, r.db).
		Where("apartment_id = ?", apartmentID).
		Order("rule_type ASC, created_at ASC").
		Find(&result).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (r *paymentDestinationRuleRepository) FindByID(ctx context.Context, id uuid.UUID) (*PaymentDestinationRule, error) {
	var m PaymentDestinationRule
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *paymentDestinationRuleRepository) Create(ctx context.Context, rule *PaymentDestinationRule) error {
	return database.DB(ctx, r.db).Create(rule).Error
}

func (r *paymentDestinationRuleRepository) Update(ctx context.Context, rule *PaymentDestinationRule) error {
	return database.DB(ctx, r.db).Model(rule).Select("*").Omit("deleted_at").Updates(rule).Error
}

func (r *paymentDestinationRuleRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&PaymentDestinationRule{}, "id = ?", id).Error
}
