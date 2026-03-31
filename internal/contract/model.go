package contract

import (
	"errors"
	"time"

	"nana/internal/room"
	"nana/internal/tenant"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Types ---

type ContractStatus string

const (
	ContractStatusActive     ContractStatus = "ACTIVE"
	ContractStatusEnded      ContractStatus = "ENDED"
	ContractStatusTerminated ContractStatus = "TERMINATED"
)

type DepositStatus string

const (
	DepositStatusCollected DepositStatus = "COLLECTED"
	DepositStatusRefunded  DepositStatus = "REFUNDED"
	DepositStatusForfeited DepositStatus = "FORFEITED"
)

// --- Model ---

type Contract struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	TenantID               uuid.UUID      `gorm:"type:uuid;not null" json:"tenant_id"`
	RoomID                 uuid.UUID      `gorm:"type:uuid;not null" json:"room_id"`
	StartDate              time.Time      `gorm:"type:date;not null" json:"start_date"`
	MinMonths              int            `gorm:"not null;default:6" json:"min_months"`
	MonthlyRent            int64          `gorm:"not null;default:0" json:"monthly_rent"`
	DepositAmount          int64          `gorm:"not null;default:0" json:"deposit_amount"`
	DepositStatus          DepositStatus  `gorm:"type:varchar(20);not null;default:'COLLECTED'" json:"deposit_status"`
	ElectricityRatePerUnit int64          `gorm:"not null;default:0" json:"electricity_rate_per_unit"`
	WaterRatePerUnit       int64          `gorm:"not null;default:0" json:"water_rate_per_unit"`
	Status                 ContractStatus `gorm:"type:varchar(20);not null;default:'ACTIVE'" json:"status"`
	EndDate                *time.Time     `gorm:"type:date" json:"end_date"`
	MoveOutDate            *time.Time     `gorm:"type:date" json:"move_out_date"`
	CreatedAt              time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"-"`

	Tenant *tenant.Tenant `gorm:"foreignKey:TenantID" json:"-"`
	Room   *room.Room     `gorm:"foreignKey:RoomID" json:"-"`
}

func (Contract) TableName() string { return "contracts" }

func (c *Contract) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// --- Domain errors ---

var (
	ErrContractNotActive = errors.New("ไม่สามารถแก้ไขสัญญาที่ไม่ใช่สถานะใช้งาน")
	ErrContractIsActive  = errors.New("ไม่สามารถลบสัญญาที่ยังใช้งานอยู่")
)

// --- Domain methods (pure, no DB, no side effects) ---

func (c *Contract) IsActive() bool {
	return c.Status == ContractStatusActive
}

func (c *Contract) ValidateForUpdate() error {
	if !c.IsActive() {
		return ErrContractNotActive
	}
	return nil
}

func (c *Contract) CanBeDeleted() error {
	if c.IsActive() {
		return ErrContractIsActive
	}
	return nil
}
