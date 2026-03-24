package model

import (
	"time"

	"nana/internal/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Tenant struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	FullName         string         `gorm:"type:varchar(255);not null"`
	IDCard           string         `gorm:"type:varchar(13);not null;uniqueIndex"`
	Phone            string         `gorm:"type:varchar(20);not null"`
	Address          string         `gorm:"type:text;not null;default:''"`
	EmergencyContact string         `gorm:"type:varchar(255);not null;default:''"`
	CreatedAt        time.Time      `gorm:"not null;default:now()"`
	UpdatedAt        time.Time      `gorm:"not null;default:now()"`
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}

func (Tenant) TableName() string { return "tenants" }

func (t *Tenant) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

func (t *Tenant) ToDomain() domain.Tenant {
	return domain.Tenant{
		ID:               t.ID,
		FullName:         t.FullName,
		IDCard:           t.IDCard,
		Phone:            t.Phone,
		Address:          t.Address,
		EmergencyContact: t.EmergencyContact,

		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
	}
}

func TenantFromDomain(d domain.Tenant) Tenant {
	return Tenant{
		ID:               d.ID,
		FullName:         d.FullName,
		IDCard:           d.IDCard,
		Phone:            d.Phone,
		Address:          d.Address,
		EmergencyContact: d.EmergencyContact,
		CreatedAt:        d.CreatedAt,
		UpdatedAt:        d.UpdatedAt,
	}
}
