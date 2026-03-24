package dto

import (
	"nana/internal/domain"
	"nana/internal/money"
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

type RoomResponse struct {
	ID          string  `json:"id"`
	ApartmentID string  `json:"apartment_id"`
	Number      string  `json:"number"`
	Type        string  `json:"type"`
	Floor       int     `json:"floor"`
	BaseRent    float64 `json:"base_rent"`
	BaseDeposit float64 `json:"base_deposit"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func ToRoomResponse(r domain.Room) RoomResponse {
	return RoomResponse{
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
}

func ToRoomResponseList(rooms []domain.Room) []RoomResponse {
	result := make([]RoomResponse, len(rooms))
	for i, r := range rooms {
		result[i] = ToRoomResponse(r)
	}
	return result
}
