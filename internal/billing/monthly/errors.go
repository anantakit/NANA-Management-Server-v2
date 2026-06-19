package monthly

import "nana/internal/shared/respond"

// --- RePlanBatchItem error sentinels ---
//
// These four errors are workflow-state errors for the single-row replan
// operation. They don't reference any batch entity directly so they can
// live here without coupling to the batch types in billing root.
//
// Shared domain sentinels used by monthly's workflow (ErrBatchNotFound,
// ErrBatchAlreadyCommitted, ErrBillNotFound, ErrNotDraft, ErrNoLineItems,
// ErrBillAlreadyExists, snapshot errors) all remain at billing root because
// they're referenced by the repository and the shared Bill domain methods.
// Monthly imports them as billing.ErrXxx.

var errBatchItemContractMissing = respond.ErrConflict.WithMessage(
	"สัญญานี้ไม่อยู่ในรายการที่ใช้งานแล้ว ไม่สามารถ replan ได้ กรุณาสร้างรอบบิลใหม่",
)

var errBatchItemAlreadyCommitted = respond.ErrConflict.WithMessage(
	"แถวนี้สร้างบิลแล้ว ไม่สามารถ replan ได้",
)

var errBatchItemMismatch = respond.ErrNotFound.WithMessage("ไม่พบรายการในรอบบิลนี้")

var errBatchAlreadyCommittedForReplan = respond.ErrConflict.WithMessage(
	"รอบบิลนี้ commit แล้ว ไม่สามารถ replan ได้",
)
