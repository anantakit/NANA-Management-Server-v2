package model

import (
	"time"

	"nana/internal/domain"
	"nana/internal/room"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MeterReading struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	RoomID              uuid.UUID  `gorm:"type:uuid;not null"`
	ReadingDate         time.Time  `gorm:"type:date;not null"`
	ElectricityPrevious int        `gorm:"not null;default:0"`
	ElectricityCurrent  int        `gorm:"not null;default:0"`
	WaterPrevious       int        `gorm:"not null;default:0"`
	WaterCurrent        int        `gorm:"not null;default:0"`
	ReadBy              *uuid.UUID `gorm:"type:uuid"`
	CreatedAt           time.Time  `gorm:"not null;default:now()"`

	Room *room.Room `gorm:"foreignKey:RoomID"`
}

func (MeterReading) TableName() string { return "meter_readings" }

func (m *MeterReading) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

func (m *MeterReading) ToDomain() domain.MeterReading {
	return domain.MeterReading{
		ID:                  m.ID,
		RoomID:              m.RoomID,
		ReadingDate:         m.ReadingDate,
		ElectricityPrevious: m.ElectricityPrevious,
		ElectricityCurrent:  m.ElectricityCurrent,
		WaterPrevious:       m.WaterPrevious,
		WaterCurrent:        m.WaterCurrent,
		ReadBy:              m.ReadBy,
		CreatedAt:           m.CreatedAt,
	}
}

func MeterReadingFromDomain(d domain.MeterReading) MeterReading {
	return MeterReading{
		ID:                  d.ID,
		RoomID:              d.RoomID,
		ReadingDate:         d.ReadingDate,
		ElectricityPrevious: d.ElectricityPrevious,
		ElectricityCurrent:  d.ElectricityCurrent,
		WaterPrevious:       d.WaterPrevious,
		WaterCurrent:        d.WaterCurrent,
		ReadBy:              d.ReadBy,
		CreatedAt:           d.CreatedAt,
	}
}
