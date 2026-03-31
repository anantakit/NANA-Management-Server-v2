package room

import (
	"errors"
	"time"

	"nana/internal/apartment"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID uuid.UUID      `gorm:"type:uuid;not null" json:"apartment_id"`
	Number      string         `gorm:"type:varchar(20);not null" json:"number"`
	Type        RoomType       `gorm:"type:varchar(10);not null" json:"type"`
	Floor       int            `gorm:"not null;default:1" json:"floor"`
	BaseRent    int64          `gorm:"not null;default:0" json:"base_rent"`
	BaseDeposit int64          `gorm:"not null;default:0" json:"base_deposit"`
	Status      RoomStatus     `gorm:"type:varchar(20);not null;default:'VACANT'" json:"status"`
	CreatedAt   time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	Apartment *apartment.Apartment `gorm:"foreignKey:ApartmentID" json:"-"`
}

func (Room) TableName() string { return "rooms" }

func (r *Room) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// --- Domain methods (pure, no DB, no side effects) ---

var (
	ErrRoomOccupiedStatus = errors.New("ไม่สามารถเปลี่ยนสถานะห้องที่มีผู้เช่าอยู่")
	ErrRoomOccupiedDelete = errors.New("ไม่สามารถลบห้องที่มีผู้เช่าอยู่")
)

func (r *Room) IsVacant() bool {
	return r.Status == RoomStatusVacant
}

func (r *Room) IsOccupied() bool {
	return r.Status == RoomStatusOccupied
}

func (r *Room) ValidateStatusChange() error {
	if r.IsOccupied() {
		return ErrRoomOccupiedStatus
	}
	return nil
}

func (r *Room) CanBeDeleted() error {
	if r.IsOccupied() {
		return ErrRoomOccupiedDelete
	}
	return nil
}
