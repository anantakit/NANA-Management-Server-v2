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
	MoveOutStatusPendingMeter      MoveOutStatus = "PENDING_METER"
	MoveOutStatusPendingSettlement MoveOutStatus = "PENDING_SETTLEMENT"
	MoveOutStatusPendingPayment    MoveOutStatus = "PENDING_PAYMENT"
	MoveOutStatusReadyToClose      MoveOutStatus = "READY_TO_CLOSE"
	MoveOutStatusCompleted         MoveOutStatus = "COMPLETED"
	MoveOutStatusCancelled         MoveOutStatus = "CANCELLED"
)

type PaymentOutcome string

const (
	PaymentOutcomePaidExtra   PaymentOutcome = "PAID_EXTRA"
	PaymentOutcomeRefunded    PaymentOutcome = "REFUNDED"
	PaymentOutcomeZeroBalance PaymentOutcome = "ZERO_BALANCE"
)

// --- Model ---

type MoveOutNotice struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ContractID           uuid.UUID      `gorm:"type:uuid;not null" json:"contract_id"`
	NoticeDate           time.Time      `gorm:"type:date;not null" json:"notice_date"`
	ScheduledMoveOutDate time.Time      `gorm:"column:scheduled_move_out_date;type:date;not null" json:"scheduled_move_out_date"`
	// ActualMoveOutDate is the real date the tenant vacated the unit.
	// This is the source of truth for all financial calculations (rent prorate, utilities, settlement).
	// It is NOT tied to the date when settlement is generated.
	ActualMoveOutDate *time.Time     `gorm:"type:date" json:"actual_move_out_date,omitempty"`
	Status            MoveOutStatus  `gorm:"type:varchar(20);not null;default:'PENDING_METER'" json:"status"`
	Note                 string         `gorm:"type:text;not null;default:''" json:"note"`

	// V2 workflow columns
	SettlementBillID *uuid.UUID      `gorm:"type:uuid" json:"settlement_bill_id,omitempty"`
	NetAmount        *int64          `gorm:"type:bigint" json:"net_amount,omitempty"`
	PaymentOutcome   *PaymentOutcome `gorm:"type:varchar(20)" json:"payment_outcome,omitempty"`
	PaymentNote      string          `gorm:"type:text;not null;default:''" json:"payment_note"`
	ClosedAt         *time.Time      `gorm:"type:timestamptz" json:"closed_at,omitempty"`
	LastActionBy     *uuid.UUID      `gorm:"type:uuid" json:"last_action_by,omitempty"`
	LastActionAt     *time.Time      `gorm:"type:timestamptz" json:"last_action_at,omitempty"`

	CreatedAt time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
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
	ErrDateOrderInvalid       = errors.New("วันย้ายออกจริงต้องไม่ก่อนวันแจ้ง")
	ErrCannotCancel            = errors.New("ยกเลิกได้เฉพาะสถานะรอจดมิเตอร์หรือรอสร้างบิล")
	ErrCannotRecordSettlement  = errors.New("สร้างบิลได้เฉพาะสถานะรอสร้างบิล")
	ErrCannotAdvanceToPayment  = errors.New("ยืนยันบิลได้เฉพาะสถานะรอสร้างบิลที่มีบิลแนบแล้ว")
	ErrCannotRecordPayment     = errors.New("บันทึกชำระได้เฉพาะสถานะรอชำระ")
	ErrCannotClose             = errors.New("ปิดได้เฉพาะสถานะพร้อมปิด")
	ErrMissingSettlementBill   = errors.New("ต้องมีบิลสรุปก่อนปิด")
	ErrMissingPaymentOutcome   = errors.New("ต้องระบุผลการชำระก่อนปิด")
	ErrNotPendingMeter             = errors.New("ไม่สามารถดำเนินการได้ เนื่องจากสถานะไม่ใช่รอจดมิเตอร์")
	ErrActualMoveOutDateRequired   = errors.New("ต้องระบุวันย้ายออกจริงก่อนสร้างบิลสรุป")
	ErrActualDateBeforeContractStart = errors.New("วันย้ายออกจริงต้องไม่ก่อนวันเริ่มสัญญา")
	ErrCannotSetActualDate         = errors.New("ไม่สามารถตั้งวันย้ายออกจริงได้ในสถานะนี้")
)

// --- Status checks ---

func (m *MoveOutNotice) IsPendingMeter() bool      { return m.Status == MoveOutStatusPendingMeter }
func (m *MoveOutNotice) IsPendingSettlement() bool  { return m.Status == MoveOutStatusPendingSettlement }
func (m *MoveOutNotice) IsPendingPayment() bool     { return m.Status == MoveOutStatusPendingPayment }
func (m *MoveOutNotice) IsReadyToClose() bool       { return m.Status == MoveOutStatusReadyToClose }
func (m *MoveOutNotice) IsCompleted() bool           { return m.Status == MoveOutStatusCompleted }
func (m *MoveOutNotice) IsCancelled() bool           { return m.Status == MoveOutStatusCancelled }

// IsTerminal returns true for COMPLETED or CANCELLED (no further transitions).
func (m *MoveOutNotice) IsTerminal() bool {
	return m.IsCompleted() || m.IsCancelled()
}

// --- Domain methods (pure, no DB, no side effects) ---

