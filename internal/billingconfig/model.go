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
	// FeeTypeProrateDailyRate stores the per-day rate (in satang) used by
	// the settlement bill's prorate rent line. Lives alongside flat-fee
	// configs because the data shape is identical (apartment_id +
	// default_amount + is_active); the semantic difference (rate vs flat
	// charge) is handled in the billing service, not the config layer.
	FeeTypeProrateDailyRate FeeType = "PRORATE_DAILY_RATE"
	// FeeTypeLatePenalty stores the flat-fee rate (in satang) shown as a
	// compute-on-demand suggestion when an overdue bill is opened for
	// collection. Display-time hint only — never mutates an issued bill.
	// See backlog_late_payment_penalty.md for the v1 invariant lock.
	FeeTypeLatePenalty FeeType = "LATE_PENALTY"
)

// ValidFeeTypes lists all allowed fee types for billing configs.
var ValidFeeTypes = []FeeType{
	FeeTypeCleaningFee,
	FeeTypeKeyService,
	FeeTypeProrateDailyRate,
	FeeTypeLatePenalty,
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
