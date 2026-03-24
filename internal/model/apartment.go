package model

import (
	"time"

	"nana/internal/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Apartment struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	Name                   string         `gorm:"type:varchar(255);not null"`
	DisplayOrder           int            `gorm:"not null;default:0"`
	ElectricityRatePerUnit int64          `gorm:"not null;default:0"`
	WaterRatePerUnit       int64          `gorm:"not null;default:0"`
	Address                string         `gorm:"type:text;not null;default:''"`
	TaxID                  string         `gorm:"type:varchar(20);not null;default:''"`
	CreatedAt              time.Time      `gorm:"not null;default:now()"`
	UpdatedAt              time.Time      `gorm:"not null;default:now()"`
	DeletedAt              gorm.DeletedAt `gorm:"index"`
}

func (Apartment) TableName() string { return "apartments" }

func (a *Apartment) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

func (a *Apartment) ToDomain() domain.Apartment {
	return domain.Apartment{
		ID:                     a.ID,
		Name:                   a.Name,
		DisplayOrder:           a.DisplayOrder,
		ElectricityRatePerUnit: a.ElectricityRatePerUnit,
		WaterRatePerUnit:       a.WaterRatePerUnit,
		Address:                a.Address,
		TaxID:                  a.TaxID,
		CreatedAt:              a.CreatedAt,
		UpdatedAt:              a.UpdatedAt,
	}
}

func ApartmentFromDomain(d domain.Apartment) Apartment {
	return Apartment{
		ID:                     d.ID,
		Name:                   d.Name,
		DisplayOrder:           d.DisplayOrder,
		ElectricityRatePerUnit: d.ElectricityRatePerUnit,
		WaterRatePerUnit:       d.WaterRatePerUnit,
		Address:                d.Address,
		TaxID:                  d.TaxID,
		CreatedAt:              d.CreatedAt,
		UpdatedAt:              d.UpdatedAt,
	}
}

// ApartmentBankAccount

type ApartmentBankAccount struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	ApartmentID   uuid.UUID      `gorm:"type:uuid;not null"`
	BankName      string         `gorm:"type:varchar(100);not null"`
	AccountName   string         `gorm:"type:varchar(255);not null"`
	AccountNumber string         `gorm:"type:varchar(50);not null"`
	PromptPayID   *string        `gorm:"column:promptpay_id;type:varchar(20)"`
	IsPrimary     bool           `gorm:"not null;default:false"`
	Note          *string        `gorm:"type:text"`
	CreatedAt     time.Time      `gorm:"not null;default:now()"`
	UpdatedAt     time.Time      `gorm:"not null;default:now()"`
	DeletedAt     gorm.DeletedAt `gorm:"index"`

	Apartment *Apartment `gorm:"foreignKey:ApartmentID"`
}

func (ApartmentBankAccount) TableName() string { return "apartment_bank_accounts" }

func (b *ApartmentBankAccount) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

func (b *ApartmentBankAccount) ToDomain() domain.ApartmentBankAccount {
	return domain.ApartmentBankAccount{
		ID:            b.ID,
		ApartmentID:   b.ApartmentID,
		BankName:      b.BankName,
		AccountName:   b.AccountName,
		AccountNumber: b.AccountNumber,
		PromptPayID:   b.PromptPayID,
		IsPrimary:     b.IsPrimary,
		Note:          b.Note,
		CreatedAt:     b.CreatedAt,
		UpdatedAt:     b.UpdatedAt,
	}
}

func ApartmentBankAccountFromDomain(d domain.ApartmentBankAccount) ApartmentBankAccount {
	return ApartmentBankAccount{
		ID:            d.ID,
		ApartmentID:   d.ApartmentID,
		BankName:      d.BankName,
		AccountName:   d.AccountName,
		AccountNumber: d.AccountNumber,
		PromptPayID:   d.PromptPayID,
		IsPrimary:     d.IsPrimary,
		Note:          d.Note,
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
	}
}
