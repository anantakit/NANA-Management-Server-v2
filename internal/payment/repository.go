package payment

import (
	"context"
	"fmt"

	"nana/internal/shared/database"

	"gorm.io/gorm"
)

type PaymentRepository interface {
	Create(ctx context.Context, p *BillPayment) error
}

type paymentRepository struct {
	db *gorm.DB
}

var _ PaymentRepository = (*paymentRepository)(nil)

func NewPaymentRepository(db *gorm.DB) PaymentRepository {
	return &paymentRepository{db: db}
}

func (r *paymentRepository) Create(ctx context.Context, p *BillPayment) error {
	if err := database.DB(ctx, r.db).Create(p).Error; err != nil {
		return fmt.Errorf("create bill payment: %w", err)
	}
	return nil
}
