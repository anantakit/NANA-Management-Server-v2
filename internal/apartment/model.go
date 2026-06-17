package apartment

import (
	"fmt"
	"strconv"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Apartment struct {
	ID                     uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	Name                   string         `gorm:"type:varchar(255);not null" json:"name"`
	DisplayOrder           int            `gorm:"not null;default:0" json:"display_order"`
	ElectricityRatePerUnit int64          `gorm:"not null;default:0" json:"electricity_rate_per_unit"`
	WaterRatePerUnit       int64          `gorm:"not null;default:0" json:"water_rate_per_unit"`
	Address                string         `gorm:"type:text;not null;default:''" json:"address"`
	TaxID                  string         `gorm:"type:varchar(20);not null;default:''" json:"tax_id"`
	CreatedAt              time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt              gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Apartment) TableName() string { return "apartments" }

func (a *Apartment) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// ApartmentBankAccount

type ApartmentBankAccount struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID   uuid.UUID      `gorm:"type:uuid;not null" json:"apartment_id"`
	BankName      string         `gorm:"type:varchar(100);not null" json:"bank_name"`
	AccountName   string         `gorm:"type:varchar(255);not null" json:"account_name"`
	AccountNumber string         `gorm:"type:varchar(50);not null" json:"account_number"`
	PromptPayID   *string        `gorm:"column:promptpay_id;type:varchar(20)" json:"promptpay_id"`
	IsPrimary     bool           `gorm:"not null;default:false" json:"is_primary"`
	Note          *string        `gorm:"type:text" json:"note"`
	CreatedAt     time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`

	ApartmentRef *Apartment `gorm:"foreignKey:ApartmentID" json:"-"`
}

func (ApartmentBankAccount) TableName() string { return "apartment_bank_accounts" }

func (b *ApartmentBankAccount) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

// --- PaymentDestinationRule ---

type PaymentDestinationRuleType string

const (
	RuleTypeApartmentDefault PaymentDestinationRuleType = "APARTMENT_DEFAULT"
	RuleTypeRoomRange        PaymentDestinationRuleType = "ROOM_RANGE"
	RuleTypeRoomOverride     PaymentDestinationRuleType = "ROOM_OVERRIDE"
)

type PaymentDestinationRule struct {
	ID            uuid.UUID                  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	ApartmentID   uuid.UUID                  `gorm:"type:uuid;not null" json:"apartment_id"`
	BankAccountID uuid.UUID                  `gorm:"type:uuid;not null" json:"bank_account_id"`
	RuleType      PaymentDestinationRuleType `gorm:"type:varchar(20);not null" json:"rule_type"`
	RoomNumber    *string                    `gorm:"type:varchar(20)" json:"room_number,omitempty"` // ROOM_OVERRIDE
	RangeStart    *string                    `gorm:"type:varchar(20)" json:"range_start,omitempty"` // ROOM_RANGE
	RangeEnd      *string                    `gorm:"type:varchar(20)" json:"range_end,omitempty"`   // ROOM_RANGE
	CreatedAt     time.Time                  `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt     time.Time                  `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt     gorm.DeletedAt             `gorm:"index" json:"-"`
}

func (PaymentDestinationRule) TableName() string { return "payment_destination_rules" }

func (r *PaymentDestinationRule) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// ValidateRuleFields checks rule-type-specific field invariants.
// Returns a non-nil error string when the rule is structurally invalid.
func (r *PaymentDestinationRule) ValidateRuleFields() error {
	switch r.RuleType {
	case RuleTypeRoomOverride:
		if r.RoomNumber == nil || *r.RoomNumber == "" {
			return fmt.Errorf("room_number required for ROOM_OVERRIDE")
		}
	case RuleTypeRoomRange:
		if r.RangeStart == nil || r.RangeEnd == nil {
			return fmt.Errorf("range_start and range_end required for ROOM_RANGE")
		}
		if err := ValidateRoomRange(*r.RangeStart, *r.RangeEnd); err != nil {
			return err
		}
	}
	return nil
}

// --- PaymentDestinationInfo ---

// PaymentDestinationInfo is the resolved payment destination for a bill.
// Snapshotted into the bill at DRAFT creation time.
type PaymentDestinationInfo struct {
	BankName      string
	AccountNumber string
	AccountName   string
}

// --- Resolver ---

// ResolvePaymentDestination returns the payment destination for roomNumber.
// Priority: ROOM_OVERRIDE > ROOM_RANGE (first match) > APARTMENT_DEFAULT.
// Returns nil if no rule matches — bill will be created with null destination.
func ResolvePaymentDestination(rules []PaymentDestinationRule, accounts map[uuid.UUID]ApartmentBankAccount, roomNumber string) *PaymentDestinationInfo {
	// 1. Room override — exact match
	for _, r := range rules {
		if r.RuleType == RuleTypeRoomOverride && r.RoomNumber != nil && *r.RoomNumber == roomNumber {
			if acct, ok := accounts[r.BankAccountID]; ok {
				return destinationFrom(acct)
			}
		}
	}
	// 2. Room range — first matching range wins
	for _, r := range rules {
		if r.RuleType == RuleTypeRoomRange && r.RangeStart != nil && r.RangeEnd != nil {
			if roomInRange(roomNumber, *r.RangeStart, *r.RangeEnd) {
				if acct, ok := accounts[r.BankAccountID]; ok {
					return destinationFrom(acct)
				}
			}
		}
	}
	// 3. Apartment default
	for _, r := range rules {
		if r.RuleType == RuleTypeApartmentDefault {
			if acct, ok := accounts[r.BankAccountID]; ok {
				return destinationFrom(acct)
			}
		}
	}
	return nil
}

func destinationFrom(a ApartmentBankAccount) *PaymentDestinationInfo {
	return &PaymentDestinationInfo{
		BankName:      a.BankName,
		AccountNumber: a.AccountNumber,
		AccountName:   a.AccountName,
	}
}

// --- Range helpers ---

// RangesOverlap reports whether two room ranges [start1,end1] and [start2,end2]
// overlap (inclusive on both ends). Returns false if either range is unparseable
// or the ranges belong to different building prefixes.
// Used at rule-creation time to enforce unambiguous routing.
func RangesOverlap(start1, end1, start2, end2 string) bool {
	p1, n1s, ok1 := splitRoomNumber(start1)
	_, n1e, ok2 := splitRoomNumber(end1)
	p2, n2s, ok3 := splitRoomNumber(start2)
	_, n2e, ok4 := splitRoomNumber(end2)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return false
	}
	if p1 != p2 {
		return false // different building prefixes — can never overlap
	}
	return n1s <= n2e && n1e >= n2s
}

// ValidateRoomRange checks that start and end share a prefix and start ≤ end.
func ValidateRoomRange(start, end string) error {
	sp, sn, ok1 := splitRoomNumber(start)
	ep, en, ok2 := splitRoomNumber(end)
	if !ok1 || !ok2 {
		return fmt.Errorf("room range must have a numeric suffix (e.g. A101)")
	}
	if sp != ep {
		return fmt.Errorf("range_start and range_end must share the same prefix (got %q and %q)", start, end)
	}
	if sn > en {
		return fmt.Errorf("range_start (%s) must not be greater than range_end (%s)", start, end)
	}
	return nil
}

// roomInRange reports whether roomNumber falls within [start, end].
// Returns false if any number cannot be parsed or prefixes differ.
func roomInRange(roomNumber, start, end string) bool {
	rp, rn, ok1 := splitRoomNumber(roomNumber)
	sp, sn, ok2 := splitRoomNumber(start)
	ep, en, ok3 := splitRoomNumber(end)
	if !ok1 || !ok2 || !ok3 {
		return false
	}
	if rp != sp || rp != ep {
		return false
	}
	return rn >= sn && rn <= en
}

// splitRoomNumber splits a room number into its non-digit prefix and integer suffix.
// "A101" → ("A", 101, true), "101" → ("", 101, true), "ABC" → ("", 0, false).
func splitRoomNumber(s string) (prefix string, num int, ok bool) {
	i := 0
	for i < len(s) && !unicode.IsDigit(rune(s[i])) {
		i++
	}
	if i == len(s) {
		return "", 0, false // no numeric suffix
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return "", 0, false
	}
	return s[:i], n, true
}
