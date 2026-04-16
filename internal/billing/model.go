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
)

// --- Models ---

type Bill struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ContractID     uuid.UUID      `gorm:"type:uuid;not null" json:"contract_id"`
	BillingMonth   string         `gorm:"type:varchar(7);not null" json:"billing_month"`
	BillType       BillType       `gorm:"type:varchar(20);not null" json:"bill_type"`
	Status         BillStatus     `gorm:"type:varchar(20);not null;default:'DRAFT'" json:"status"`
	VoidReason     *string        `gorm:"type:varchar(100)" json:"void_reason"`
	DepositAmount  int64          `gorm:"not null;default:0" json:"deposit_amount"`
	DepositBalance int64          `gorm:"not null;default:0" json:"deposit_balance"`
	TotalAmount    int64          `gorm:"not null;default:0" json:"total_amount"`
	BatchID        *uuid.UUID     `gorm:"type:uuid" json:"batch_id,omitempty"`
	RentPaid           bool               `gorm:"not null;default:false" json:"rent_paid"`
	SettlementRentMode SettlementRentMode `gorm:"column:settlement_rent_mode;type:varchar(30);not null;default:'PRORATED'" json:"settlement_rent_mode"`
	Note               string             `gorm:"type:text;not null;default:''" json:"note"`
	CreatedAt      time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt      time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`

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
	return nil
}

func (b *Bill) Void(reason string) error {
	if reason == "" {
		return ErrVoidReasonEmpty
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

// --- Calculation ---

// CalculateTotal computes TotalAmount from line items.
// For settlement bills, also computes DepositBalance.
func (b *Bill) CalculateTotal() {
	var total int64
	for _, item := range b.LineItems {
		total += item.Amount
	}
	b.TotalAmount = total

	if b.IsSettlement() {
		b.DepositBalance = b.DepositAmount - b.TotalAmount
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

func NewProrateRentLine(daysUsed, daysInMonth int, monthlyRent int64, description string, order int) BillLineItem {
	amount := monthlyRent * int64(daysUsed) / int64(daysInMonth)
	return BillLineItem{
		LineType:    LineItemProrateRent,
		Source:      LineItemSourceAuto,
		Description: description,
		Amount:      amount,
		Quantity:    daysUsed,
		UnitPrice:   monthlyRent / int64(daysInMonth),
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

// BatchItemWithTenant is a projection for batch item API responses (JOIN tenant).
type BatchItemWithTenant struct {
	BillGenerationBatchItem
	TenantName string `json:"tenant_name"`
}

// BillWithRelations is a projection for list/detail API responses.
type BillWithRelations struct {
	Bill
	TenantName    string    `json:"tenant_name"`
	RoomNumber    string    `json:"room_number"`
	ApartmentName string    `json:"apartment_name"`
	ApartmentID   uuid.UUID `json:"apartment_id"`
}

// BillSummaryRaw holds aggregate counts from the summary query (satang).
type BillSummaryRaw struct {
	TotalCount   int   `gorm:"column:total_count"`
	PendingCount int   `gorm:"column:pending_count"`
	PaidCount    int   `gorm:"column:paid_count"`
	VoidedCount  int   `gorm:"column:voided_count"`
	TotalAmount  int64 `gorm:"column:total_amount"` // satang, sum of non-VOID
}
