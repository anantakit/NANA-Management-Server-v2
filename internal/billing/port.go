package billing

import (
	"context"
	"time"

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
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error)
}

// BillingConfigQuerier looks up configurable fees for settlement bills.
type BillingConfigQuerier interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error)
}

// MoveOutQuerier looks up move-out notices for settlement and batch billing.
type MoveOutQuerier interface {
	FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*moveout.MoveOutNotice, error)
	// FindRoomIDsWithMoveOutInMonth returns rooms whose non-terminal move-out
	// notice has scheduled_move_out_date inside the given billing month
	// (YYYY-MM). Used by the monthly batch flow to skip ONLY current-month
	// move-outs (settlement will cover them); rooms with future-month notices
	// still get a normal monthly bill.
	FindRoomIDsWithMoveOutInMonth(ctx context.Context, roomIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error)
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
