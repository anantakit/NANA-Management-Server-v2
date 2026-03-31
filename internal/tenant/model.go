package tenant

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Tenant struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	FullName         string         `gorm:"type:varchar(255);not null" json:"full_name"`
	IDCard           string         `gorm:"type:varchar(13);not null;uniqueIndex" json:"id_card"`
	Phone            string         `gorm:"type:varchar(20);not null" json:"phone"`
	Address          string         `gorm:"type:text;not null;default:''" json:"address"`
	EmergencyContact string         `gorm:"type:varchar(255);not null;default:''" json:"emergency_contact"`
	CreatedAt        time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Tenant) TableName() string { return "tenants" }

func (t *Tenant) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}
