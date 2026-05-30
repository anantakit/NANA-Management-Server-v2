package billing

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Types ---

type BillStatus string

const (
	BillStatusDraft     BillStatus = "DRAFT"
	BillStatusFinalized BillStatus = "FINALIZED"
	BillStatusPaid      BillStatus = "PAID"
	BillStatusVoid      BillStatus = "VOID"
)

type BillType string

const (
	BillTypeMonthly    BillType = "MONTHLY"
	BillTypeSettlement BillType = "SETTLEMENT"
)

type LineItemType string

const (
	LineItemRoomRent      LineItemType = "ROOM_RENT"
	LineItemElectricity   LineItemType = "ELECTRICITY"
	LineItemWater         LineItemType = "WATER"
	LineItemCleaningFee   LineItemType = "CLEANING_FEE"
	LineItemKeyService    LineItemType = "KEY_SERVICE"
	LineItemProrateRent   LineItemType = "PRORATE_RENT"
	LineItemPenalty       LineItemType = "PENALTY"
	LineItemPrepaidCredit   LineItemType = "PREPAID_CREDIT"
	LineItemOutstandingBill LineItemType = "OUTSTANDING_BILL"
	LineItemOther           LineItemType = "OTHER"
)

// validManualLineTypes are the line types allowed for user-added MANUAL items.
var validManualLineTypes = map[LineItemType]bool{
	LineItemCleaningFee: true,
	LineItemKeyService:  true,
	LineItemPenalty:     true,
	LineItemOther:       true,
}

// IsValidManualLineType returns true if the type can be used for MANUAL items.
func IsValidManualLineType(lt LineItemType) bool {
	return validManualLineTypes[lt]
}

type LineItemSource string

const (
	LineItemSourceAuto   LineItemSource = "AUTO"
	LineItemSourceManual LineItemSource = "MANUAL"
)

// --- Override map (jsonb on bills) ---

// OverrideMap stores override_key → amount (satang) for settlement draft editing.
// Keys are strings to allow future composite keys (e.g., "OUTSTANDING_BILL:2026-03").
// For current scope, key = LineType string for unique monetary AUTO rows.
type OverrideMap map[string]int64

// Scan implements sql.Scanner for jsonb column.
func (m *OverrideMap) Scan(value any) error {
	if value == nil {
		*m = OverrideMap{}
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("overrides: unsupported scan type %T", value)
	}
	if len(data) == 0 || string(data) == "{}" {
		*m = OverrideMap{}
		return nil
	}
	return json.Unmarshal(data, m)
}

// Value implements driver.Valuer for jsonb column.
func (m OverrideMap) Value() (driver.Value, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// --- Deposit application ---

type DepositApplication string

const (
	DepositAppFull   DepositApplication = "FULL"
	DepositAppNone   DepositApplication = "NONE"
	DepositAppCustom DepositApplication = "CUSTOM"
)

func (d DepositApplication) IsValid() bool {
	switch d {
	case DepositAppFull, DepositAppNone, DepositAppCustom:
		return true
	}
	return false
}

// --- Overrideable line types ---

// overrideableLineTypes defines which AUTO line types can be overridden by the user.
// Only monetary items with unique-per-bill guarantee are included.
var overrideableLineTypes = map[LineItemType]bool{
	LineItemRoomRent:    true,
	LineItemProrateRent: true,
	LineItemElectricity: true,
	LineItemWater:       true,
	LineItemCleaningFee: true,
	LineItemKeyService:  true,
}

// IsOverrideableLineType returns true if the line type can be overridden.
func IsOverrideableLineType(lt LineItemType) bool {
	return overrideableLineTypes[lt]
}

// --- Settlement options ---

type SettlementRentMode string

const (
	// RentModeProrated prorates rent by actual days used.
	// Deposit qualification uses actual move-out date.
	RentModeProrated SettlementRentMode = "PRORATED"

	// RentModeFullMonthKeepDeposit charges full-month rent for the move-out month.
	// Deposit qualification uses end-of-month, which may push effective stay
	// past MinMonths and make the deposit returnable.
	RentModeFullMonthKeepDeposit SettlementRentMode = "FULL_MONTH_KEEP_DEPOSIT"
)

// SettlementOptions controls settlement computation behavior.
// Phase 2: RentMode. Future: additional options.
type SettlementOptions struct {
	RentMode SettlementRentMode
	// SkipDuplicateGuard disables the "one non-VOID settlement per month" check.
	// Used by preview (read-only) which must work even when a draft exists.
	SkipDuplicateGuard bool
}

// DefaultSettlementOptions returns PRORATED (backward-compatible default).
func DefaultSettlementOptions() SettlementOptions {
	return SettlementOptions{RentMode: RentModeProrated}
}

// --- Domain errors ---

var (
	ErrNotDraft       = errors.New("บิลไม่ใช่สถานะร่าง")
	ErrNotFinalized   = errors.New("บิลไม่ใช่สถานะยืนยันแล้ว")
	ErrNoLineItems    = errors.New("บิลต้องมีรายการอย่างน้อย 1 รายการ")
	ErrAlreadyVoided  = errors.New("บิลถูกยกเลิกแล้ว")
	ErrAlreadyPaid    = errors.New("บิลถูกชำระแล้ว")
	ErrVoidReasonEmpty = errors.New("กรุณาระบุเหตุผลในการยกเลิก")
	ErrNotAbsorbed     = errors.New("บิลไม่ได้อยู่ในสถานะถูกรวมเข้าบิลสรุปยอด")
	ErrBatchAlreadyCommitted = errors.New("batch ถูก commit ไปแล้ว")
	// Correction (void+recreate) errors. ErrAlreadySuperseded fires when a bill
	// is already linked to a replacement — chain corrections must target the
	// latest bill in the chain.
	//
	// ErrSettlementCorrectionNotSupported is endpoint-level routing: settlement
	// correction lives in the move-out workflow (POST /move-out-notices/:id/
	// correct-settlement), not the billing /bills/:id/correct endpoint. The
	// billing CorrectBill dispatcher rejects SETTLEMENT bills with this error
	// so callers route to the right endpoint. Domain CanCorrect itself is
	// type-agnostic since Phase 2.1E-A — both bill types satisfy the
	// document-replacement invariants. See project_settlement_correction_design_lock.
	ErrAlreadySuperseded                = errors.New("บิลนี้ถูกแทนที่ด้วยใบใหม่แล้ว")
	ErrSettlementCorrectionNotSupported = errors.New("ยังไม่รองรับการแก้ไขบิลย้ายออกใน v1")
)

// --- Models ---

type Bill struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ContractID     uuid.UUID      `gorm:"type:uuid;not null" json:"contract_id"`
	BillingMonth   string         `gorm:"type:varchar(7);not null" json:"billing_month"`
	BillType       BillType       `gorm:"type:varchar(20);not null" json:"bill_type"`
	Status         BillStatus     `gorm:"type:varchar(20);not null;default:'DRAFT'" json:"status"`
	VoidReason     *string        `gorm:"type:varchar(100)" json:"void_reason"`
	DepositAmount    int64          `gorm:"not null;default:0" json:"deposit_amount"`
	DepositBalance   int64          `gorm:"not null;default:0" json:"deposit_balance"`
	DepositForfeited bool           `gorm:"not null;default:false" json:"deposit_forfeited"`
	TotalAmount    int64          `gorm:"not null;default:0" json:"total_amount"`
	BatchID        *uuid.UUID     `gorm:"type:uuid" json:"batch_id,omitempty"`
	RentPaid             bool               `gorm:"not null;default:false" json:"rent_paid"`
	SettlementRentMode   SettlementRentMode `gorm:"column:settlement_rent_mode;type:varchar(30);not null;default:'PRORATED'" json:"settlement_rent_mode"`
	Note                 string             `gorm:"type:text;not null;default:''" json:"note"`
	Overrides            OverrideMap        `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"overrides,omitempty"`
	DepositApp           DepositApplication `gorm:"column:deposit_application;type:varchar(10);not null;default:'FULL'" json:"deposit_application"`
	CustomDepositApplied int64              `gorm:"not null;default:0" json:"custom_deposit_applied"`
	// FinalizedAt records the moment the bill transitioned DRAFT → FINALIZED.
	// Stays set through PAID/VOID transitions (audit of "when did this become
	// a real receivable"). Nil while the bill is still DRAFT.
	FinalizedAt *time.Time `gorm:"type:timestamptz" json:"finalized_at,omitempty"`
	// SupersededByBillID forward-links this bill to its replacement when admin
	// corrects a FINALIZED bill (void+recreate). Old bill: status=VOID +
	// void_reason='CORRECTION' + SupersededByBillID=<new bill id>. New DRAFT
	// leaves this NULL. Reverse direction ("which bill replaces this one") is
	// served by indexed query on this column OR audit log
	// CREATE_FROM_CORRECTION event — no second column maintained.
	// See project_billing_correction_arch_lock.md.
	SupersededByBillID *uuid.UUID `gorm:"type:uuid;column:superseded_by_bill_id" json:"superseded_by_bill_id,omitempty"`

	CreatedAt time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	LineItems []BillLineItem `gorm:"foreignKey:BillID" json:"line_items,omitempty"`
}

