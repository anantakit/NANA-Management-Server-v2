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
	// Meter-hardware flags (rollover/replaced) + Model B over-record flags
	// ("เดือนก่อนจดเกิน") are plain bool — create always supplies them (unlike
	// UpdateExitForMoveOut's *bool "keep existing"). When an over-record flag is
	// set the command also creates the READING_RECOVERY re-anchor before the exit
	// reading (Epic B Model B). Must be called within the caller's transaction.
	CreateExitForMoveOut(ctx context.Context, roomID uuid.UUID, readingDate time.Time, elecCurrent, waterCurrent int,
		elecReplaced, waterReplaced, elecRollover, waterRollover, elecOverRecord, waterOverRecord bool) error

	// UpdateExitForMoveOut updates an existing EXIT reading in-place.
	// Flags use *bool: nil = keep existing value.
	// Must be called within the caller's transaction context.
	UpdateExitForMoveOut(ctx context.Context, roomID uuid.UUID,
		elecCurrent, waterCurrent *int, readingDate *time.Time,
		elecReplaced, waterReplaced, elecRollover, waterRollover *bool) error

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

// CorrectSettlementInput bundles the inputs for BillingCommander.CorrectSettlement.
// Struct-form (vs positional) because the field set is large enough that a
// flat parameter list becomes a readability hazard and easy to mis-order at
// the call site. Mirrors the user-triggered void+recreate intent.
//
// Actor lives in the struct rather than as a separate signature parameter so
// the port shape stays (ctx, in) — symmetric with the standard Go pattern
// for command ports. (Other BillingCommander methods drop actor entirely
// because they're system-triggered; correction is the first user-attributed
// one, see service_moveout_ports.go for the audit trail rationale.)
type CorrectSettlementInput struct {
	ExistingBillID   uuid.UUID
	ContractID       uuid.UUID
	MoveOutDate      time.Time
	RentMode         RentMode // empty → derived from the existing bill's setting
	CorrectionReason string
	Actor            *uuid.UUID // nil for system-triggered events; UI sets the admin's user ID
}

// BillingCommander generates, regenerates, finalizes, voids, and corrects
// settlement bills. Implemented by billing.BillingService; injected via main.go.
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

	// CorrectSettlement is the user-triggered void+recreate flow for a
	// FINALIZED settlement bill. Differs from RegenerateSettlement on three
	// axes: (1) requires CanCorrect guard (FINALIZED, not PAID, not VOID,
	// not already superseded), (2) sets old.superseded_by_bill_id to the
	// new bill so the audit trail forms an explicit chain, (3) does NOT
	// carry over MANUAL items / overrides / note — fresh slate per the
	// "regenerate not clone" architectural lock (admin re-applies edits
	// against the new DRAFT if needed).
	//
	// Caller (moveout orchestrator) is responsible for rebinding
	// notice.settlement_bill_id + notice.net_amount in the same tx, plus
	// the workflow rewind (downgrade to PENDING_SETTLEMENT, clear payment
	// metadata). Must be called within the caller's transaction context.
	//
	// Emits SUPERSEDE on old + CREATE_FROM_CORRECTION on new in the same
	// tx, attributed to in.Actor (nil → system event). Audit failure rolls
	// back the entire correction.
	CorrectSettlement(ctx context.Context, in CorrectSettlementInput) (*SettlementBillResult, error)
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

// DataState indicates whether the preview data is complete or has gaps.
type DataState string

const (
	DataStateComplete   DataState = "complete"
	DataStateIncomplete DataState = "incomplete"
)

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
	DataState            DataState
	Warnings             []string
}

// BillingQuerier provides read-only settlement preview computation.
// Implemented by billing.BillingService; injected via main.go.
type BillingQuerier interface {
	// PreviewSettlementForNotice computes a settlement preview for the given
	// contract. rentMode empty → PRORATED (default).
	PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode RentMode) (*SettlementPreviewResult, error)
}
