package billingconfig

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Types ---

type FeeType string

const (
	FeeTypeCleaningFee FeeType = "CLEANING_FEE"
	FeeTypeKeyService  FeeType = "KEY_SERVICE"
)

// ValidFeeTypes lists all allowed fee types for billing configs.
var ValidFeeTypes = []FeeType{
	FeeTypeCleaningFee,
	FeeTypeKeyService,
}

// --- Model ---

type BillingConfig struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID   uuid.UUID      `gorm:"type:uuid;not null" json:"apartment_id"`
	FeeType       FeeType        `gorm:"type:varchar(30);not null" json:"fee_type"`
	DefaultAmount int64          `gorm:"not null;default:0" json:"default_amount"`
	IsActive      bool           `gorm:"not null;default:true" json:"is_active"`
	CreatedAt     time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

func (BillingConfig) TableName() string { return "billing_configs" }

func (b *BillingConfig) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

// --- Domain methods ---

func IsValidFeeType(ft string) bool {
	for _, v := range ValidFeeTypes {
		if string(v) == ft {
			return true
		}
	}
	return false
}
