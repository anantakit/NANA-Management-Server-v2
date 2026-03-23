package model

import (
	"time"

	"nana/internal/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Payment struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	BillID     uuid.UUID  `gorm:"type:uuid;not null"`
	Amount     int64      `gorm:"not null;default:0"`
	Method     string     `gorm:"type:varchar(20);not null"`
	PaidAt     time.Time  `gorm:"not null;default:now()"`
	ReceivedBy *uuid.UUID `gorm:"type:uuid"`
	CreatedAt  time.Time  `gorm:"not null;default:now()"`

	Bill *Bill `gorm:"foreignKey:BillID"`
}

func (Payment) TableName() string { return "payments" }

func (p *Payment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

func (p *Payment) ToDomain() domain.Payment {
	return domain.Payment{
		ID:         p.ID,
		BillID:     p.BillID,
		Amount:     p.Amount,
		Method:     domain.PaymentMethod(p.Method),
		PaidAt:     p.PaidAt,
		ReceivedBy: p.ReceivedBy,
		CreatedAt:  p.CreatedAt,
	}
}

func PaymentFromDomain(d domain.Payment) Payment {
	return Payment{
		ID:         d.ID,
		BillID:     d.BillID,
		Amount:     d.Amount,
		Method:     string(d.Method),
		PaidAt:     d.PaidAt,
		ReceivedBy: d.ReceivedBy,
		CreatedAt:  d.CreatedAt,
	}
}
