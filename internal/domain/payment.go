package domain

import (
	"time"

	"github.com/google/uuid"
)

type PaymentMethod string

const (
	PaymentMethodCash     PaymentMethod = "CASH"
	PaymentMethodTransfer PaymentMethod = "TRANSFER"
)

type Payment struct {
	ID         uuid.UUID     `json:"id"`
	BillID     uuid.UUID     `json:"bill_id"`
	Amount     int64         `json:"amount"`
	Method     PaymentMethod `json:"method"`
	PaidAt     time.Time     `json:"paid_at"`
	ReceivedBy *uuid.UUID    `json:"received_by"`
	CreatedAt  time.Time     `json:"created_at"`
}
