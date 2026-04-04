package billingconfig

import (
	"context"

	"github.com/google/uuid"
)

// ApartmentChecker verifies apartment existence.
// Implemented by apartment.ApartmentRepository; injected via main.go.
type ApartmentChecker interface {
	ExistsByID(ctx context.Context, id uuid.UUID) (bool, error)
}
