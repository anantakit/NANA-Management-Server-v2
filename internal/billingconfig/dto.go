package billingconfig

import (
	"nana/internal/shared/money"
)

type CreateBillingConfigRequest struct {
	FeeType       string  `json:"fee_type" validate:"required"`
	DefaultAmount float64 `json:"default_amount" validate:"min=0"`
	IsActive      *bool   `json:"is_active"`
}

type UpdateBillingConfigRequest struct {
	DefaultAmount *float64 `json:"default_amount" validate:"omitempty,min=0"`
	IsActive      *bool    `json:"is_active"`
}

type BillingConfigResponse struct {
	ID            string  `json:"id"`
	ApartmentID   string  `json:"apartment_id"`
	FeeType       string  `json:"fee_type"`
	DefaultAmount float64 `json:"default_amount"`
	IsActive      bool    `json:"is_active"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func ToBillingConfigResponse(b BillingConfig) BillingConfigResponse {
	return BillingConfigResponse{
		ID:            b.ID.String(),
		ApartmentID:   b.ApartmentID.String(),
		FeeType:       string(b.FeeType),
		DefaultAmount: money.ToBaht(b.DefaultAmount),
		IsActive:      b.IsActive,
		CreatedAt:     b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToBillingConfigResponseList(configs []BillingConfig) []BillingConfigResponse {
	result := make([]BillingConfigResponse, len(configs))
	for i, c := range configs {
		result[i] = ToBillingConfigResponse(c)
	}
	return result
}
