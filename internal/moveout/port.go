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
// Implemented by meterreading.MeterReadingRepository; injected via main.go.
type MeterReadingCommander interface {
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

// BillingCommander generates, regenerates, finalizes, and voids settlement bills.
// Implemented by billing.BillingService; injected via main.go.
type BillingCommander interface {
	// GenerateSettlement creates a DRAFT settlement bill for the given contract
	// and move-out date. Snapshots AUTO line items at creation time.
	// Must be called within the caller's transaction context.
	GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time) (*SettlementBillResult, error)

	// RegenerateSettlement voids the existing draft, creates a new DRAFT with
	// fresh AUTO items, and preserves any MANUAL items + note from the old bill.
	// Must be called within the caller's transaction context.
	RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time) (*SettlementBillResult, error)

	// FinalizeSettlement recomputes totals and marks the DRAFT bill as FINALIZED.
	// Must be called within the caller's transaction context.
	FinalizeSettlement(ctx context.Context, billID uuid.UUID) error

	// VoidSettlement marks a settlement bill as VOIDED with the given reason.
	// Must be called within the caller's transaction context.
	VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error
}
