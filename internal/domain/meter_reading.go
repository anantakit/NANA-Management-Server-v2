package domain

import (
	"time"

	"github.com/google/uuid"
)

type MeterReading struct {
	ID                  uuid.UUID  `json:"id"`
	RoomID              uuid.UUID  `json:"room_id"`
	ReadingDate         time.Time  `json:"reading_date"`
	ElectricityPrevious int        `json:"electricity_previous"`
	ElectricityCurrent  int        `json:"electricity_current"`
	WaterPrevious       int        `json:"water_previous"`
	WaterCurrent        int        `json:"water_current"`
	ReadBy              *uuid.UUID `json:"read_by"`
	CreatedAt           time.Time  `json:"created_at"`
}