func (Bill) TableName() string { return "bills" }

func (b *Bill) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

type BillLineItem struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BillID      uuid.UUID      `gorm:"type:uuid;not null" json:"bill_id"`
	LineType    LineItemType   `gorm:"type:varchar(30);not null" json:"line_type"`
	Source      LineItemSource `gorm:"type:varchar(10);not null;default:'AUTO'" json:"source"`
	Description string         `gorm:"type:text;not null;default:''" json:"description"`
	Amount      int64          `gorm:"not null" json:"amount"`
	Quantity    int            `gorm:"not null;default:0" json:"quantity"`
	UnitPrice   int64          `gorm:"not null;default:0" json:"unit_price"`
	SortOrder   int            `gorm:"not null;default:0" json:"sort_order"`
	CreatedAt   time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"not null;default:now()" json:"updated_at"`
}

func (li *BillLineItem) IsAuto() bool   { return li.Source == LineItemSourceAuto }
func (li *BillLineItem) IsManual() bool { return li.Source == LineItemSourceManual }

// OverrideKey returns the stable key used to match overrides.
//
// IMPORTANT ASSUMPTION:
// For settlement bills, overrideable AUTO line items (rent, water, electricity, config fees)
// are guaranteed to be unique per LineType within a single bill.
//
// If future requirements introduce multiple items per LineType (e.g. multiple outstanding bills),
// OverrideKey MUST be upgraded to a composite key (e.g. type:identifier).
func (li *BillLineItem) OverrideKey() string { return string(li.LineType) }

func (BillLineItem) TableName() string { return "bill_line_items" }

func (li *BillLineItem) BeforeCreate(tx *gorm.DB) error {
	if li.ID == uuid.Nil {
		li.ID = uuid.New()
	}
	return nil
}

// --- Status checks ---

func (b *Bill) IsDraft() bool     { return b.Status == BillStatusDraft }
func (b *Bill) IsFinalized() bool { return b.Status == BillStatusFinalized }
func (b *Bill) IsPaid() bool      { return b.Status == BillStatusPaid }
func (b *Bill) IsVoid() bool      { return b.Status == BillStatusVoid }

func (b *Bill) IsMonthly() bool    { return b.BillType == BillTypeMonthly }
func (b *Bill) IsSettlement() bool { return b.BillType == BillTypeSettlement }

// OverdueDays returns the number of full calendar days the bill is past
// its day-5 due date. Returns 0 when the bill is not eligible for overdue
// counting:
//   - not FINALIZED (DRAFT, PAID, VOID all return 0)
//   - not MONTHLY (settlement bills follow the move-out workflow, not the
//     monthly cadence — see backlog_late_payment_penalty.md hard rule)
//   - billing_month is malformed
//   - today is on or before the due day (calendar comparison)
//
// Calendar-day arithmetic: compares date components only (year/month/day),
// independent of time-of-day. So "เลยกำหนด 1 วัน" surfaces on day-6
// regardless of whether admin opens the drawer at 8am or 11pm.
func (b *Bill) OverdueDays(today time.Time) int {
	if !b.IsFinalized() || !b.IsMonthly() {
		return 0
	}
	due, ok := BillDueDate(b.BillingMonth)
	if !ok {
		return 0
	}
	dueDay := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
	todayDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	days := int(todayDay.Sub(dueDay).Hours() / 24)
	if days <= 0 {
		return 0
	}
	return days
}

// IsOverdue is a convenience boolean derived from OverdueDays. Returns
// true iff the bill is past its due day by at least 1 calendar day —
// inherits the FINALIZED + MONTHLY gate from OverdueDays.
func (b *Bill) IsOverdue(today time.Time) bool {
	return b.OverdueDays(today) > 0
}

// --- State transitions ---

func (b *Bill) CanFinalize() error {
	if !b.IsDraft() {
		return ErrNotDraft
	}
	if len(b.LineItems) == 0 {
		return ErrNoLineItems
	}
	return nil
}

func (b *Bill) CanVoid() error {
	if b.IsVoid() {
		return ErrAlreadyVoided
	}
	if b.IsPaid() {
		return ErrAlreadyPaid
	}
	if !b.IsDraft() && !b.IsFinalized() {
		return ErrNotFinalized
	}
	return nil
}

func (b *Bill) CanMarkPaid() error {
	if !b.IsFinalized() {
		return ErrNotFinalized
	}
	return nil
}

func (b *Bill) Finalize() error {
	if err := b.CanFinalize(); err != nil {
		return err
	}
	b.Status = BillStatusFinalized
	// Stamp the moment the bill became a real receivable. Subsequent
	// MarkPaid/Void transitions intentionally leave this field alone — it
	// is the audit anchor for the FINALIZED→PAID timeline.
	now := time.Now()
	b.FinalizedAt = &now
	return nil
}

func (b *Bill) Void(reason string) error {
	if reason == "" {
		return ErrVoidReasonEmpty
	}
	// Idempotent: already voided → no-op
	if b.IsVoid() {
		return nil
	}
	if err := b.CanVoid(); err != nil {
		return err
	}
	b.Status = BillStatusVoid
	b.VoidReason = &reason
	return nil
}

func (b *Bill) MarkPaid() error {
	if err := b.CanMarkPaid(); err != nil {
		return err
	}
	b.Status = BillStatusPaid
	return nil
}

// --- AR-lite amount projections (status-derived) ---
//
// IMPORTANT — atomicity assumption:
// These projections assume "1 bill = 1 payment, atomic" (PAID = 100% paid;
// no partial payments). The system has NO payments table today (the legacy
// table was dropped in migration 00014 and never recreated), so this is the
// only honest reading available.
//
// When partial-payment infrastructure ships, both methods MUST be replaced
// with payments-table aggregations (e.g. SUM(payments.amount) per bill).
// Any caller that calls PaidAmount/OutstandingAmount today will read the
// updated value transparently — no API shape change required.

// PaidAmount returns the satang amount considered collected for this bill.
// Under atomic semantics: PAID = total, otherwise 0.
func (b *Bill) PaidAmount() int64 {
	if b.IsPaid() {
		return b.TotalAmount
	}
	return 0
}

// OutstandingAmount returns the satang amount still owed on this bill.
// Under atomic semantics: DRAFT or FINALIZED = total (entire bill is
// outstanding); PAID or VOID = 0 (collected, or excluded from AR).
func (b *Bill) OutstandingAmount() int64 {
	if b.IsDraft() || b.IsFinalized() {
		return b.TotalAmount
	}
	return 0
}

// --- Absorbed-by-settlement lifecycle ---

const voidReasonAbsorbed = "ABSORBED_BY_SETTLEMENT"

// MarkAbsorbedBySettlement voids this bill as absorbed into a settlement.
// Technically callable on DRAFT or FINALIZED (CanVoid allows both),
// but service layer restricts to FINALIZED only — DRAFT bills are unconfirmed.
func (b *Bill) MarkAbsorbedBySettlement() error {
	return b.Void(voidReasonAbsorbed)
}

// RestoreFromAbsorbed reverses absorption, setting the bill back to FINALIZED.
// Only callable on VOID bills with the matching void reason.
func (b *Bill) RestoreFromAbsorbed() error {
	if !b.IsAbsorbedBySettlement() {
		return ErrNotAbsorbed
	}
	b.Status = BillStatusFinalized
	b.VoidReason = nil
	return nil
}

// IsAbsorbedBySettlement returns true if this bill was voided specifically
// because it was absorbed into a settlement (not cancelled or regenerated).
func (b *Bill) IsAbsorbedBySettlement() bool {
	return b.IsVoid() && b.VoidReason != nil && *b.VoidReason == voidReasonAbsorbed
}

// --- Correction (void+recreate) lifecycle ---

const voidReasonCorrection = "CORRECTION"

// CanCorrect reports whether this bill is eligible for the void+recreate
// correction flow. Stricter than CanVoid:
//   - Must be FINALIZED (DRAFT → use the edit flow; PAID → ErrAlreadyPaid;
//     VOID → ErrAlreadyVoided)
//   - Must not already be superseded — chain corrections must target the
//     latest bill in the chain, not an already-replaced one
//
// Type-agnostic since Phase 2.1E-A: both MONTHLY and SETTLEMENT satisfy the
// document-replacement invariants the domain models here. Routing to the
// right orchestrator is a service-layer concern, not a domain one — the
// billing CorrectBill dispatcher rejects SETTLEMENT at the endpoint level
// (MONTHLY-only by design); settlement correction is owned by the move-out
// workflow (Phase 2.1E-B+). See project_settlement_correction_design_lock.
func (b *Bill) CanCorrect() error {
	if b.IsVoid() {
		return ErrAlreadyVoided
	}
	if b.IsPaid() {
		return ErrAlreadyPaid
	}
	if !b.IsFinalized() {
		return ErrNotFinalized
	}
	if b.SupersededByBillID != nil {
		return ErrAlreadySuperseded
	}
	return nil
}

// IsSuperseded reports whether this bill has been replaced by a correction.
// Used by the FE to render "ถูกแทนที่ด้วยใบใหม่" cross-link on the old bill.
func (b *Bill) IsSuperseded() bool {
	return b.SupersededByBillID != nil
}

// IsSupersededByCorrection narrows IsSuperseded to the specific void reason
// emitted by the correction flow (vs. the future possibility of a different
// supersede path). True iff voided with CORRECTION reason AND the forward
// link is set — both invariants hold together post-MarkSupersededByCorrection.
func (b *Bill) IsSupersededByCorrection() bool {
	return b.IsSuperseded() && b.IsVoid() &&
		b.VoidReason != nil && *b.VoidReason == voidReasonCorrection
}

// MarkSupersededByCorrection voids this bill as superseded by a new DRAFT
// created via the correction flow. Sets the forward link to the new bill so
// the FE can render "ถูกแทนที่ด้วยใบใหม่" without an extra query.
//
// Re-checks CanCorrect defensively (caller must also gate, but the domain
// stays correct under direct invocation). Also guards against self-reference
// since the FK CHECK constraint would only catch it on persist.
func (b *Bill) MarkSupersededByCorrection(newBillID uuid.UUID) error {
	if newBillID == uuid.Nil {
		return fmt.Errorf("supersede: new bill id required")
	}
	if newBillID == b.ID {
		return fmt.Errorf("supersede: bill cannot supersede itself")
	}
	if err := b.CanCorrect(); err != nil {
		return err
	}
	if err := b.Void(voidReasonCorrection); err != nil {
		return err
	}
	b.SupersededByBillID = &newBillID
	return nil
}

// --- Calculation ---

// CalculateTotal computes TotalAmount from line items, applying overrides for settlement bills.
// For settlement bills, also computes DepositBalance via DepositBreakdown().
func (b *Bill) CalculateTotal() {
	var total int64
	for _, item := range b.LineItems {
		// Overrides are a bill-level curation layer that applies to any
		// editable bill (MONTHLY + SETTLEMENT), not just settlement.
		// Skipping this for MONTHLY made `bills.total_amount` drift from
		// the sum of effective line item amounts surfaced by the DTO
		// mapper (which already substitutes overrides at read time).
		if item.IsAuto() {
			if override, ok := b.Overrides[item.OverrideKey()]; ok {
				total += override
				continue
			}
		}
		total += item.Amount
	}
	b.TotalAmount = total

	if b.IsSettlement() {
		b.applyDepositCalculation()
	}
}

// applyDepositCalculation sets DepositBalance directly.
// DepositBalance: positive = refund to tenant, negative = tenant owes.
// This handles negative TotalAmount (credits > charges) correctly.
func (b *Bill) applyDepositCalculation() {
	switch b.DepositApp {
	case DepositAppNone:
		b.DepositBalance = -b.TotalAmount
	case DepositAppCustom:
		applied := b.CustomDepositApplied
		if applied > b.DepositAmount {
			applied = b.DepositAmount
		}
		if applied < 0 {
			applied = 0
		}
		b.DepositBalance = applied - b.TotalAmount
	default: // FULL (including empty string)
		if b.DepositForfeited {
			b.DepositBalance = -b.TotalAmount
		} else {
			b.DepositBalance = b.DepositAmount - b.TotalAmount
		}
	}
}

// --- Deposit breakdown ---

// DepositBreakdownResult holds the fully decomposed deposit state for a settlement.
// Every field has a single, unambiguous meaning — no dual-purpose fields.
type DepositBreakdownResult struct {
	OriginalAmount int64 // contract deposit held by landlord
	AppliedAmount  int64 // deposit offset against charges
	RefundAmount   int64 // deposit returned to tenant
	WithheldAmount int64 // deposit kept by admin (NONE mode or forfeited)
	AmountDue      int64 // remaining amount tenant must pay
}

// DepositBreakdown computes the full deposit state based on DepositApp mode.
// Must be called AFTER TotalAmount is set (i.e. after CalculateTotal's sum phase).
// Charges are clamped to 0 when TotalAmount is negative (credits > charges).
func (b *Bill) DepositBreakdown() DepositBreakdownResult {
	if !b.IsSettlement() {
		return DepositBreakdownResult{AmountDue: b.TotalAmount}
	}

	// When credits exceed charges, there's nothing to offset.
	charges := b.TotalAmount
	if charges < 0 {
		charges = 0
	}

	switch b.DepositApp {
	case DepositAppNone:
		// Admin withholds entire deposit; tenant pays full charges
		return DepositBreakdownResult{
			OriginalAmount: b.DepositAmount,
			WithheldAmount: b.DepositAmount,
			AmountDue:      charges,
		}

	case DepositAppCustom:
		applied := b.CustomDepositApplied
		if applied > b.DepositAmount {
			applied = b.DepositAmount
		}
		if applied > charges {
			applied = charges
		}
		if applied < 0 {
			applied = 0
		}
		return DepositBreakdownResult{
			OriginalAmount: b.DepositAmount,
			AppliedAmount:  applied,
			RefundAmount:   b.DepositAmount - applied,
			AmountDue:      charges - applied,
		}

	default: // FULL (including empty string)
		if b.DepositForfeited {
			// Forfeited = withheld by contract terms; tenant pays full charges
			return DepositBreakdownResult{
				OriginalAmount: b.DepositAmount,
				WithheldAmount: b.DepositAmount,
				AmountDue:      charges,
			}
		}
		applied := b.DepositAmount
		if applied > charges {
			applied = charges
		}
		return DepositBreakdownResult{
			OriginalAmount: b.DepositAmount,
			AppliedAmount:  applied,
			RefundAmount:   b.DepositAmount - applied,
			AmountDue:      charges - applied,
		}
	}
}

// --- Override validation/pruning ---

// ValidateOverrides rejects non-overrideable keys and negative amounts.
func (b *Bill) ValidateOverrides() error {
	for key, amount := range b.Overrides {
		lt := LineItemType(key)
		if !IsOverrideableLineType(lt) {
			return fmt.Errorf("ประเภท %q ไม่สามารถ override ได้", key)
		}
		if amount < 0 {
			return fmt.Errorf("จำนวนเงิน override ต้องไม่ติดลบ (%q)", key)
		}
	}
	return nil
}

// PruneStaleOverrides removes overrides for keys not present in current AUTO items.
// Called after regeneration to drop overrides for line types that no longer exist.
func (b *Bill) PruneStaleOverrides() {
	if len(b.Overrides) == 0 {
		return
	}
	autoKeys := make(map[string]bool)
	for _, li := range b.LineItems {
		if li.IsAuto() {
			autoKeys[li.OverrideKey()] = true
		}
	}
	for key := range b.Overrides {
		if !autoKeys[key] {
			delete(b.Overrides, key)
		}
	}
}

// ChargesTotal returns sum of positive line items (charges).
func (b *Bill) ChargesTotal() int64 {
	var total int64
	for _, item := range b.LineItems {
		if item.Amount > 0 {
			total += item.Amount
		}
	}
	return total
}

// CreditsTotal returns sum of absolute values of negative line items (credits).
func (b *Bill) CreditsTotal() int64 {
	var total int64
	for _, item := range b.LineItems {
		if item.Amount < 0 {
			total += -item.Amount
		}
	}
	return total
}

// --- Line item factories ---

func NewRoomRentLine(monthlyRent int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemRoomRent,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      monthlyRent,
		SortOrder:   order,
	}
}

func NewElectricityLine(units int, ratePerUnit int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemElectricity,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      int64(units) * ratePerUnit,
		Quantity:    units,
		UnitPrice:   ratePerUnit,
		SortOrder:   order,
	}
}

func NewWaterLine(units int, ratePerUnit int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemWater,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      int64(units) * ratePerUnit,
		Quantity:    units,
		UnitPrice:   ratePerUnit,
		SortOrder:   order,
	}
}

// NewProrateRentLine builds a prorate-rent line from a flat per-day
// rate. `Amount = dailyRate × daysUsed` is exact integer math, so the
// per-day rate displayed in `Description` ("N วัน × ฿X/วัน") always
// multiplies back to the stored amount — no ±1 satang display drift.
//
// The flat-rate model decouples partial-month pricing from monthly_rent;
// the rate itself is sourced from the apartment's PRORATE_DAILY_RATE
// billing_config row by the caller.
func NewProrateRentLine(daysUsed int, dailyRate int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemProrateRent,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      dailyRate * int64(daysUsed),
		Quantity:    daysUsed,
		UnitPrice:   dailyRate,
		SortOrder:   order,
	}
}

func NewFeeLine(lineType LineItemType, amount int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    lineType,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      amount,
		SortOrder:   order,
	}
}

func NewPrepaidCreditLine(amount int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemPrepaidCredit,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      -amount, // negative = credit
		SortOrder:   order,
	}
}

// NewOutstandingBillLine creates a line item representing an absorbed unpaid bill.
func NewOutstandingBillLine(amount int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemOutstandingBill,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      amount,
		SortOrder:   order,
	}
}

// NewManualLine creates a user-added line item (editable, preserved on regenerate).
func NewManualLine(lineType LineItemType, amount int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    lineType,
		Source:      LineItemSourceManual,
		Description: description,
		Amount:      amount,
		SortOrder:   order,
	}
}

// ManualItems returns only MANUAL line items from the bill.
func (b *Bill) ManualItems() []BillLineItem {
	var items []BillLineItem
	for _, li := range b.LineItems {
		if li.IsManual() {
			items = append(items, li)
		}
	}
	return items
}

// --- Bill generation batch ---

type BatchStatus string

const (
	BatchStatusCompleted BatchStatus = "COMPLETED"
	BatchStatusPartial   BatchStatus = "PARTIAL"
	BatchStatusFailed    BatchStatus = "FAILED"
)

func (s BatchStatus) IsValid() bool {
	switch s {
	case BatchStatusCompleted, BatchStatusPartial, BatchStatusFailed:
		return true
	}
	return false
}

type ResultType string

const (
	ResultCreated       ResultType = "CREATED"
	ResultAlreadyExists ResultType = "ALREADY_EXISTS"
	ResultSkipped       ResultType = "SKIPPED"
	ResultFailed        ResultType = "FAILED"
)

// Reason codes for batch items (machine-readable, stable).
const (
	ReasonMoveOutPending      = "MOVE_OUT_PENDING"
	ReasonNotBillable         = "NOT_BILLABLE"
	ReasonMissingMeterReading = "MISSING_METER_READING"
	ReasonAlreadyExists       = "ALREADY_EXISTS"
	ReasonValidationError     = "VALIDATION_ERROR"
	ReasonSystemError         = "SYSTEM_ERROR"
	ReasonCodeCommitError     = "COMMIT_ERROR"
)

// --- Commit status (batch-level) ---

type CommitStatus string

const (
	CommitStatusCommitted          CommitStatus = "COMMITTED"
	CommitStatusPartiallyCommitted CommitStatus = "PARTIALLY_COMMITTED"
	CommitStatusFailed             CommitStatus = "FAILED"
)

// --- Computed snapshot (per-item, serialized to jsonb) ---

const ComputedSnapshotVersion = 1

var (
	ErrSnapshotUnsupportedVersion = errors.New("snapshot ไม่รองรับเวอร์ชันนี้")
	ErrSnapshotNoLineItems        = errors.New("snapshot ต้องมีรายการอย่างน้อย 1 รายการ")
	ErrSnapshotNegativeTotal      = errors.New("snapshot มียอดรวมติดลบ")
)

type ComputedLineItem struct {
	Type        LineItemType   `json:"type"`
	Description string         `json:"description"`
	Amount      int64          `json:"amount"`
	Quantity    int            `json:"quantity,omitempty"`
	UnitPrice   int64          `json:"unit_price,omitempty"`
	SortOrder   int            `json:"sort_order,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type ComputedSnapshot struct {
	Version     int                `json:"version"`
	LineItems   []ComputedLineItem `json:"line_items"`
	TotalAmount int64              `json:"total_amount"`
	ComputedAt  time.Time          `json:"computed_at"`
	SourceHash  string             `json:"source_hash,omitempty"`
}

