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

// RoomCommander updates room state as a cross-feature command.
// Semantic methods — consumer ไม่ต้องรู้ค่า constant ของ room.
type RoomCommander interface {
	MarkOccupied(ctx context.Context, id uuid.UUID) error
}

// TenantQuerier looks up tenants for validation.
// Implemented by tenant.TenantRepository; injected via main.go.
type TenantQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error)
}
