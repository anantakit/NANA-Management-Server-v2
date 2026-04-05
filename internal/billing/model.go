package billing

import (
	"errors"
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
	LineItemPrepaidCredit LineItemType = "PREPAID_CREDIT"
	LineItemOther         LineItemType = "OTHER"
)

// --- Domain errors ---

var (
	ErrNotDraft       = errors.New("บิลไม่ใช่สถานะร่าง")
	ErrNotFinalized   = errors.New("บิลไม่ใช่สถานะยืนยันแล้ว")
	ErrNoLineItems    = errors.New("บิลต้องมีรายการอย่างน้อย 1 รายการ")
	ErrAlreadyVoided  = errors.New("บิลถูกยกเลิกแล้ว")
	ErrAlreadyPaid    = errors.New("บิลถูกชำระแล้ว")
	ErrVoidReasonEmpty = errors.New("กรุณาระบุเหตุผลในการยกเลิก")
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
	Note           string         `gorm:"type:text;not null;default:''" json:"note"`
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
	ID          uuid.UUID    `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	BillID      uuid.UUID    `gorm:"type:uuid;not null" json:"bill_id"`
	LineType    LineItemType `gorm:"type:varchar(30);not null" json:"line_type"`
	Description string       `gorm:"type:text;not null;default:''" json:"description"`
	Amount      int64        `gorm:"not null" json:"amount"`
	Quantity    int          `gorm:"not null;default:0" json:"quantity"`
	UnitPrice   int64        `gorm:"not null;default:0" json:"unit_price"`
	SortOrder   int          `gorm:"not null;default:0" json:"sort_order"`
	CreatedAt   time.Time    `gorm:"not null;default:now()" json:"created_at"`
}

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
		Description: description,
		Amount:      monthlyRent,
		SortOrder:   order,
	}
}

func NewElectricityLine(units int, ratePerUnit int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemElectricity,
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
		Description: description,
		Amount:      amount,
		SortOrder:   order,
	}
}

func NewPrepaidCreditLine(amount int64, description string, order int) BillLineItem {
	return BillLineItem{
		LineType:    LineItemPrepaidCredit,
		Description: description,
		Amount:      -amount, // negative = credit
		SortOrder:   order,
	}
}

// --- Batch billing types ---

type BatchBillItemStatus string

const (
	BatchItemCreated  BatchBillItemStatus = "CREATED"
	BatchItemExisting BatchBillItemStatus = "EXISTING"
	BatchItemSkipped  BatchBillItemStatus = "SKIPPED"
	BatchItemFailed   BatchBillItemStatus = "FAILED"
)

// ContractBillResult is the per-contract outcome of batch billing.
type ContractBillResult struct {
	ContractID uuid.UUID
	RoomID     uuid.UUID
	RoomNumber string
	RoomFloor  int
	Status     BatchBillItemStatus
	ReasonCode string     // machine-readable: MOVE_OUT_PENDING, NO_METER_READING, etc.
	ReasonText string     // Thai human-readable
	BillID     *uuid.UUID // set when CREATED or EXISTING
}

// BatchBillSummary aggregates batch results.
type BatchBillSummary struct {
	TotalContracts int
	Created        int
	Existing       int
	Skipped        int
	Failed         int
}

// BatchBillResult is the complete return value from batch billing.
type BatchBillResult struct {
	Summary BatchBillSummary
	Details []ContractBillResult
}

// --- Projections ---

// BillWithRelations is a projection for list/detail API responses.
type BillWithRelations struct {
	Bill
	TenantName    string    `json:"tenant_name"`
	RoomNumber    string    `json:"room_number"`
	ApartmentName string    `json:"apartment_name"`
	ApartmentID   uuid.UUID `json:"apartment_id"`
}
