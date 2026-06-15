package payment

import "nana/internal/shared/respond"

var (
	ErrBillNotFound    = respond.ErrNotFound.WithMessage("ไม่พบบิล")
	ErrBillNotFinalized = respond.ErrBadRequest.WithMessage("บิลต้องอยู่ในสถานะรอชำระก่อนบันทึกการรับชำระ")
	ErrBillAlreadyPaid  = respond.ErrConflict.WithMessage("บิลนี้ชำระแล้ว")
	ErrInvalidBillType  = respond.ErrBadRequest.WithMessage("บันทึกการรับชำระได้เฉพาะบิลรายเดือนเท่านั้น")
	ErrAmountMismatch   = respond.ErrBadRequest.WithMessage("ยอดชำระต้องเท่ากับยอดที่ต้องชำระ")
)
