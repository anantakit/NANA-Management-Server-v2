package moveout

import (
	"context"
	"time"

	"nana/internal/contract"

	"github.com/google/uuid"
)

// ContractQuerier looks up contracts for validation.
// Implemented by contract.ContractRepository; injected via main.go.
type ContractQuerier interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

// ContractCommander updates contract state when move-out completes.
// Semantic method — consumer ไม่ต้องรู้ค่า constant ของ contract.
// Implemented by contract.ContractRepository; injected via main.go.
type ContractCommander interface {
	EndContract(ctx context.Context, id uuid.UUID, endDate time.Time) error
}

// RoomCommander updates room state when move-out completes.
// Implemented by room.RoomRepository; injected via main.go.
type RoomCommander interface {
	MarkVacant(ctx context.Context, id uuid.UUID) error
}

// MeterReadingCommander reverts move-out artifacts on the meter-reading side.
// Used by Cancel() to soft-delete the room's active EXIT reading so the
// workflow can be re-initiated cleanly (notice re-created, EXIT re-recorded).
// Semantic method — consumer ไม่ต้องรู้ว่า EXIT reading เก็บยังไง.
// Implemented by meterreading.MeterReadingRepository; injected via main.go.
type MeterReadingCommander interface {
	DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error
}
