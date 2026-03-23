package domain

import (
	"time"

	"github.com/google/uuid"
)

type Apartment struct {
	ID                     uuid.UUID `json:"id"`
	Name                   string    `json:"name"`
	DisplayOrder           int       `json:"display_order"`
	ElectricityRatePerUnit int64     `json:"electricity_rate_per_unit"`
	WaterRatePerUnit       int64     `json:"water_rate_per_unit"`
	AddressDetails         string    `json:"address_details"`
	ProvinceID             int       `json:"province_id"`
	DistrictID             int       `json:"district_id"`
	SubdistrictID          int       `json:"subdistrict_id"`
	TaxID                  string    `json:"tax_id"`
	BankName               string    `json:"bank_name"`
	BankAccountName        string    `json:"bank_account_name"`
	BankAccountNumber      string    `json:"bank_account_number"`
	PromptPayID            *string   `json:"promptpay_id"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}
