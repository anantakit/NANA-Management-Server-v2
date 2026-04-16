package moveout

import (
	"context"
	"time"

	"nana/internal/contract"

	"github.com/google/uuid"
)

// ContractQuerier looks up contracts for validation.
// Implemented by contract.ContractRepository; injected via main.go.
type ContractQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

// ContractCommander updates contract state when move-out completes.
// Semantic method — consumer ไม่ต้องรู้ค่า constant ของ contract.
// Implemented by contract.ContractRepository; injected via main.go.
type ContractCommander interface {
	EndContract(ctx context.Context, id uuid.UUID, endDate time.Time) error
}

// RoomCommander updates room state when move-out completes.
// Implemented by room.RoomRepository; injected via main.go.
type RoomCommander interface {
	MarkVacant(ctx context.Context, id uuid.UUID) error
}

// MeterReadingCommander manages move-out artifacts on the meter-reading side.
// Implemented by meterreading.MeterReadingService; injected via main.go.
type MeterReadingCommander interface {
	// CreateExitForMoveOut creates an EXIT meter reading for the given room.
	// Must be called within the caller's transaction context.
	CreateExitForMoveOut(ctx context.Context, roomID uuid.UUID, readingDate time.Time, elecCurrent, waterCurrent int) error

	// DeleteExitByRoomID soft-deletes the room's active EXIT reading.
	// Used by Cancel() so the workflow can be re-initiated cleanly.
	DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error
}

// --- Billing port ---

// SettlementBillResult holds the output of settlement bill generation.
// Defined in the moveout package to avoid circular imports (billing → moveout
// is already established; this DTO lets billing satisfy BillingCommander
// without moveout importing billing).
type SettlementBillResult struct {
	BillID      uuid.UUID
	NetAmount   int64 // satang: positive = tenant pays, negative = refund
	DepositUsed int64 // satang: how much deposit was consumed by charges
}

// RentMode is the settlement rent-mode selector at the moveout port boundary.
//
// Kept as a local typed alias (instead of importing billing's internal
// SettlementRentMode) so that:
//   - moveout callers get autocomplete + oneof-style compile-time safety
//   - billing can independently evolve its internal type
//   - cross-feature rules stay clean — moveout defines its own boundary enum
//
// Wire format matches billing constants 1:1.
type RentMode string

const (
	RentModeUnspecified          RentMode = ""                       // caller wants the default
	RentModeProrated             RentMode = "PRORATED"
	RentModeFullMonthKeepDeposit RentMode = "FULL_MONTH_KEEP_DEPOSIT"
)

// IsValid reports whether m is one of the accepted rent-mode values
// (empty counts as valid — it means "use default").
func (m RentMode) IsValid() bool {
	switch m {
	case RentModeUnspecified, RentModeProrated, RentModeFullMonthKeepDeposit:
		return true
	}
	return false
}

// BillingCommander generates, regenerates, finalizes, and voids settlement bills.
// Implemented by billing.BillingService; injected via main.go.
type BillingCommander interface {
	// GenerateSettlement creates a DRAFT settlement bill for the given contract
	// and move-out date. Snapshots AUTO line items at creation time.
	// rentMode empty → PRORATED (default).
	// Must be called within the caller's transaction context.
	GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error)

	// RegenerateSettlement voids the existing draft, creates a new DRAFT with
	// fresh AUTO items, and preserves any MANUAL items + note from the old bill.
	// rentMode overrides the mode from the old bill if non-empty.
	// Must be called within the caller's transaction context.
	RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode RentMode) (*SettlementBillResult, error)

	// FinalizeSettlement recomputes totals and marks the DRAFT bill as FINALIZED.
	// Must be called within the caller's transaction context.
	FinalizeSettlement(ctx context.Context, billID uuid.UUID) error

	// VoidSettlement marks a settlement bill as VOIDED with the given reason.
	// Must be called within the caller's transaction context.
	VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error
}

// --- Billing query port ---

// SettlementPreviewLineItem holds one line item in a settlement preview.
type SettlementPreviewLineItem struct {
	LineType    string
	Description string
	Amount      int64 // satang
	Quantity    int
	UnitPrice   int64 // satang
	SortOrder   int
}

// SettlementPreviewDeposit holds deposit breakdown in a settlement preview.
type SettlementPreviewDeposit struct {
	Original  int64
	Forfeited int64
	Applied   int64
	Refund    int64
	Due       int64
}

// SettlementPreviewAbsorbedBill holds an outstanding bill absorbed into settlement.
type SettlementPreviewAbsorbedBill struct {
	BillID       uuid.UUID
	BillingMonth string
	TotalAmount  int64 // satang
}

// SettlementPreviewResult holds the full non-persisted settlement preview.
// Defined in moveout to avoid circular imports.
type SettlementPreviewResult struct {
	BillingMonth         string
	ActualMoveOutDate    time.Time
	EffectiveMoveOutDate time.Time
	RentMode             string
	RentPaid             bool
	MinMonths            int
	DepositReturnable    bool
	LineItems            []SettlementPreviewLineItem
	TotalAmount          int64 // satang
	Deposit              SettlementPreviewDeposit
	AbsorbedBills        []SettlementPreviewAbsorbedBill
	Outcome              string // "PAY_MORE", "REFUND", "ZERO_BALANCE"
}

// BillingQuerier provides read-only settlement preview computation.
// Implemented by billing.BillingService; injected via main.go.
type BillingQuerier interface {
	// PreviewSettlementForNotice computes a settlement preview for the given
	// contract. rentMode empty → PRORATED (default).
	PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode RentMode) (*SettlementPreviewResult, error)
}
