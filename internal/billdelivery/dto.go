package billdelivery

import (
	"time"

	"github.com/google/uuid"
)

type RecordDeliveryRequest struct {
	BillID string  `json:"bill_id" validate:"required,uuid"`
	Note   *string `json:"note"    validate:"omitempty,max=500"`
}

type DeliveryResponse struct {
	ID          uuid.UUID  `json:"id"`
	BillID      uuid.UUID  `json:"bill_id"`
	ActorID     *string    `json:"actor_id,omitempty"`
	Channel     string     `json:"channel"`
	DeliveredAt time.Time  `json:"delivered_at"`
	Note        string     `json:"note"`
}

func toDeliveryResponse(d *BillDelivery) *DeliveryResponse {
	r := &DeliveryResponse{
		ID:          d.ID,
		BillID:      d.BillID,
		Channel:     d.Channel,
		DeliveredAt: d.DeliveredAt,
		Note:        d.Note,
	}
	if d.ActorID != nil {
		s := d.ActorID.String()
		r.ActorID = &s
	}
	return r
}
