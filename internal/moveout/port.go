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
