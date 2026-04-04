package billing

import (
	"context"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"

	"github.com/google/uuid"
)

// ContractQuerier looks up contract data for bill generation.
type ContractQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

// MeterReadingQuerier looks up meter readings for bill line items.
type MeterReadingQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error)
	FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*meterreading.MeterReading, error)
}

// BillingConfigQuerier looks up configurable fees for settlement bills.
type BillingConfigQuerier interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error)
}

// MoveOutQuerier looks up move-out notices for settlement validation.
type MoveOutQuerier interface {
	FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*moveout.MoveOutNotice, error)
}
