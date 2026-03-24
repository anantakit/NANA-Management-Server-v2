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
	Address                string    `json:"address"`
	TaxID                  string    `json:"tax_id"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type ApartmentBankAccount struct {
	ID          uuid.UUID `json:"id"`
	ApartmentID uuid.UUID `json:"apartment_id"`
	BankName    string    `json:"bank_name"`
	AccountName string    `json:"account_name"`
	AccountNumber string  `json:"account_number"`
	PromptPayID *string   `json:"promptpay_id"`
	IsPrimary   bool      `json:"is_primary"`
	Note        *string   `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
