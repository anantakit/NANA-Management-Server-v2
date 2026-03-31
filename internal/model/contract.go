package model

import (
	"time"

	"nana/internal/domain"
	"nana/internal/tenant"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Contract struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	TenantID               uuid.UUID      `gorm:"type:uuid;not null"`
	RoomID                 uuid.UUID      `gorm:"type:uuid;not null"`
	StartDate              time.Time      `gorm:"type:date;not null"`
	MinMonths              int            `gorm:"not null;default:6"`
	MonthlyRent            int64          `gorm:"not null;default:0"`
	DepositAmount          int64          `gorm:"not null;default:0"`
	DepositStatus          string         `gorm:"type:varchar(20);not null;default:'COLLECTED'"`
	ElectricityRatePerUnit int64          `gorm:"not null;default:0"`
	WaterRatePerUnit       int64          `gorm:"not null;default:0"`
	Status                 string         `gorm:"type:varchar(20);not null;default:'ACTIVE'"`
	EndDate                *time.Time     `gorm:"type:date"`
	MoveOutDate            *time.Time     `gorm:"type:date"`
	CreatedAt              time.Time      `gorm:"not null;default:now()"`
	UpdatedAt              time.Time      `gorm:"not null;default:now()"`
	DeletedAt              gorm.DeletedAt `gorm:"index"`

	Tenant *tenant.Tenant `gorm:"foreignKey:TenantID"`
	Room   *Room   `gorm:"foreignKey:RoomID"`
}

func (Contract) TableName() string { return "contracts" }

func (c *Contract) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

func (c *Contract) ToDomain() domain.Contract {
	return domain.Contract{
		ID:                     c.ID,
		TenantID:               c.TenantID,
		RoomID:                 c.RoomID,
		StartDate:              c.StartDate,
		MinMonths:              c.MinMonths,
		MonthlyRent:            c.MonthlyRent,
		DepositAmount:          c.DepositAmount,
		DepositStatus:          domain.DepositStatus(c.DepositStatus),
		ElectricityRatePerUnit: c.ElectricityRatePerUnit,
		WaterRatePerUnit:       c.WaterRatePerUnit,
		Status:                 domain.ContractStatus(c.Status),
		EndDate:                c.EndDate,
		MoveOutDate:            c.MoveOutDate,
		CreatedAt:              c.CreatedAt,
		UpdatedAt:              c.UpdatedAt,
	}
}

func ContractFromDomain(d domain.Contract) Contract {
	return Contract{
		ID:                     d.ID,
		TenantID:               d.TenantID,
		RoomID:                 d.RoomID,
		StartDate:              d.StartDate,
		MinMonths:              d.MinMonths,
		MonthlyRent:            d.MonthlyRent,
		DepositAmount:          d.DepositAmount,
		DepositStatus:          string(d.DepositStatus),
		ElectricityRatePerUnit: d.ElectricityRatePerUnit,
		WaterRatePerUnit:       d.WaterRatePerUnit,
		Status:                 string(d.Status),
		EndDate:                d.EndDate,
		MoveOutDate:            d.MoveOutDate,
		CreatedAt:              d.CreatedAt,
		UpdatedAt:              d.UpdatedAt,
	}
}
