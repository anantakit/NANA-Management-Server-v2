package billing

import (
	"context"
	"time"

	"nana/internal/apartment"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// ContractQuerier looks up contract data for bill generation.
type ContractQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

// MeterReadingQuerier looks up meter readings for bill line items.
//
// Phase 7 also uses FindByIDSimple to load the recovery meter row when
// applying a baseline correction inside UpdateMonthlyDraft (source month
// + sanity check that the row is still a READING_RECOVERY anchor).
type MeterReadingQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error)
	FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*meterreading.MeterReading, error)
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
	// FindConsumptionMonthlyByRoomAndMonth loads the real (non-anchor) MONTHLY
	// consumption row coexisting with a recovery anchor, so buildMonthlyDraftBill
	// can project the recovery overlay per utility after its ID-based re-fetch.
	FindConsumptionMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (*meterreading.MeterReading, error)
	// FindReplacementAnchorsByRoomAndMonth returns the room's PHYSICAL_REPLACEMENT
	// events for the month (oldest-first) so CanonicalPeriodUsage can aggregate
	// their tails into the period usage. Replace Meter.
	FindReplacementAnchorsByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) ([]*meterreading.MeterReading, error)
}

// BillingConfigQuerier looks up configurable fees for settlement bills.
type BillingConfigQuerier interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error)
}


// PaymentRoutingQuerier resolves the payment destination for a room.
// Implemented by apartment.PaymentRoutingService; nil destination means no rules
// are configured — bill is created with null snapshot and blocked at delivery.
type PaymentRoutingQuerier interface {
	ResolveDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) (*apartment.PaymentDestinationInfo, error)
}

// --- Batch billing projections ---

// ContractWithRoom is a lightweight projection for batch billing orchestration.
// Display-read JOIN: contracts + rooms (cross-feature pattern level 1).
type ContractWithRoom struct {
	ContractID            uuid.UUID
	RoomID                uuid.UUID
	RoomNumber            string
	RoomFloor             int
	StartDate             time.Time
	EndDate               *time.Time
	MonthlyRent           int64
	ElectricityRatePerUnit int64
	WaterRatePerUnit      int64
}