// Scan implements sql.Scanner for jsonb column.
func (s *ComputedSnapshot) Scan(value any) error {
	if value == nil {
		*s = ComputedSnapshot{}
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("computed_snapshot: unsupported scan type %T", value)
	}
	if len(data) == 0 {
		*s = ComputedSnapshot{}
		return nil
	}
	return json.Unmarshal(data, s)
}

// Value implements driver.Valuer for jsonb column.
func (s ComputedSnapshot) Value() (driver.Value, error) {
	if s.Version == 0 && len(s.LineItems) == 0 && s.TotalAmount == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(s)
}

// Validate checks snapshot invariants before commit.
func (s *ComputedSnapshot) Validate() error {
	if s.Version != ComputedSnapshotVersion {
		return ErrSnapshotUnsupportedVersion
	}
	if len(s.LineItems) == 0 {
		return ErrSnapshotNoLineItems
	}
	if s.TotalAmount < 0 {
		return ErrSnapshotNegativeTotal
	}
	return nil
}

// ToLineItems converts snapshot items into BillLineItem rows for a given bill.
func (s *ComputedSnapshot) ToLineItems(billID uuid.UUID) []BillLineItem {
	items := make([]BillLineItem, 0, len(s.LineItems))
	for _, li := range s.LineItems {
		items = append(items, BillLineItem{
			BillID:      billID,
			LineType:    li.Type,
			Source:      LineItemSourceAuto,
			Description: li.Description,
			Amount:      li.Amount,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			SortOrder:   li.SortOrder,
		})
	}
	return items
}

