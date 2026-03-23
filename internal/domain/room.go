package domain

import (
	"time"

	"github.com/google/uuid"
)

type RoomType string

const (
	RoomTypeAir RoomType = "air"
	RoomTypeFan RoomType = "fan"
)

type RoomStatus string

const (
	RoomStatusVacant      RoomStatus = "VACANT"
	RoomStatusOccupied    RoomStatus = "OCCUPIED"
	RoomStatusMaintenance RoomStatus = "MAINTENANCE"
)

type Room struct {
	ID          uuid.UUID  `json:"id"`
	ApartmentID uuid.UUID  `json:"apartment_id"`
	Number      string     `json:"number"`
	Type        RoomType   `json:"type"`
	Floor       int        `json:"floor"`
	BaseRent    int64      `json:"base_rent"`
	BaseDeposit int64      `json:"base_deposit"`
	Status      RoomStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
