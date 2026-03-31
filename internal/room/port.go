package room

import (
	"context"

	"nana/internal/apartment"

	"github.com/google/uuid"
)

// ApartmentQuerier looks up apartments for validation.
// Implemented by apartment.ApartmentRepository; injected via main.go.
type ApartmentQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*apartment.Apartment, error)
}
