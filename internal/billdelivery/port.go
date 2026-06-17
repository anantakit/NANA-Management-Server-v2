package billdelivery

import (
	"context"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// BillQuerier lets billdelivery validate a bill without importing billing's service.
// Implemented by billing.billingRepository (satisfies the interface directly).
type BillQuerier interface {
	FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
}
