package contract

import (
	"nana/internal/shared/money"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
)

type CreateContractRequest struct {
	TenantID               string  `json:"tenant_id" validate:"required,uuid"`
	RoomID                 string  `json:"room_id" validate:"required,uuid"`
	StartDate              string  `json:"start_date" validate:"required"`
	MinMonths              int     `json:"min_months" validate:"required,min=1"`
	MonthlyRent            float64 `json:"monthly_rent" validate:"min=0"`
	DepositAmount          float64 `json:"deposit_amount" validate:"min=0"`
	ElectricityRatePerUnit float64 `json:"electricity_rate_per_unit" validate:"min=0"`
	WaterRatePerUnit       float64 `json:"water_rate_per_unit" validate:"min=0"`
}

type UpdateContractRequest struct {
	MinMonths              *int     `json:"min_months" validate:"omitempty,min=1"`
	MonthlyRent            *float64 `json:"monthly_rent" validate:"omitempty,min=0"`
	DepositAmount          *float64 `json:"deposit_amount" validate:"omitempty,min=0"`
	ElectricityRatePerUnit *float64 `json:"electricity_rate_per_unit" validate:"omitempty,min=0"`
	WaterRatePerUnit       *float64 `json:"water_rate_per_unit" validate:"omitempty,min=0"`
}

type ContractListParams struct {
	pagination.PaginationParams
	Status      string `query:"status" validate:"omitempty,oneof=ACTIVE ENDED TERMINATED"`
	ApartmentID string `query:"apartment_id" validate:"omitempty,uuid"`
	RoomID      string `query:"room_id" validate:"omitempty,uuid"`
}

type ContractResponse struct {
	ID                     string  `json:"id"`
	TenantID               string  `json:"tenant_id"`
	RoomID                 string  `json:"room_id"`
	StartDate              string  `json:"start_date"`
	MinMonths              int     `json:"min_months"`
	MonthlyRent            float64 `json:"monthly_rent"`
	DepositAmount          float64 `json:"deposit_amount"`
	DepositStatus          string  `json:"deposit_status"`
	ElectricityRatePerUnit float64 `json:"electricity_rate_per_unit"`
	WaterRatePerUnit       float64 `json:"water_rate_per_unit"`
	Status                 string  `json:"status"`
	EndDate                *string `json:"end_date"`
	MoveOutNoticeID        *string `json:"move_out_notice_id"`
	TenantName             string  `json:"tenant_name"`
	TenantPhone            string  `json:"tenant_phone"`
	RoomNumber             string  `json:"room_number"`
	ApartmentID            string  `json:"apartment_id"`
	ApartmentName          string  `json:"apartment_name"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

// ContractWithRelations holds joined data from contracts + tenants + rooms + apartments.
type ContractWithRelations struct {
	Contract
	TenantName      string
	TenantPhone     string
	RoomNumber      string
	ApartmentID     uuid.UUID
	ApartmentName   string
	MoveOutNoticeID *string
}

func ToContractResponse(c ContractWithRelations) ContractResponse {
	var endDate *string
	if c.EndDate != nil {
		s := c.EndDate.Format("2006-01-02")
		endDate = &s
	}

	return ContractResponse{
		ID:                     c.ID.String(),
		TenantID:               c.TenantID.String(),
		RoomID:                 c.RoomID.String(),
		StartDate:              c.StartDate.Format("2006-01-02"),
		MinMonths:              c.MinMonths,
		MonthlyRent:            money.ToBaht(c.MonthlyRent),
		DepositAmount:          money.ToBaht(c.DepositAmount),
		DepositStatus:          string(c.DepositStatus),
		ElectricityRatePerUnit: money.ToBaht(c.ElectricityRatePerUnit),
		WaterRatePerUnit:       money.ToBaht(c.WaterRatePerUnit),
		Status:                 string(c.Status),
		EndDate:                endDate,
		MoveOutNoticeID:        c.MoveOutNoticeID,
		TenantName:             c.TenantName,
		TenantPhone:            c.TenantPhone,
		RoomNumber:             c.RoomNumber,
		ApartmentID:            c.ApartmentID.String(),
		ApartmentName:          c.ApartmentName,
		CreatedAt:              c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:              c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToContractResponseList(contracts []ContractWithRelations) []ContractResponse {
	result := make([]ContractResponse, len(contracts))
	for i, c := range contracts {
		result[i] = ToContractResponse(c)
	}
	return result
}
