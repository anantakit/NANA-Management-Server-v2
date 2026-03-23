package domain

import (
	"time"

	"github.com/google/uuid"
)

type BillType string

const (
	BillTypeMonthly    BillType = "MONTHLY"
	BillTypeSettlement BillType = "SETTLEMENT"
)

type BillStatus string

const (
	BillStatusPending BillStatus = "PENDING"
	BillStatusPaid    BillStatus = "PAID"
)

type Bill struct {
	ID               uuid.UUID  `json:"id"`
	ContractID       uuid.UUID  `json:"contract_id"`
	RoomID           uuid.UUID  `json:"room_id"`
	BillingMonth     string     `json:"billing_month"`
	BillDate         time.Time  `json:"bill_date"`
	Type             BillType   `json:"type"`
	RoomCharge       int64      `json:"room_charge"`
	ElectricityUnits int        `json:"electricity_units"`
	ElectricityCharge int64     `json:"electricity_charge"`
	WaterUnits       int        `json:"water_units"`
	WaterCharge      int64      `json:"water_charge"`
	TotalAmount      int64      `json:"total_amount"`
	DueDate          time.Time  `json:"due_date"`
	Status           BillStatus `json:"status"`
	DepositDeduction int64      `json:"deposit_deduction"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
