package room

import (
	"nana/internal/shared/money"
)

type CreateRoomRequest struct {
	Number      string  `json:"number" validate:"required,min=1,max=20"`
	Type        string  `json:"type" validate:"required,oneof=air fan"`
	Floor       int     `json:"floor" validate:"required,min=1"`
	BaseRent    float64 `json:"base_rent" validate:"min=0"`
	BaseDeposit float64 `json:"base_deposit" validate:"min=0"`
}

type UpdateRoomRequest struct {
	Number      *string  `json:"number" validate:"omitempty,min=1,max=20"`
	Type        *string  `json:"type" validate:"omitempty,oneof=air fan"`
	Floor       *int     `json:"floor" validate:"omitempty,min=1"`
	BaseRent    *float64 `json:"base_rent" validate:"omitempty,min=0"`
	BaseDeposit *float64 `json:"base_deposit" validate:"omitempty,min=0"`
	Status      *string  `json:"status" validate:"omitempty,oneof=VACANT MAINTENANCE"`
}

type ActiveContractSummary struct {
	ContractID             string  `json:"contract_id"`
	TenantID               string  `json:"tenant_id"`
	TenantName             string  `json:"tenant_name"`
	TenantPhone            string  `json:"tenant_phone"`
	MonthlyRent            float64 `json:"monthly_rent"`
	DepositAmount          float64 `json:"deposit_amount"`
	ElectricityRatePerUnit float64 `json:"electricity_rate_per_unit"`
	WaterRatePerUnit       float64 `json:"water_rate_per_unit"`
	StartDate              string  `json:"start_date"`
	MinMonths              int     `json:"min_months"`
}

type RoomResponse struct {
	ID             string                 `json:"id"`
	ApartmentID    string                 `json:"apartment_id"`
	Number         string                 `json:"number"`
	Type           string                 `json:"type"`
	Floor          int                    `json:"floor"`
	BaseRent       float64                `json:"base_rent"`
	BaseDeposit    float64                `json:"base_deposit"`
	Status         string                 `json:"status"`
	ActiveContract *ActiveContractSummary `json:"active_contract"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
}

// RoomWithContract holds room + optional active contract info (from JOIN).
type RoomWithContract struct {
	Room
	ContractID             *string
	TenantID               *string
	TenantName             *string
	TenantPhone            *string
	MonthlyRent            *int64
	DepositAmount          *int64
	ElectricityRatePerUnit *int64
	WaterRatePerUnit       *int64
	StartDate              *string
	MinMonths              *int
}

func ToRoomResponse(r Room) RoomResponse {
	return toRoomResponseWithContract(RoomWithContract{Room: r})
}

func toRoomResponseWithContract(r RoomWithContract) RoomResponse {
	resp := RoomResponse{
		ID:          r.ID.String(),
		ApartmentID: r.ApartmentID.String(),
		Number:      r.Number,
		Type:        string(r.Type),
		Floor:       r.Floor,
		BaseRent:    money.ToBaht(r.BaseRent),
		BaseDeposit: money.ToBaht(r.BaseDeposit),
		Status:      string(r.Status),
		CreatedAt:   r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   r.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	if r.ContractID != nil && *r.ContractID != "" {
		resp.ActiveContract = &ActiveContractSummary{
			ContractID:             *r.ContractID,
			TenantID:               derefStr(r.TenantID),
			TenantName:             derefStr(r.TenantName),
			TenantPhone:            derefStr(r.TenantPhone),
			MonthlyRent:            money.ToBaht(derefInt64(r.MonthlyRent)),
			DepositAmount:          money.ToBaht(derefInt64(r.DepositAmount)),
			ElectricityRatePerUnit: money.ToBaht(derefInt64(r.ElectricityRatePerUnit)),
			WaterRatePerUnit:       money.ToBaht(derefInt64(r.WaterRatePerUnit)),
			StartDate:              derefStr(r.StartDate),
			MinMonths:              derefInt(r.MinMonths),
		}
	}

	return resp
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func ToRoomResponseList(rooms []Room) []RoomResponse {
	result := make([]RoomResponse, len(rooms))
	for i, r := range rooms {
		result[i] = ToRoomResponse(r)
	}
	return result
}

func ToRoomWithContractResponseList(rooms []RoomWithContract) []RoomResponse {
	result := make([]RoomResponse, len(rooms))
	for i, r := range rooms {
		result[i] = toRoomResponseWithContract(r)
	}
	return result
}
