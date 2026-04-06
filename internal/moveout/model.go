package moveout

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Types ---

type MoveOutStatus string

// NOTE: when adding a status, update ComputeWorkflowStatus below — unknown
// values fall through to WorkflowCancelled and silently disappear from queues.
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
	ScheduledMoveOutDate time.Time   `gorm:"column:scheduled_move_out_date;type:date;not null" json:"scheduled_move_out_date"`
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

// ValidateDates checks that scheduled_move_out_date >= notice_date.
func (m *MoveOutNotice) ValidateDates() error {
	if m.ScheduledMoveOutDate.Before(m.NoticeDate) {
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

// --- Urgency (queue bucket relative to today) ---

type Urgency string

const (
	UrgencyOverdue Urgency = "OVERDUE" // days_until < 0
	UrgencyToday   Urgency = "TODAY"   // days_until == 0
	UrgencySoon    Urgency = "SOON"    // 1..7
	UrgencyNormal  Urgency = "NORMAL"  // > 7
)

// truncateToDateUTC normalizes to a UTC midnight stamp using the input's local
// calendar date. UTC avoids DST wall-clock anomalies (23h/25h days) so the
// subsequent diff is always a clean multiple of 24h.
func truncateToDateUTC(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// DaysUntil returns scheduled - today in whole days. Negative = overdue.
// Both inputs are reduced to their calendar date (input's own location) before
// comparison, then diffed in UTC to stay DST-safe.
func DaysUntil(scheduled, today time.Time) int {
	s := truncateToDateUTC(scheduled)
	t := truncateToDateUTC(today)
	return int(s.Sub(t) / (24 * time.Hour))
}

// ComputeUrgency returns the bucket for a scheduled move-out date relative to
// today. Both inputs are truncated to date (no time component) before compare.
func ComputeUrgency(scheduled, today time.Time) Urgency {
	d := DaysUntil(scheduled, today)
	switch {
	case d < 0:
		return UrgencyOverdue
	case d == 0:
		return UrgencyToday
	case d <= 7:
		return UrgencySoon
	default:
		return UrgencyNormal
	}
}

// --- Workflow status (persisted status + meter presence → effective state) ---

type WorkflowStatus string

const (
	WorkflowAwaitingMeter   WorkflowStatus = "AWAITING_METER"
	WorkflowReadyToComplete WorkflowStatus = "READY_TO_COMPLETE"
	WorkflowCompleted       WorkflowStatus = "COMPLETED"
	WorkflowCancelled       WorkflowStatus = "CANCELLED"
)

// ComputeWorkflowStatus maps a notice's persisted status + meter presence to a
// workflow state. Unknown statuses fall back to WorkflowCancelled defensively
// — surfacing the row as inert rather than crashing the queue list.
func ComputeWorkflowStatus(noticeStatus MoveOutStatus, hasExitMeter bool) WorkflowStatus {
	switch noticeStatus {
	case MoveOutStatusPending:
		if hasExitMeter {
			return WorkflowReadyToComplete
		}
		return WorkflowAwaitingMeter
	case MoveOutStatusCompleted:
		return WorkflowCompleted
	case MoveOutStatusCancelled:
		return WorkflowCancelled
	default:
		return WorkflowCancelled
	}
}
