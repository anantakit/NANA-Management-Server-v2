package meterreading

import (
	"context"

	"nana/internal/room"

	"github.com/google/uuid"
)

// RoomQuerier looks up rooms for validation.
// Implemented by room.RoomRepository; injected via main.go.
type RoomQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*room.Room, error)
}
