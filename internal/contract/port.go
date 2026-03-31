package contract

import (
	"context"

	"nana/internal/room"
	"nana/internal/tenant"

	"github.com/google/uuid"
)

// RoomQuerier looks up rooms for validation.
// Implemented by room.RoomRepository; injected via main.go.
type RoomQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*room.Room, error)
}

// RoomStatusUpdater updates room status as a cross-feature command.
// Implemented by room.RoomRepository; injected via main.go.
type RoomStatusUpdater interface {
	UpdateStatus(ctx context.Context, id uuid.UUID, status room.RoomStatus) error
}

// TenantQuerier looks up tenants for validation.
// Implemented by tenant.TenantRepository; injected via main.go.
type TenantQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error)
}