type BillGenerationBatch struct {
	ID                 uuid.UUID   `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID        uuid.UUID   `gorm:"type:uuid;not null" json:"apartment_id"`
	BillingMonth       string      `gorm:"type:varchar(7);not null" json:"billing_month"`
	Status             BatchStatus `gorm:"type:varchar(20);not null" json:"status"`
	TotalContracts     int         `gorm:"not null;default:0" json:"total_contracts"`
	CreatedCount       int         `gorm:"not null;default:0;column:created_count" json:"created_count"`
	AlreadyExistsCount int         `gorm:"not null;default:0;column:already_exists_count" json:"already_exists_count"`
	SkippedCount       int         `gorm:"not null;default:0;column:skipped_count" json:"skipped_count"`
	FailedCount        int         `gorm:"not null;default:0;column:failed_count" json:"failed_count"`
	CreatedBy          *uuid.UUID  `gorm:"type:uuid" json:"created_by,omitempty"`
	CreatedAt          time.Time   `gorm:"not null;default:now()" json:"created_at"`
	CommitStatus       *CommitStatus `gorm:"type:varchar(32)" json:"commit_status,omitempty"`
	CommittedAt        *time.Time    `gorm:"type:timestamptz" json:"committed_at,omitempty"`
}

// CanCommit returns nil if the batch is eligible for a commit attempt.
// Already-committed batches are rejected; NULL/PARTIALLY_COMMITTED/FAILED are retryable.
func (b *BillGenerationBatch) CanCommit() error {
	if b.CommitStatus != nil && *b.CommitStatus == CommitStatusCommitted {
		return ErrBatchAlreadyCommitted
	}
	return nil
}

// MarkCommitResult sets CommitStatus + CommittedAt based on per-item tallies.
// pendingCount > 0 keeps CommitStatus as NULL (defensive — caller shouldn't do this).
func (b *BillGenerationBatch) MarkCommitResult(successCount, failCount, pendingCount int) {
	if pendingCount > 0 {
		return
	}
	now := time.Now()
	switch {
	case successCount > 0 && failCount == 0:
		s := CommitStatusCommitted
		b.CommitStatus = &s
		b.CommittedAt = &now
	case successCount > 0 && failCount > 0:
		s := CommitStatusPartiallyCommitted
		b.CommitStatus = &s
		b.CommittedAt = &now
	case successCount == 0 && failCount > 0:
		s := CommitStatusFailed
		b.CommitStatus = &s
		b.CommittedAt = nil
	}
}

func (BillGenerationBatch) TableName() string { return "bill_generation_batches" }

// ComputeStatus derives batch status from counts. Must be called before persist.
// FAILED  = no bills produced (created + already_exists == 0) AND any failed
// COMPLETED = no failed, no skipped (everything landed — includes the empty-apartment case)
// PARTIAL = anything else (at least some bills produced, but some failed/skipped)
func (b *BillGenerationBatch) ComputeStatus() {
	landed := b.CreatedCount + b.AlreadyExistsCount
	switch {
	case landed == 0 && b.FailedCount > 0:
		b.Status = BatchStatusFailed
	case b.FailedCount == 0 && b.SkippedCount == 0:
		b.Status = BatchStatusCompleted
	default:
		b.Status = BatchStatusPartial
	}
}

type BillGenerationBatchItem struct {
	ID         uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BatchID    uuid.UUID  `gorm:"type:uuid;not null" json:"batch_id"`
	ContractID uuid.UUID  `gorm:"type:uuid;not null" json:"contract_id"`
	RoomID     uuid.UUID  `gorm:"type:uuid;not null" json:"room_id"`
	RoomNumber string     `gorm:"type:varchar(20);not null" json:"room_number"`
	RoomFloor  int        `gorm:"not null;default:0" json:"room_floor"`
	ResultType ResultType `gorm:"type:varchar(20);not null" json:"result_type"`
	ReasonCode string     `gorm:"type:varchar(40);not null;default:''" json:"reason_code"`
	ReasonText string     `gorm:"type:text;not null;default:''" json:"reason_text"`
	BillID           *uuid.UUID       `gorm:"type:uuid" json:"bill_id,omitempty"`
	ComputedSnapshot ComputedSnapshot `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"computed_snapshot"`
	CreatedAt        time.Time        `gorm:"not null;default:now()" json:"created_at"`
}

func (BillGenerationBatchItem) TableName() string { return "bill_generation_batch_items" }

// CommitBatchResult holds the outcome of a commit attempt for API response.
type CommitBatchResult struct {
	Batch        *BillGenerationBatch
	SuccessCount int
	FailCount    int
	PendingCount int
}

// --- Projections ---

// BatchItemWithTenant is a projection for batch item API responses (JOIN tenant + bill).
type BatchItemWithTenant struct {
	BillGenerationBatchItem
	TenantName string `json:"tenant_name"`

	// BillStatus is the current status of the linked bill, populated via
	// LEFT JOIN bills in FindBatchItemsByBatchID. Nil for items without a
	// committed bill (BillID == nil pre-commit, or failed commit). Lets
	// BillBatchReview compute draftCount / editedCount client-side without
	// an extra round-trip — primary consumer is the post-commit
	// "ออกบิลทั้งหมด" CTA which only renders when at least one item still
	// has BillStatus = DRAFT.
	BillStatus *BillStatus `gorm:"column:bill_status" json:"-"`

	// IsEdited mirrors BillWithRelations.IsEdited — true iff the linked bill
	// has at least one edit-class audit event. Populated by GetBatchItems
	// via batched EditedBillIDs lookup, false for items without a committed
	// bill. gorm:"-" because it's compute-on-demand (separate audit query),
	// not column-derived like BillStatus.
	IsEdited bool `gorm:"-" json:"-"`
}

// BillWithRelations is a projection for list/detail API responses.
type BillWithRelations struct {
	Bill
	TenantName    string    `json:"tenant_name"`
	RoomNumber    string    `json:"room_number"`
	ApartmentName string    `json:"apartment_name"`
	ApartmentID   uuid.UUID `json:"apartment_id"`

	// OverdueDays is a compute-on-demand factual context surfaced only
	// by the detail endpoint (GET /bills/:id) for the BillDrawer hint.
	// Calendar-day count past day-5 due (MONTHLY bills only); 0 when
	// not overdue. Never persisted, never affects the bill total.
	// See backlog_late_payment_penalty.md.
	OverdueDays int `gorm:"-" json:"-"`

	// LatePenaltyReferenceAmount is the apartment's configured LATE_PENALTY
	// rate (satang), surfaced as a muted secondary "policy reference"
	// hint beneath the primary overdue-days line. 0 when no active config
	// or bill not overdue. Reference only — never a recommendation, never
	// persisted, never mutates the bill.
	LatePenaltyReferenceAmount int64 `gorm:"-" json:"-"`

	// IsEdited reports whether the admin has curated this bill — true iff
	// the audit log carries at least one edit event (UPDATE_OVERRIDE /
	// ADD_MANUAL_ITEM / REMOVE_MANUAL_ITEM / UPDATE_NOTE). Lifecycle events
	// (CREATE_DRAFT / FINALIZE / VOID) do NOT count. Single source of truth
	// lives in BillAuditAction.IsEditEvent — FE never sees audit semantics,
	// only this derived boolean. Populated by service.GetByID (single-bill)
	// and service.List (batched per page, no N+1).
	IsEdited bool `gorm:"-" json:"-"`

	// CorrectedFromBillID is the reverse link to the VOID(CORRECTION) bill
	// that this bill replaced — populated only by service.GetByID when the
	// reverse-link lookup hits a match. Nil for normal bills (the common
	// case). FE renders the "บิลนี้สร้างจากการแก้ไขบิลเดิม" hint off
	// this field's presence. Not surfaced on list responses (would be N+1
	// without a batched join, and the list collapses correction chains
	// client-side anyway — only the drawer needs lineage context).
	CorrectedFromBillID *uuid.UUID `gorm:"-" json:"-"`

	// CorrectionReason is the admin-typed reason captured at correction time
	// (min 5 chars per CorrectBillRequest / CorrectSettlementRequest). Sourced
	// from the latest SUPERSEDE audit event for this bill — the bills table
	// does NOT denormalize this field (audit log stays single source of truth).
	// Populated only by service.GetByID when the bill is VOID(CORRECTION);
	// empty string for every other state. FE renders verbatim beneath the
	// humanized void_reason on the BillDrawer so "why was this voided"
	// readable without DB access.
	CorrectionReason string `gorm:"-" json:"-"`
}

// BillSummaryRaw holds aggregate counts from the summary query (satang).
//
// Amount semantics:
//   - TotalAmount      = sum of total_amount for non-VOID bills (existing,
//     unchanged) — represents "billed value in scope".
//   - PendingAmount    = sum for status IN (DRAFT, FINALIZED). Under the
//     atomic 1-bill-1-payment model this is also "amount outstanding".
//   - PaidAmount       = sum for status = PAID. "Amount collected".
//   - VoidedAmount     = sum for status = VOID. "Amount excluded from AR".
//
// When partial-payment infrastructure ships, PendingAmount/PaidAmount must be
// recomputed from a payments table instead of bill.status.
type BillSummaryRaw struct {
	TotalCount    int   `gorm:"column:total_count"`
	PendingCount  int   `gorm:"column:pending_count"`
	PaidCount     int   `gorm:"column:paid_count"`
	VoidedCount   int   `gorm:"column:voided_count"`
	TotalAmount   int64 `gorm:"column:total_amount"`   // satang, sum of non-VOID
	PendingAmount int64 `gorm:"column:pending_amount"` // satang, sum of DRAFT+FINALIZED
	PaidAmount    int64 `gorm:"column:paid_amount"`    // satang, sum of PAID
	VoidedAmount  int64 `gorm:"column:voided_amount"`  // satang, sum of VOID
}

// --- Bill audit log (event-sourced edit history) ---
//
// Separate aggregate from Bill: bills holds current state (overrides map,
// line items, totals), bill_audit_log holds the event timeline of how that
// state was reached. Two columns intentionally NOT collapsed because:
//   - current state is queried hot, event log is read cold
//   - event log is append-only forever; bill state mutates
//   - one bill → many audit rows naturally relational
//
// Audit recorder writes inside the same TX as the state mutation. On audit
// failure the parent TX rolls back — correctness > availability for billing
// forensics (see project_billing_editable_monthly_arch_lock.md).
//
// Payload shape is structured per action (not freeform blob): each action
// has a typed Go struct below that marshals into the jsonb column. Action
// itself is the discriminator — payload does NOT repeat the field name.

type BillAuditAction string

const (
	// Lifecycle events — do NOT count toward is_edited.
	AuditCreateDraft BillAuditAction = "CREATE_DRAFT"
	AuditFinalize    BillAuditAction = "FINALIZE"
	AuditVoid        BillAuditAction = "VOID"

	// Correction lifecycle events — emitted as a pair when admin corrects a
	// FINALIZED bill (void+recreate). SUPERSEDE goes on the OLD bill,
	// CREATE_FROM_CORRECTION on the NEW bill. Both are lifecycle events
	// (NOT counted toward is_edited).
	AuditSupersede            BillAuditAction = "SUPERSEDE"
	AuditCreateFromCorrection BillAuditAction = "CREATE_FROM_CORRECTION"

	// Edit events — count toward is_edited.
	AuditUpdateOverride   BillAuditAction = "UPDATE_OVERRIDE"
	AuditAddManualItem    BillAuditAction = "ADD_MANUAL_ITEM"
	AuditRemoveManualItem BillAuditAction = "REMOVE_MANUAL_ITEM"
	AuditUpdateNote       BillAuditAction = "UPDATE_NOTE"
)

// IsEditEvent reports whether this action contributes to the is_edited flag
// on the bill. Lifecycle events (CREATE_DRAFT, FINALIZE, VOID) do not.
func (a BillAuditAction) IsEditEvent() bool {
	switch a {
	case AuditUpdateOverride, AuditAddManualItem, AuditRemoveManualItem, AuditUpdateNote:
		return true
	}
	return false
}

// BillAuditLog is one append-only event in a bill's edit timeline.
//
// ActorID is nullable because some events are system-triggered (e.g. settlement
// regenerate from move-out service has no user actor) — DB enforces
// ON DELETE SET NULL so audit survives user removal.
type BillAuditLog struct {
	ID        uuid.UUID       `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BillID    uuid.UUID       `gorm:"type:uuid;not null" json:"bill_id"`
	Action    BillAuditAction `gorm:"type:varchar(30);not null" json:"action"`
	ActorID   *uuid.UUID      `gorm:"type:uuid" json:"actor_id,omitempty"`
	Payload   AuditPayload    `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"payload"`
	CreatedAt time.Time       `gorm:"not null;default:now()" json:"created_at"`
}

