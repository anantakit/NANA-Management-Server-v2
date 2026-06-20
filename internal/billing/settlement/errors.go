package settlement

import "nana/internal/shared/respond"

// Settlement-only error sentinels. Workflow-specific — used only by the
// settlement workflow's preview/create/regenerate paths to surface invariant
// violations (no active move-out notice, missing actual move-out date,
// missing EXIT meter reading) to the API layer with Thai messages.
//
// Shared sentinels (ErrBillNotFound, ErrBillAlreadyExists, ErrContractNotFound,
// ErrMeterNotFound) stay at billing root — they're shared with monthly and
// the bill repo. Settlement reaches them as billing.ErrXxx.

var (
	ErrMoveOutNotFound    = respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
	ErrActualDateRequired = respond.ErrBadRequest.WithMessage("ต้องระบุวันย้ายออกจริงก่อนสร้างบิลปิดสัญญา")
	ErrExitReadingMissing = respond.ErrBadRequest.WithMessage("ไม่พบข้อมูลมิเตอร์ย้ายออก กรุณาจดมิเตอร์ย้ายออกก่อน")
)
