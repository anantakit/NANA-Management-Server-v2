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
	AddressDetails         string         `gorm:"type:text;not null;default:''"`
	ProvinceID             int            `gorm:"not null;default:0"`
	DistrictID             int            `gorm:"not null;default:0"`
	SubdistrictID          int            `gorm:"not null;default:0"`
	TaxID                  string         `gorm:"type:varchar(20);not null;default:''"`
	BankName               string         `gorm:"type:varchar(100);not null;default:''"`
	BankAccountName        string         `gorm:"type:varchar(255);not null;default:''"`
	BankAccountNumber      string         `gorm:"type:varchar(50);not null;default:''"`
	PromptPayID            *string        `gorm:"column:promptpay_id;type:varchar(20)"`
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
		AddressDetails:         a.AddressDetails,
		ProvinceID:             a.ProvinceID,
		DistrictID:             a.DistrictID,
		SubdistrictID:          a.SubdistrictID,
		TaxID:                  a.TaxID,
		BankName:               a.BankName,
		BankAccountName:        a.BankAccountName,
		BankAccountNumber:      a.BankAccountNumber,
		PromptPayID:            a.PromptPayID,
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
		AddressDetails:         d.AddressDetails,
		ProvinceID:             d.ProvinceID,
		DistrictID:             d.DistrictID,
		SubdistrictID:          d.SubdistrictID,
		TaxID:                  d.TaxID,
		BankName:               d.BankName,
		BankAccountName:        d.BankAccountName,
		BankAccountNumber:      d.BankAccountNumber,
		PromptPayID:            d.PromptPayID,
		CreatedAt:              d.CreatedAt,
		UpdatedAt:              d.UpdatedAt,
	}
}