func (BillAuditLog) TableName() string { return "bill_audit_log" }

func (l *BillAuditLog) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// AuditPayload is a raw jsonb cell that wraps any of the typed payload structs
// below. Caller marshals the action-specific struct via MarshalAuditPayload;
// reader unmarshals via the typed accessors.
type AuditPayload json.RawMessage

func (p *AuditPayload) Scan(value any) error {
	if value == nil {
		*p = AuditPayload("{}")
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("audit payload: unsupported scan type %T", value)
	}
	if len(data) == 0 {
		*p = AuditPayload("{}")
		return nil
	}
	*p = AuditPayload(data)
	return nil
}

func (p AuditPayload) Value() (driver.Value, error) {
	if len(p) == 0 {
		return []byte("{}"), nil
	}
	return []byte(p), nil
}

// MarshalAuditPayload is the single entry point for building an AuditPayload
// from a typed payload struct. Returns the raw json bytes for storage.
func MarshalAuditPayload(v any) (AuditPayload, error) {
	if v == nil {
		return AuditPayload("{}"), nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal audit payload: %w", err)
	}
	return AuditPayload(data), nil
}

// --- Typed payload structs (one per BillAuditAction) ---
//
// Money fields are stored as int64 satang in the jsonb payload (matches
// the rest of the billing domain — never baht at this layer).