// ValidateDates checks that scheduled_move_out_date >= notice_date.
func (m *MoveOutNotice) ValidateDates() error {
	if m.ScheduledMoveOutDate.Before(m.NoticeDate) {
		return ErrDateOrderInvalid
	}
	return nil
}

// --- ActualMoveOutDate methods ---

// CanSetActualDate returns nil if the actual move-out date can be set.
// Allowed in any non-terminal state.
func (m *MoveOutNotice) CanSetActualDate() error {
	if m.IsTerminal() {
		return ErrCannotSetActualDate
	}
	return nil
}

// SetActualDate sets the actual move-out date. Allowed in any non-terminal state.
func (m *MoveOutNotice) SetActualDate(d time.Time) error {
	if err := m.CanSetActualDate(); err != nil {
		return err
	}
	m.ActualMoveOutDate = &d
	return nil
}

// RequireActualDate returns the actual move-out date or an error if not set.
// Must be called before settlement generation.
func (m *MoveOutNotice) RequireActualDate() (time.Time, error) {
	if m.ActualMoveOutDate == nil {
		return time.Time{}, ErrActualMoveOutDateRequired
	}
	return *m.ActualMoveOutDate, nil
}

// --- Guard methods ---

// CanCancel returns nil if the notice can be cancelled.
// Only PENDING_METER and PENDING_SETTLEMENT are cancellable.
func (m *MoveOutNotice) CanCancel() error {
	if m.IsPendingMeter() || m.IsPendingSettlement() {
		return nil
	}
	return ErrCannotCancel
}

// CanAdvanceToSettlement returns nil if the notice can move to PENDING_SETTLEMENT.
func (m *MoveOutNotice) CanAdvanceToSettlement() error {
	if !m.IsPendingMeter() {
		return ErrNotPendingMeter
	}
	return nil
}

// CanRecordSettlement returns nil if a settlement draft can be generated/attached.
func (m *MoveOutNotice) CanRecordSettlement() error {
	if !m.IsPendingSettlement() {
		return ErrCannotRecordSettlement
	}
	return nil
}

// CanAdvanceToPayment returns nil if the notice can move to PENDING_PAYMENT.
// Requires PENDING_SETTLEMENT with a settlement bill attached.
func (m *MoveOutNotice) CanAdvanceToPayment() error {
	if !m.IsPendingSettlement() {
		return ErrCannotAdvanceToPayment
	}
	if m.SettlementBillID == nil {
		return ErrMissingSettlementBill
	}
	return nil
}

// CanRegenerateDraft returns nil if the settlement draft can be regenerated.
// Requires PENDING_SETTLEMENT with a draft already attached.
func (m *MoveOutNotice) CanRegenerateDraft() error {
	if !m.IsPendingSettlement() {
		return ErrCannotRecordSettlement
	}
	if m.SettlementBillID == nil {
		return ErrMissingSettlementBill
	}
	return nil
}

// CanRecordPayment returns nil if payment outcome can be recorded.
func (m *MoveOutNotice) CanRecordPayment() error {
	if !m.IsPendingPayment() {
		return ErrCannotRecordPayment
	}
	return nil
}

// CanClose returns nil if the notice can be closed (completed).
func (m *MoveOutNotice) CanClose() error {
	if !m.IsReadyToClose() {
		return ErrCannotClose
	}
	if m.SettlementBillID == nil {
		return ErrMissingSettlementBill
	}
	if m.PaymentOutcome == nil {
		return ErrMissingPaymentOutcome
	}
	return nil
}

// --- Transition methods ---

// AdvanceToSettlement moves PENDING_METER → PENDING_SETTLEMENT.
func (m *MoveOutNotice) AdvanceToSettlement() error {
	if err := m.CanAdvanceToSettlement(); err != nil {
		return err
	}
	m.Status = MoveOutStatusPendingSettlement
	return nil
}

// AttachDraft attaches a settlement draft bill. Stays in PENDING_SETTLEMENT.
func (m *MoveOutNotice) AttachDraft(billID uuid.UUID, netAmount int64) error {
	if err := m.CanRecordSettlement(); err != nil {
		return err
	}
	m.SettlementBillID = &billID
	m.NetAmount = &netAmount
	return nil
}

// AdvanceToPayment moves PENDING_SETTLEMENT → PENDING_PAYMENT after bill finalize.
func (m *MoveOutNotice) AdvanceToPayment() error {
	if err := m.CanAdvanceToPayment(); err != nil {
		return err
	}
	m.Status = MoveOutStatusPendingPayment
	return nil
}

// RecordPayment records payment outcome and moves → READY_TO_CLOSE.
func (m *MoveOutNotice) RecordPayment(outcome PaymentOutcome, note string) error {
	if err := m.CanRecordPayment(); err != nil {
		return err
	}
	m.PaymentOutcome = &outcome
	m.PaymentNote = note
	m.Status = MoveOutStatusReadyToClose
	return nil
}

// Close transitions READY_TO_CLOSE → COMPLETED.
func (m *MoveOutNotice) Close(now time.Time) error {
	if err := m.CanClose(); err != nil {
		return err
	}
	m.Status = MoveOutStatusCompleted
	m.ClosedAt = &now
	return nil
}

// Cancel transitions to CANCELLED.
func (m *MoveOutNotice) Cancel() error {
	if err := m.CanCancel(); err != nil {
		return err
	}
	m.Status = MoveOutStatusCancelled
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
