package model

import (
	"time"

	"nana/internal/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Room struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	ApartmentID uuid.UUID      `gorm:"type:uuid;not null"`
	Number      string         `gorm:"type:varchar(20);not null"`
	Type        string         `gorm:"type:varchar(10);not null"`
	Floor       int            `gorm:"not null;default:1"`
	BaseRent    int64          `gorm:"not null;default:0"`
	BaseDeposit int64          `gorm:"not null;default:0"`
	Status      string         `gorm:"type:varchar(20);not null;default:'VACANT'"`
	CreatedAt   time.Time      `gorm:"not null;default:now()"`
	UpdatedAt   time.Time      `gorm:"not null;default:now()"`
	DeletedAt   gorm.DeletedAt `gorm:"index"`

	Apartment *Apartment `gorm:"foreignKey:ApartmentID"`
}

func (Room) TableName() string { return "rooms" }

func (r *Room) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

func (r *Room) ToDomain() domain.Room {
	return domain.Room{
		ID:          r.ID,
		ApartmentID: r.ApartmentID,
		Number:      r.Number,
		Type:        domain.RoomType(r.Type),
		Floor:       r.Floor,
		BaseRent:    r.BaseRent,
		BaseDeposit: r.BaseDeposit,
		Status:      domain.RoomStatus(r.Status),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func RoomFromDomain(d domain.Room) Room {
	return Room{
		ID:          d.ID,
		ApartmentID: d.ApartmentID,
		Number:      d.Number,
		Type:        string(d.Type),
		Floor:       d.Floor,
		BaseRent:    d.BaseRent,
		BaseDeposit: d.BaseDeposit,
		Status:      string(d.Status),
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
	}
}
