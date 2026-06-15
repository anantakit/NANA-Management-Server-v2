package payment

import (
	"time"

	"github.com/google/uuid"
	"nana/internal/shared/money"
)

type RecordBillPaymentRequest struct {
	Amount float64 `json:"amount" validate:"required,gt=0"`
	Method string  `json:"method" validate:"required,oneof=CASH TRANSFER"`
	Note   string  `json:"note"   validate:"omitempty,max=500"`
}

type BillPaymentResponse struct {
	ID         uuid.UUID `json:"id"`
	BillID     uuid.UUID `json:"bill_id"`
	Amount     float64   `json:"amount"`
	Method     string    `json:"method"`
	Note       string    `json:"note"`
	PaidAt     time.Time `json:"paid_at"`
	ReceivedBy *string   `json:"received_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func toBillPaymentResponse(p *BillPayment) *BillPaymentResponse {
	r := &BillPaymentResponse{
		ID:        p.ID,
		BillID:    p.BillID,
		Amount:    money.ToBaht(p.Amount),
		Method:    string(p.Method),
		Note:      p.Note,
		PaidAt:    p.PaidAt,
		CreatedAt: p.CreatedAt,
	}
	if p.ReceivedBy != nil {
		s := p.ReceivedBy.String()
		r.ReceivedBy = &s
	}
	return r
}
