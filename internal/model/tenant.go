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
	IDCardNumber     string         `gorm:"type:varchar(20)"`
	Phone            string         `gorm:"type:varchar(20)"`
	Email            string         `gorm:"type:varchar(255)"`
	EmergencyContact string         `gorm:"type:varchar(255)"`
	EmergencyPhone   string         `gorm:"type:varchar(20)"`
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
		IDCardNumber:     t.IDCardNumber,
		Phone:            t.Phone,
		Email:            t.Email,
		EmergencyContact: t.EmergencyContact,
		EmergencyPhone:   t.EmergencyPhone,
		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
	}
}

func TenantFromDomain(d domain.Tenant) Tenant {
	return Tenant{
		ID:               d.ID,
		FullName:         d.FullName,
		IDCardNumber:     d.IDCardNumber,
		Phone:            d.Phone,
		Email:            d.Email,
		EmergencyContact: d.EmergencyContact,
		EmergencyPhone:   d.EmergencyPhone,
		CreatedAt:        d.CreatedAt,
		UpdatedAt:        d.UpdatedAt,
	}
}
