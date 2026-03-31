package meterreading

import (
	"errors"
	"time"

	"nana/internal/room"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Domain errors ---

var (
	ErrElectricityCurrentBelowPrevious = errors.New("ค่าไฟฟ้าปัจจุบันต้องมากกว่าหรือเท่ากับค่าก่อนหน้า")
	ErrWaterCurrentBelowPrevious       = errors.New("ค่าน้ำปัจจุบันต้องมากกว่าหรือเท่ากับค่าก่อนหน้า")
	ErrLatestRoomMismatch              = errors.New("ข้อมูลมิเตอร์ล่าสุดไม่ตรงกับห้อง")
	ErrReadingDateBeforeLatest         = errors.New("วันที่จดมิเตอร์ต้องไม่ย้อนหลังกว่าครั้งล่าสุด")
	ErrOnlyLatestCanBeUpdated          = errors.New("แก้ไขได้เฉพาะรายการจดมิเตอร์ล่าสุดเท่านั้น")
)

// --- Model ---

type MeterReading struct {
	ID                  uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	RoomID              uuid.UUID      `gorm:"type:uuid;not null" json:"room_id"`
	ReadingDate         time.Time      `gorm:"type:date;not null" json:"reading_date"`
	ElectricityPrevious int            `gorm:"not null;default:0" json:"electricity_previous"`
	ElectricityCurrent  int            `gorm:"not null;default:0" json:"electricity_current"`
	WaterPrevious       int            `gorm:"not null;default:0" json:"water_previous"`
	WaterCurrent        int            `gorm:"not null;default:0" json:"water_current"`
	ReadBy              *uuid.UUID     `gorm:"type:uuid" json:"read_by"`
	CreatedAt           time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt           time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`

	Room *room.Room `gorm:"foreignKey:RoomID" json:"-"`
}

func (MeterReading) TableName() string { return "meter_readings" }

func (m *MeterReading) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

// --- Domain methods (pure, no DB, no side effects) ---

func (m *MeterReading) ElectricityUsed() int {
	return m.ElectricityCurrent - m.ElectricityPrevious
}

func (m *MeterReading) WaterUsed() int {
	return m.WaterCurrent - m.WaterPrevious
}

func (m *MeterReading) validate() error {
	if m.ElectricityCurrent < m.ElectricityPrevious {
		return ErrElectricityCurrentBelowPrevious
	}
	if m.WaterCurrent < m.WaterPrevious {
		return ErrWaterCurrentBelowPrevious
	}
	return nil
}

// NewReading creates a MeterReading with auto-populated previous values.
// If isMeterReplaced is true, previous starts at 0.
// Otherwise, previous is carried over from the latest reading.
func NewReading(roomID uuid.UUID, readingDate time.Time, elecCurrent, waterCurrent int, latest *MeterReading, isMeterReplaced bool) (*MeterReading, error) {
	if latest != nil {
		// Guard: latest must belong to the same room
		if latest.RoomID != roomID {
			return nil, ErrLatestRoomMismatch
		}
		// Guard: reading date must not be before latest
		if readingDate.Before(latest.ReadingDate) {
			return nil, ErrReadingDateBeforeLatest
		}
	}

	var elecPrev, waterPrev int
	if !isMeterReplaced && latest != nil {
		elecPrev = latest.ElectricityCurrent
		waterPrev = latest.WaterCurrent
	}

	m := &MeterReading{
		RoomID:              roomID,
		ReadingDate:         readingDate,
		ElectricityPrevious: elecPrev,
		ElectricityCurrent:  elecCurrent,
		WaterPrevious:       waterPrev,
		WaterCurrent:        waterCurrent,
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// CanUpdate checks that only the latest reading for a room can be updated.
func (m *MeterReading) CanUpdate(latestID uuid.UUID) error {
	if m.ID != latestID {
		return ErrOnlyLatestCanBeUpdated
	}
	return nil
}

// ApplyUpdate mutates current values and re-validates.
// Caller must verify CanUpdate() first.
func (m *MeterReading) ApplyUpdate(elecCurrent, waterCurrent *int) error {
	if elecCurrent != nil {
		m.ElectricityCurrent = *elecCurrent
	}
	if waterCurrent != nil {
		m.WaterCurrent = *waterCurrent
	}
	return m.validate()
}
