package billdelivery

import (
	"context"
	"fmt"

	"nana/internal/shared/database"

	"gorm.io/gorm"
)

type DeliveryRepository interface {
	Create(ctx context.Context, d *BillDelivery) error
}

type deliveryRepository struct {
	db *gorm.DB
}

var _ DeliveryRepository = (*deliveryRepository)(nil)

func NewDeliveryRepository(db *gorm.DB) DeliveryRepository {
	return &deliveryRepository{db: db}
}

func (r *deliveryRepository) Create(ctx context.Context, d *BillDelivery) error {
	if err := database.DB(ctx, r.db).Create(d).Error; err != nil {
		return fmt.Errorf("create bill delivery: %w", err)
	}
	return nil
}
