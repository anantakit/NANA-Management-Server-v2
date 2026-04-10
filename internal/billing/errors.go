package billing

import "nana/internal/shared/respond"

var (
	ErrBatchNotFound       = respond.ErrNotFound.WithMessage("ไม่พบ batch")
	ErrBillNotFound        = respond.ErrNotFound.WithMessage("ไม่พบบิล")
	ErrContractNotFound    = respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	ErrContractNotActive   = respond.ErrBadRequest.WithMessage("สัญญาไม่ได้อยู่ในสถานะใช้งาน")
	ErrMeterNotFound       = respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์สำหรับเดือนที่ระบุ")
	ErrBillAlreadyExists   = respond.ErrConflict.WithMessage("มีบิลสำหรับเดือนนี้อยู่แล้ว")
	ErrMoveOutNotFound     = respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
	ErrMoveOutNotCompleted = respond.ErrBadRequest.WithMessage("ใบแจ้งย้ายออกยังไม่ดำเนินการเสร็จสิ้น กรุณา complete ก่อนออกบิล")
	ErrExitReadingMissing  = respond.ErrBadRequest.WithMessage("ไม่พบข้อมูลมิเตอร์ย้ายออก กรุณาจดมิเตอร์ย้ายออกก่อน")
	ErrMeterTypeMismatch   = respond.ErrBadRequest.WithMessage("มิเตอร์ที่ระบุไม่ใช่ประเภทรายเดือน")
	ErrMeterRoomMismatch   = respond.ErrBadRequest.WithMessage("มิเตอร์ไม่ตรงกับห้องในสัญญา")
	ErrMeterMonthMismatch  = respond.ErrBadRequest.WithMessage("เดือนของมิเตอร์ไม่ตรงกับเดือนที่ออกบิล")
)
