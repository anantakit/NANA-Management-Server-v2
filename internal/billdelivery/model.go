package billdelivery

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BillDelivery struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BillID      uuid.UUID  `gorm:"type:uuid;not null"                             json:"bill_id"`
	ActorID     *uuid.UUID `gorm:"type:uuid"                                      json:"actor_id,omitempty"`
	Channel     string     `gorm:"type:text;not null;default:'LINE_MANUAL'"       json:"channel"`
	DeliveredAt time.Time  `gorm:"type:timestamptz;not null;default:now()"        json:"delivered_at"`
	Note        string     `gorm:"type:text;not null;default:''"                  json:"note"`
}

func (BillDelivery) TableName() string { return "bill_deliveries" }

func (d *BillDelivery) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}
