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
}

// BillingConfigQuerier looks up configurable fees for settlement bills.
type BillingConfigQuerier interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error)
}

// BaselineCorrectionQuerier exposes meterreading's pending-correction
// lookup to the billing handler. Phase 7 plan B3: the bill convenience
// endpoint (GET /bills/:id/pending-baseline-corrections) resolves
// bill→contract→room then forwards to meterreading. Service-layer
// dispatch lives in meterreading — this is a routing convenience port.
//
// Consumer-defined per cross-feature-patterns.md §4. Satisfied directly
// by meterreading.MeterReadingService (structural typing — the method
// signature matches).
type BaselineCorrectionQuerier interface {
	ListPendingBaselineCorrectionsByRoom(ctx context.Context, roomID uuid.UUID) ([]meterreading.PendingBaselineCorrection, error)
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
