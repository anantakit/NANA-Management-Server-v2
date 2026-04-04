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
}

// MoveOutChecker checks move-out notice status for rooms.
// Used to exclude rooms with pending notices from monthly batch,
// and to validate EXIT reading creation.
// Implemented by moveout.MoveOutRepository; injected via main.go.
type MoveOutChecker interface {
	FindRoomIDsWithPendingNotice(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}
