package apartment

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Apartment struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	Name                   string         `gorm:"type:varchar(255);not null" json:"name"`
	DisplayOrder           int            `gorm:"not null;default:0" json:"display_order"`
	ElectricityRatePerUnit int64          `gorm:"not null;default:0" json:"electricity_rate_per_unit"`
	WaterRatePerUnit       int64          `gorm:"not null;default:0" json:"water_rate_per_unit"`
	Address                string         `gorm:"type:text;not null;default:''" json:"address"`
	TaxID                  string         `gorm:"type:varchar(20);not null;default:''" json:"tax_id"`
	CreatedAt              time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Apartment) TableName() string { return "apartments" }

func (a *Apartment) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// ApartmentBankAccount

type ApartmentBankAccount struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID   uuid.UUID      `gorm:"type:uuid;not null" json:"apartment_id"`
	BankName      string         `gorm:"type:varchar(100);not null" json:"bank_name"`
	AccountName   string         `gorm:"type:varchar(255);not null" json:"account_name"`
	AccountNumber string         `gorm:"type:varchar(50);not null" json:"account_number"`
	PromptPayID   *string        `gorm:"column:promptpay_id;type:varchar(20)" json:"promptpay_id"`
	IsPrimary     bool           `gorm:"not null;default:false" json:"is_primary"`
	Note          *string        `gorm:"type:text" json:"note"`
	CreatedAt     time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`

	ApartmentRef *Apartment `gorm:"foreignKey:ApartmentID" json:"-"`
}

func (ApartmentBankAccount) TableName() string { return "apartment_bank_accounts" }

func (b *ApartmentBankAccount) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

