package model

import (
	"time"

	"nana/internal/domain"
	"nana/internal/room"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Bill struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	ContractID        uuid.UUID `gorm:"type:uuid;not null"`
	RoomID            uuid.UUID `gorm:"type:uuid;not null"`
	BillingMonth      string    `gorm:"type:varchar(7);not null"`
	BillDate          time.Time `gorm:"type:date;not null"`
	Type              string    `gorm:"type:varchar(20);not null"`
	RoomCharge        int64     `gorm:"not null;default:0"`
	ElectricityUnits  int       `gorm:"not null;default:0"`
	ElectricityCharge int64     `gorm:"not null;default:0"`
	WaterUnits        int       `gorm:"not null;default:0"`
	WaterCharge       int64     `gorm:"not null;default:0"`
	TotalAmount       int64     `gorm:"not null;default:0"`
	DueDate           time.Time `gorm:"type:date;not null"`
	Status            string    `gorm:"type:varchar(20);not null;default:'PENDING'"`
	DepositDeduction  int64     `gorm:"not null;default:0"`
	CreatedAt         time.Time `gorm:"not null;default:now()"`
	UpdatedAt         time.Time `gorm:"not null;default:now()"`

	Contract *Contract `gorm:"foreignKey:ContractID"`
	Room     *room.Room `gorm:"foreignKey:RoomID"`
}

func (Bill) TableName() string { return "bills" }

func (b *Bill) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

func (b *Bill) ToDomain() domain.Bill {
	return domain.Bill{
		ID:                b.ID,
		ContractID:        b.ContractID,
		RoomID:            b.RoomID,
		BillingMonth:      b.BillingMonth,
		BillDate:          b.BillDate,
		Type:              domain.BillType(b.Type),
		RoomCharge:        b.RoomCharge,
		ElectricityUnits:  b.ElectricityUnits,
		ElectricityCharge: b.ElectricityCharge,
		WaterUnits:        b.WaterUnits,
		WaterCharge:       b.WaterCharge,
		TotalAmount:       b.TotalAmount,
		DueDate:           b.DueDate,
		Status:            domain.BillStatus(b.Status),
		DepositDeduction:  b.DepositDeduction,
		CreatedAt:         b.CreatedAt,
		UpdatedAt:         b.UpdatedAt,
	}
}

func BillFromDomain(d domain.Bill) Bill {
	return Bill{
		ID:                d.ID,
		ContractID:        d.ContractID,
		RoomID:            d.RoomID,
		BillingMonth:      d.BillingMonth,
		BillDate:          d.BillDate,
		Type:              string(d.Type),
		RoomCharge:        d.RoomCharge,
		ElectricityUnits:  d.ElectricityUnits,
		ElectricityCharge: d.ElectricityCharge,
		WaterUnits:        d.WaterUnits,
		WaterCharge:       d.WaterCharge,
		TotalAmount:       d.TotalAmount,
		DueDate:           d.DueDate,
		Status:            string(d.Status),
		DepositDeduction:  d.DepositDeduction,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}
