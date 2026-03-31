package tenant

import (
	"context"

	"github.com/google/uuid"
)

// ContractChecker checks whether a tenant has an active contract.
// Implemented by contract repository; injected via main.go.
type ContractChecker interface {
	HasActiveByTenantID(ctx context.Context, tenantID uuid.UUID) (bool, error)
}
