package moveout

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nana/internal/shared/paymentmethod"
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
	ID                   uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ContractID           uuid.UUID `gorm:"type:uuid;not null" json:"contract_id"`
	NoticeDate           time.Time `gorm:"type:date;not null" json:"notice_date"`
	ScheduledMoveOutDate time.Time `gorm:"column:scheduled_move_out_date;type:date;not null" json:"scheduled_move_out_date"`
	// ActualMoveOutDate is the real date the tenant vacated the unit.
	// This is the source of truth for all financial calculations (rent prorate, utilities, settlement).
	// It is NOT tied to the date when settlement is generated.
	ActualMoveOutDate *time.Time    `gorm:"type:date" json:"actual_move_out_date,omitempty"`
	Status            MoveOutStatus `gorm:"type:varchar(20);not null;default:'PENDING_METER'" json:"status"`
	Note              string        `gorm:"type:text;not null;default:''" json:"note"`

	// V2 workflow columns
	SettlementBillID *uuid.UUID                   `gorm:"type:uuid" json:"settlement_bill_id,omitempty"`
	NetAmount        *int64                       `gorm:"type:bigint" json:"net_amount,omitempty"`
	PaymentOutcome   *PaymentOutcome              `gorm:"type:varchar(20)" json:"payment_outcome,omitempty"`
	PaymentMethod    *paymentmethod.PaymentMethod `gorm:"column:payment_method;type:varchar(20)" json:"payment_method,omitempty"`
	PaymentNote      string                       `gorm:"type:text;not null;default:''" json:"payment_note"`
	ClosedAt         *time.Time                   `gorm:"type:timestamptz" json:"closed_at,omitempty"`
	CancelledAt      *time.Time                   `gorm:"type:timestamptz" json:"cancelled_at,omitempty"`
	LastActionBy     *uuid.UUID                   `gorm:"type:uuid" json:"last_action_by,omitempty"`
	LastActionAt     *time.Time                   `gorm:"type:timestamptz" json:"last_action_at,omitempty"`

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
	ErrDateOrderInvalid              = errors.New("วันย้ายออกจริงต้องไม่ก่อนวันแจ้ง")
	ErrCannotCancel                  = errors.New("ยกเลิกได้เฉพาะสถานะรอจดมิเตอร์หรือรอสร้างบิล")
	ErrCannotRecordSettlement        = errors.New("สร้างบิลได้เฉพาะสถานะรอสร้างบิล")
	ErrCannotAdvanceToPayment        = errors.New("สรุปยอดได้เฉพาะสถานะรอสร้างบิลที่มีบิลแนบแล้ว")
	ErrCannotRecordPayment           = errors.New("บันทึกชำระได้เฉพาะสถานะรอชำระ")
	ErrCannotSkipPayment             = errors.New("ข้ามชำระได้เฉพาะสถานะรอชำระ")
	ErrCannotClose                   = errors.New("ปิดได้เฉพาะสถานะพร้อมปิด")
	ErrCannotCloseWithUnsettled      = errors.New("ปิดงาน (ยังไม่ชำระ) ได้เฉพาะสถานะที่ยังไม่บันทึกการเงิน")
	ErrMissingSettlementBill         = errors.New("ต้องมีบิลปิดสัญญาก่อนปิด")
	ErrMissingPaymentOutcome         = errors.New("ต้องระบุผลการชำระก่อนปิด")
	ErrNotPendingMeter               = errors.New("ไม่สามารถดำเนินการได้ เนื่องจากสถานะไม่ใช่รอจดมิเตอร์")
	ErrActualMoveOutDateRequired     = errors.New("ต้องระบุวันย้ายออกจริงก่อนสร้างบิลปิดสัญญา")
	ErrActualDateBeforeContractStart = errors.New("วันย้ายออกจริงต้องไม่ก่อนวันเริ่มสัญญา")
	ErrCannotSetActualDate           = errors.New("ไม่สามารถตั้งวันย้ายออกจริงได้ในสถานะนี้")
	// ErrCannotDowngradeToPendingSettlement fires when the settlement-
	// correction "workflow rewind" is attempted from a status that doesn't
	// host a FINALIZED settlement bill — see CanDowngradeToPendingSettlement.
	ErrCannotDowngradeToPendingSettlement = errors.New("ลดสถานะกลับสู่รอสร้างบิลได้เฉพาะสถานะรอชำระหรือพร้อมปิด")
)

// --- Status checks ---

