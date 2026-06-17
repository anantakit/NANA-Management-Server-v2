package billdelivery

import "nana/internal/shared/respond"

var (
	ErrBillNotFound        = respond.ErrNotFound.WithMessage("ไม่พบบิล")
	ErrBillNotDeliverable  = respond.ErrBadRequest.WithMessage("ส่งได้เฉพาะบิลรายเดือนที่ยืนยันแล้วเท่านั้น")
)
