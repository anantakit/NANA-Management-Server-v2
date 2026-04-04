package moveout

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Types ---

type MoveOutStatus string

const (
	MoveOutStatusPending   MoveOutStatus = "PENDING"
	MoveOutStatusCompleted MoveOutStatus = "COMPLETED"
	MoveOutStatusCancelled MoveOutStatus = "CANCELLED"
)

// --- Model ---

type MoveOutNotice struct {
	ID                uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ContractID        uuid.UUID      `gorm:"type:uuid;not null" json:"contract_id"`
	NoticeDate        time.Time      `gorm:"type:date;not null" json:"notice_date"`
	ActualMoveOutDate time.Time      `gorm:"type:date;not null" json:"actual_move_out_date"`
	Status            MoveOutStatus  `gorm:"type:varchar(20);not null;default:'PENDING'" json:"status"`
	Note              string         `gorm:"type:text;not null;default:''" json:"note"`
	CreatedAt         time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt         time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}

func (MoveOutNotice) TableName() string { return "move_out_notices" }

func (m *MoveOutNotice) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

// --- Domain errors ---

var (
	ErrDateOrderInvalid = errors.New("วันย้ายออกจริงต้องไม่ก่อนวันแจ้ง")
	ErrNotPending       = errors.New("ไม่สามารถดำเนินการได้ เนื่องจากสถานะไม่ใช่รอดำเนินการ")
)

// --- Domain methods (pure, no DB, no side effects) ---

func (m *MoveOutNotice) IsPending() bool {
	return m.Status == MoveOutStatusPending
}

func (m *MoveOutNotice) IsCompleted() bool {
	return m.Status == MoveOutStatusCompleted
}

func (m *MoveOutNotice) IsCancelled() bool {
	return m.Status == MoveOutStatusCancelled
}

// ValidateDates checks that actual_move_out_date >= notice_date.
func (m *MoveOutNotice) ValidateDates() error {
	if m.ActualMoveOutDate.Before(m.NoticeDate) {
		return ErrDateOrderInvalid
	}
	return nil
}

// CanCancel checks if the notice can be cancelled.
func (m *MoveOutNotice) CanCancel() error {
	if !m.IsPending() {
		return ErrNotPending
	}
	return nil
}

// CanComplete checks if the notice can be completed.
func (m *MoveOutNotice) CanComplete() error {
	if !m.IsPending() {
		return ErrNotPending
	}
	return nil
}

// Cancel transitions the notice to CANCELLED.
func (m *MoveOutNotice) Cancel() error {
	if err := m.CanCancel(); err != nil {
		return err
	}
	m.Status = MoveOutStatusCancelled
	return nil
}

// Complete transitions the notice to COMPLETED.
func (m *MoveOutNotice) Complete() error {
	if err := m.CanComplete(); err != nil {
		return err
	}
	m.Status = MoveOutStatusCompleted
	return nil
}
