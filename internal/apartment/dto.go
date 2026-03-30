package apartment

import (
	"nana/internal/domain"
	"nana/internal/shared/money"
)

// Request DTOs

type CreateApartmentRequest struct {
	Name                   string  `json:"name" validate:"required,min=1,max=255"`
	DisplayOrder           int     `json:"display_order" validate:"min=0"`
	ElectricityRatePerUnit float64 `json:"electricity_rate_per_unit" validate:"min=0"`
	WaterRatePerUnit       float64 `json:"water_rate_per_unit" validate:"min=0"`
	Address                string  `json:"address" validate:"max=1000"`
	TaxID                  string  `json:"tax_id" validate:"max=20"`
}

type UpdateApartmentRequest struct {
	Name                   *string  `json:"name" validate:"omitempty,min=1,max=255"`
	DisplayOrder           *int     `json:"display_order" validate:"omitempty,min=0"`
	ElectricityRatePerUnit *float64 `json:"electricity_rate_per_unit" validate:"omitempty,min=0"`
	WaterRatePerUnit       *float64 `json:"water_rate_per_unit" validate:"omitempty,min=0"`
	Address                *string  `json:"address" validate:"omitempty,max=1000"`
	TaxID                  *string  `json:"tax_id" validate:"omitempty,max=20"`
}

// Response DTO

type ApartmentResponse struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	DisplayOrder           int     `json:"display_order"`
	ElectricityRatePerUnit float64 `json:"electricity_rate_per_unit"`
	WaterRatePerUnit       float64 `json:"water_rate_per_unit"`
	Address                string  `json:"address"`
	TaxID                  string  `json:"tax_id"`
	TotalRooms             int     `json:"total_rooms"`
	VacantRooms            int     `json:"vacant_rooms"`
	OccupiedRooms          int     `json:"occupied_rooms"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

func ToApartmentResponse(a domain.Apartment) ApartmentResponse {
	return ApartmentResponse{
		ID:                     a.ID.String(),
		Name:                   a.Name,
		DisplayOrder:           a.DisplayOrder,
		ElectricityRatePerUnit: money.ToBaht(a.ElectricityRatePerUnit),
		WaterRatePerUnit:       money.ToBaht(a.WaterRatePerUnit),
		Address:                a.Address,
		TaxID:                  a.TaxID,
		CreatedAt:              a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:              a.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToApartmentResponseList(apartments []domain.Apartment) []ApartmentResponse {
	result := make([]ApartmentResponse, len(apartments))
	for i, a := range apartments {
		result[i] = ToApartmentResponse(a)
	}
	return result
}