// AuditCreateDraftPayload captures the shape of a bill at creation time.
// LineItemCount + TotalAmount are snapshot values, not pointers to bill state.
//
// BatchID + RoomID + BillingMonth are populated only for batch-created monthly
// bills (commitOneItem path). They let the audit timeline link a bill back to
// the batch run it came from without joining the batch_items table at query
// time. Direct CreateMonthlyBill / CreateSettlementBill emit these as omit.
type AuditCreateDraftPayload struct {
	LineItemCount int        `json:"line_item_count"`
	TotalAmount   int64      `json:"total_amount"`             // satang
	BatchID       *uuid.UUID `json:"batch_id,omitempty"`       // populated for batch-created bills only
	RoomID        *uuid.UUID `json:"room_id,omitempty"`        // populated for batch-created bills only
	BillingMonth  string     `json:"billing_month,omitempty"`  // populated for batch-created bills only
}

// AuditOverridePayload records one override key transition. When admin edits
// multiple overrides in a single API call, the recorder emits multiple
// audit rows (one per key) so the timeline reads naturally.
//
// Before/After are pointers because absent ≠ zero — a missing key in the
// override map is semantically distinct from an explicit ฿0 override.
type AuditOverridePayload struct {
	Key    string `json:"key"`              // LineItemType as string (e.g. "ELECTRICITY")
	Before *int64 `json:"before,omitempty"` // satang, nil if previously absent
	After  *int64 `json:"after,omitempty"`  // satang, nil if removed
}

