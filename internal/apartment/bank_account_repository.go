package apartment

import (
	"context"

	"nana/internal/domain"
	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BankAccountRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]domain.ApartmentBankAccount, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ApartmentBankAccount, error)
	Create(ctx context.Context, account *domain.ApartmentBankAccount) error
	Update(ctx context.Context, account *domain.ApartmentBankAccount) error
	Delete(ctx context.Context, id uuid.UUID) error
	ClearPrimary(ctx context.Context, apartmentID uuid.UUID) error
}

type bankAccountRepository struct {
	db *gorm.DB
}

func NewBankAccountRepository(db *gorm.DB) BankAccountRepository {
	return &bankAccountRepository{db: db}
}

func (r *bankAccountRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]domain.ApartmentBankAccount, error) {
	var models []ApartmentBankAccount
	if err := database.DB(ctx, r.db).Where("apartment_id = ?", apartmentID).Order("is_primary DESC, created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]domain.ApartmentBankAccount, len(models))
	for i, m := range models {
		result[i] = m.ToDomain()
	}
	return result, nil
}

func (r *bankAccountRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ApartmentBankAccount, error) {
	var m ApartmentBankAccount
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	d := m.ToDomain()
	return &d, nil
}

func (r *bankAccountRepository) Create(ctx context.Context, account *domain.ApartmentBankAccount) error {
	m := ApartmentBankAccountFromDomain(*account)
	if err := database.DB(ctx, r.db).Create(&m).Error; err != nil {
		return err
	}
	*account = m.ToDomain()
	return nil
}

func (r *bankAccountRepository) Update(ctx context.Context, account *domain.ApartmentBankAccount) error {
	m := ApartmentBankAccountFromDomain(*account)
	if err := database.DB(ctx, r.db).Model(&m).Select("*").Omit("deleted_at").Updates(&m).Error; err != nil {
		return err
	}
	*account = m.ToDomain()
	return nil
}

func (r *bankAccountRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Delete(&ApartmentBankAccount{}, "id = ?", id).Error
}

func (r *bankAccountRepository) ClearPrimary(ctx context.Context, apartmentID uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&ApartmentBankAccount{}).
		Where("apartment_id = ? AND is_primary = true", apartmentID).
		Update("is_primary", false).Error
}
