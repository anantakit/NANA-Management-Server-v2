package payment

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nana/internal/shared/paymentmethod"
)

type BillPayment struct {
	ID         uuid.UUID                   `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BillID     uuid.UUID                   `gorm:"type:uuid;not null"                             json:"bill_id"`
	Amount     int64                       `gorm:"not null"                                       json:"amount"`
	Method     paymentmethod.PaymentMethod `gorm:"type:varchar(20);not null"                      json:"method"`
	Note       string                      `gorm:"type:text;not null;default:''"                  json:"note"`
	PaidAt     time.Time                   `gorm:"type:timestamptz;not null;default:now()"        json:"paid_at"`
	ReceivedBy *uuid.UUID                  `gorm:"type:uuid"                                      json:"received_by,omitempty"`
	CreatedAt  time.Time                   `gorm:"not null;default:now()"                         json:"created_at"`
}

func (BillPayment) TableName() string { return "bill_payments" }

func (p *BillPayment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