// AuditAddManualItemPayload records a single MANUAL line item insertion.
// Quantity + UnitPrice are optional (quantity-mode entries); flat-amount
// entries omit them.
type AuditAddManualItemPayload struct {
	LineType    string `json:"line_type"`
	Description string `json:"description"`
	Amount      int64  `json:"amount"` // satang
	Quantity    *int   `json:"quantity,omitempty"`
	UnitPrice   *int64 `json:"unit_price,omitempty"` // satang
}

// AuditRemoveManualItemPayload records a MANUAL line item removal.
type AuditRemoveManualItemPayload struct {
	LineType    string `json:"line_type"`
	Description string `json:"description"`
	Amount      int64  `json:"amount"` // satang
}

// AuditUpdateNotePayload records a note text change.
type AuditUpdateNotePayload struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// AuditFinalizePayload snapshots the bill total + the status the bill held
// before transitioning to FINALIZED. PreviousStatus is always "DRAFT" today
// (only DRAFTs can be finalized) but the field future-proofs the schema for
// any post-finalize reopen / correction flow that might re-finalize.
type AuditFinalizePayload struct {
	PreviousStatus string `json:"previous_status"`
	TotalAmount    int64  `json:"total_amount"` // satang at time of finalize
}

// AuditVoidPayload records the void reason + the status the bill was in
// before being voided (DRAFT or FINALIZED).
type AuditVoidPayload struct {
	PreviousStatus string `json:"previous_status"`
	Reason         string `json:"reason"`
}

// AuditSupersedePayload records the correction event on the OLD bill.
// PreviousStatus is always "FINALIZED" today (CanCorrect gates on it) but
// the field future-proofs the schema for any future PAID-correction path.
// NewBillID + CorrectionReason mirror the CREATE_FROM_CORRECTION payload
// on the new bill so either end of the chain is queryable on its own.
type AuditSupersedePayload struct {
	PreviousStatus   string    `json:"previous_status"`
	NewBillID        uuid.UUID `json:"new_bill_id"`
	CorrectionReason string    `json:"correction_reason"`
}

// AuditCreateFromCorrectionPayload records the correction event on the NEW
// bill. SupersededBillID + CorrectionReason mirror AuditSupersedePayload on
// the old bill so the timeline is queryable from either end without joining
// the two audit rows together.
type AuditCreateFromCorrectionPayload struct {
	SupersededBillID uuid.UUID `json:"superseded_bill_id"`
	CorrectionReason string    `json:"correction_reason"`
}
