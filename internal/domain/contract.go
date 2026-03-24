package domain

import (
	"time"

	"github.com/google/uuid"
)

type ContractStatus string

const (
	ContractStatusActive     ContractStatus = "ACTIVE"
	ContractStatusEnded      ContractStatus = "ENDED"
	ContractStatusTerminated ContractStatus = "TERMINATED"
)

type DepositStatus string

const (
	DepositStatusCollected DepositStatus = "COLLECTED"
	DepositStatusRefunded  DepositStatus = "REFUNDED"
	DepositStatusForfeited DepositStatus = "FORFEITED"
)

type Contract struct {
	ID                     uuid.UUID      `json:"id"`
	TenantID               uuid.UUID      `json:"tenant_id"`
	RoomID                 uuid.UUID      `json:"room_id"`
	StartDate              time.Time      `json:"start_date"`
	MinMonths              int            `json:"min_months"`
	MonthlyRent            int64          `json:"monthly_rent"`
	DepositAmount          int64          `json:"deposit_amount"`
	DepositStatus          DepositStatus  `json:"deposit_status"`
	ElectricityRatePerUnit int64          `json:"electricity_rate_per_unit"`
	WaterRatePerUnit       int64          `json:"water_rate_per_unit"`
	Status                 ContractStatus `json:"status"`
	EndDate                *time.Time     `json:"end_date"`
	MoveOutDate            *time.Time     `json:"move_out_date"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}