func (m *MoveOutNotice) IsPendingMeter() bool      { return m.Status == MoveOutStatusPendingMeter }
func (m *MoveOutNotice) IsPendingSettlement() bool { return m.Status == MoveOutStatusPendingSettlement }
func (m *MoveOutNotice) IsPendingPayment() bool    { return m.Status == MoveOutStatusPendingPayment }
func (m *MoveOutNotice) IsReadyToClose() bool      { return m.Status == MoveOutStatusReadyToClose }
func (m *MoveOutNotice) IsCompleted() bool         { return m.Status == MoveOutStatusCompleted }
func (m *MoveOutNotice) IsCancelled() bool         { return m.Status == MoveOutStatusCancelled }

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
//
// Phase-1 broadens this to also accept READY_TO_CLOSE so a single endpoint
// can power three flows:
//   - first record from PENDING_PAYMENT → READY_TO_CLOSE
//   - back-fill a previously skipped settlement (READY_TO_CLOSE + nil outcome)
//   - correct a previously recorded outcome (READY_TO_CLOSE + non-nil outcome)
//
// Phase-2 broadens further to accept COMPLETED + nil outcome — operators may
// re-enter a closed move-out (closed-with-unsettled) and back-fill the
// financial record without reopening the contract. COMPLETED + non-nil is
// rejected: a settled-and-closed notice is terminal for payment edits.
//
// ReopenForCorrection still exists for callers that want the "start over /
// clear fields" semantic; this guard powers the prefilled-edit path instead.
func (m *MoveOutNotice) CanRecordPayment() error {
	if m.IsPendingPayment() || m.IsReadyToClose() {
		return nil
	}
	if m.IsCompleted() && m.PaymentOutcome == nil {
		return nil
	}
	return ErrCannotRecordPayment
}

