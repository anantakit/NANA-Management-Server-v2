package meterreading

import (
	"context"
	"time"

	"nana/internal/contract"
	"nana/internal/room"

	"github.com/google/uuid"
)

// RoomQuerier looks up rooms for validation and baseline computation.
// Implemented by room.RoomRepository; injected via main.go.
type RoomQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*room.Room, error)
	FindRoomIDsByApartment(ctx context.Context, apartmentID uuid.UUID) ([]uuid.UUID, error)
}

// ContractQuerier looks up contract data for baseline filtering and history enrichment.
// Implemented by contract.ContractRepository; injected via main.go.
type ContractQuerier interface {
	FindActiveContractStartDatesByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]time.Time, error)
	FindByRoomIDWithTenants(ctx context.Context, roomID uuid.UUID) ([]contract.ContractTenantSummary, error)
	// Phase 5 (Reading Recovery, Lock D — settlement boundary): returns
	// the contract ID that covered the room at a given billing month.
	// Identifies source's contract → input to the boundary predicate.
	FindContractIDByRoomAndMonth(ctx context.Context, roomID uuid.UUID, billingMonth string) (uuid.UUID, error)
	// Phase 5 (Reading Recovery, Lock D): returns the room's CURRENT
	// active contract ID — the destination for DRAFT bill lookup.
	// ErrRecordNotFound when room is currently vacant.
	FindActiveContractIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error)
}

// MoveOutChecker checks move-out notice status for rooms.
// Used to exclude rooms with pending notices from monthly batch,
// and to validate EXIT reading creation.
// Implemented by moveout.MoveOutRepository; injected via main.go.
type MoveOutChecker interface {
	FindRoomIDsWithPendingNotice(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	// Phase 5 (Reading Recovery, Lock D — the settlement-boundary
	// predicate). True iff the contract has any move_out_notice row
	// with status = COMPLETED. The crisp doctrinal check: a completed
	// move-out closes the recovery chain on that contract.
	HasCompletedMoveOut(ctx context.Context, contractID uuid.UUID) (bool, error)
}

// BillingAdjustmentCommander asks billing to attach an ADJUSTMENT line
// to the current-month DRAFT bill for a contract, with FK provenance to
// the recovery meter row and audit-log emission. Atomic with the
// caller's TX.
//
// Consumer-defined per cross-feature-patterns.md §4. Implemented by
// billing.RecoveryAdapter; injected via main.go.
type BillingAdjustmentCommander interface {
	AttachAdjustmentLine(ctx context.Context, params AttachAdjustmentParams) error
}

// AttachAdjustmentParams travels across the meterreading → billing
// boundary. Primitive types only — no billing-domain types imported
// into meterreading. Phase 5 Lock B: operator-authoritative Amount;
// no SuggestedAmount/Deviation (deferred to post-Phase-5).
type AttachAdjustmentParams struct {
	ContractID         uuid.UUID
	BillingMonth       string // recovery month (current cycle)
	SourceBillingMonth string // for tenant-visible Description provenance
	Amount             int64  // operator-committed signed satang; negative = refund
	RecoveryReadingID  uuid.UUID
	ReasonCode         string // billing validates against AdjustmentReasonCode enum
	Note               string
	ActorID            *uuid.UUID
}