// CanSkipPayment returns nil if the operator may defer the financial record
// without setting an outcome. Only legal from PENDING_PAYMENT — once we hit
// READY_TO_CLOSE we treat skip as a no-op at the service layer to keep the
// endpoint idempotent without overwriting an existing outcome.
func (m *MoveOutNotice) CanSkipPayment() error {
	if !m.IsPendingPayment() {
		return ErrCannotSkipPayment
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

// CanCloseWithUnsettled returns nil if the notice can be closed via the
// explicit "close while unsettled" path. Allowed states (Phase-2):
//   - PENDING_PAYMENT + nil outcome — operator chooses to close in one shot
//     instead of going through SkipPayment first
//   - READY_TO_CLOSE + nil outcome — the canonical "skipped, now close" case
//     (Step 4 entry from a previously-skipped notice)
//   - COMPLETED + nil outcome — idempotent re-close (no state change)
//
// Settlement bill is still required so we never close without a financial
// record on file. PaymentOutcome must be nil — settled notices must use the
// regular CanClose / CloseMoveOut path so the two routes don't blur.
func (m *MoveOutNotice) CanCloseWithUnsettled() error {
	if m.PaymentOutcome != nil {
		return ErrCannotCloseWithUnsettled
	}
	switch m.Status {
	case MoveOutStatusPendingPayment, MoveOutStatusReadyToClose:
		if m.SettlementBillID == nil {
			return ErrMissingSettlementBill
		}
		return nil
	case MoveOutStatusCompleted:
		return nil
	default:
		return ErrCannotCloseWithUnsettled
	}
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

// RecordPayment records payment outcome and moves PENDING_PAYMENT →
// READY_TO_CLOSE. When called against an already-READY_TO_CLOSE notice
// (skipped or previously recorded), it back-fills/corrects the fields and
// keeps the status — see CanRecordPayment for the broadened guard.
//
// Phase-1 overwrite policy (LOCKED): unrestricted. Submit replaces all three
// payment fields (outcome/method/note); no merge. Direction-flips
// (PAY_MORE ↔ REFUND) are intentionally permitted — admin correction is the
// priority. Phase-2 audit log will retroactively trace overwrites.
func (m *MoveOutNotice) RecordPayment(outcome PaymentOutcome, method *paymentmethod.PaymentMethod, note string) error {
	if err := m.CanRecordPayment(); err != nil {
		return err
	}
	m.PaymentOutcome = &outcome
	m.PaymentMethod = method
	m.PaymentNote = note
	if m.IsPendingPayment() {
		m.Status = MoveOutStatusReadyToClose
	}
	// READY_TO_CLOSE + nil → fields set, status stays (back-fill).
	// READY_TO_CLOSE + outcome → fields replaced, status stays (correction).
	// COMPLETED + nil → fields set, status stays (post-close back-fill).
	return nil
}

// SkipPayment defers the financial record and moves PENDING_PAYMENT →
// READY_TO_CLOSE without setting an outcome. PaymentOutcome stays nil — this
// is how we mark the "deferred / UNSETTLED" state.
//
// At the domain layer this is strict (PENDING_PAYMENT only). The service
// layer adds idempotency by short-circuiting on already-READY_TO_CLOSE.
func (m *MoveOutNotice) SkipPayment() error {
	if err := m.CanSkipPayment(); err != nil {
		return err
	}
	m.Status = MoveOutStatusReadyToClose
	return nil
}

// IsUnsettled / IsSettled — single source of truth for the implicit
// "skipped" / "settled" derivation. Every caller in BE (and FE-mirrored)
// MUST go through these — never open-code `PaymentOutcome == nil`.
//
// Phase-2: extended to also cover COMPLETED + nil outcome so that closed-
// with-unsettled notices stay surfaced in the payment backlog. Every caller
// that delegates to these predicates picks up the new state automatically.
func (m *MoveOutNotice) IsUnsettled() bool {
	if m.PaymentOutcome != nil {
		return false
	}
	return m.IsReadyToClose() || m.IsCompleted()
}
func (m *MoveOutNotice) IsSettled() bool {
	if m.PaymentOutcome == nil {
		return false
	}
	return m.IsReadyToClose() || m.IsCompleted()
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

// CloseWithUnsettled transitions to COMPLETED without touching PaymentOutcome.
// Idempotent: a re-call against an already-COMPLETED + nil-outcome notice is a
// no-op (returns nil without mutating state). Callers (service layer) decide
// whether to skip side effects on the idempotent path by checking IsCompleted
// before invoking this method.
func (m *MoveOutNotice) CloseWithUnsettled(now time.Time) error {
	if err := m.CanCloseWithUnsettled(); err != nil {
		return err
	}
	if m.IsCompleted() {
		return nil
	}
	m.Status = MoveOutStatusCompleted
	m.ClosedAt = &now
	return nil
}

// Cancel transitions to CANCELLED and stamps the cancellation time.
// Mirrors the Close(now) pattern so the service layer can delegate the
// full transition (status + timestamp) instead of stamping fields after
// the fact.
func (m *MoveOutNotice) Cancel(now time.Time) error {
	if err := m.CanCancel(); err != nil {
		return err
	}
	m.Status = MoveOutStatusCancelled
	m.CancelledAt = &now
	return nil
}

// --- Settlement correction primitives (Phase 2.1E-A) ---
//
// Pure-domain pieces of the settlement correction workflow rewind. Do NOT
// trigger billing changes — the orchestrator (Phase 2.1E-B+) composes these
// with billingCmd.CorrectSettlement inside one transaction.
//
// Design lock: project_settlement_correction_design_lock.md.

// CanDowngradeToPendingSettlement reports whether the notice can rewind to
// PENDING_SETTLEMENT — only legal from a status that hosts a FINALIZED
// settlement bill (i.e. PENDING_PAYMENT or READY_TO_CLOSE). COMPLETED is
// rejected: closed notices revert contract.ENDED + room.VACANT, which is
// out of scope for v1 (Phase 2.1F backlog).
//
// Mirrors the UpdateExitMeter PENDING_PAYMENT → PENDING_SETTLEMENT
// precedent (service.go) — same status downgrade, made explicit + audited
// for the user-triggered correction flow.
func (m *MoveOutNotice) CanDowngradeToPendingSettlement() error {
	if m.IsPendingPayment() || m.IsReadyToClose() {
		return nil
	}
	return ErrCannotDowngradeToPendingSettlement
}

// DowngradeToPendingSettlement performs the status rewind. Caller is
// responsible for the billing-side void+regenerate + clearing payment
// metadata (ClearPaymentOutcome) in the same transaction.
func (m *MoveOutNotice) DowngradeToPendingSettlement() error {
	if err := m.CanDowngradeToPendingSettlement(); err != nil {
		return err
	}
	m.Status = MoveOutStatusPendingSettlement
	return nil
}

// ClearPaymentOutcome wipes PaymentOutcome, PaymentMethod, PaymentNote so
// settlement correction forces the admin to re-record against the new
// numbers (audit honesty — see design lock decision #6). No guard: the
// orchestrator gates appropriateness; the domain just performs.
func (m *MoveOutNotice) ClearPaymentOutcome() {
	m.PaymentOutcome = nil
	m.PaymentMethod = nil
	m.PaymentNote = ""
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
