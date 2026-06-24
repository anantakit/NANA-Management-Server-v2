package billing

import (
	"strings"

	"nana/internal/shared/respond"
)

var (
	ErrBatchNotFound       = respond.ErrNotFound.WithMessage("ไม่พบ batch")
	ErrBillNotFound        = respond.ErrNotFound.WithMessage("ไม่พบบิล")
	ErrContractNotFound    = respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	ErrContractNotActive   = respond.ErrBadRequest.WithMessage("สัญญาไม่ได้อยู่ในสถานะใช้งาน")
	ErrMeterNotFound       = respond.ErrNotFound.WithMessage("ไม่พบข้อมูลมิเตอร์สำหรับเดือนที่ระบุ")
	ErrBillAlreadyExists   = respond.ErrConflict.WithMessage("มีบิลสำหรับเดือนนี้อยู่แล้ว")
	ErrMeterTypeMismatch   = respond.ErrBadRequest.WithMessage("มิเตอร์ที่ระบุไม่ใช่ประเภทรายเดือน")
	ErrMeterRoomMismatch   = respond.ErrBadRequest.WithMessage("มิเตอร์ไม่ตรงกับห้องในสัญญา")
	ErrMeterMonthMismatch  = respond.ErrBadRequest.WithMessage("เดือนของมิเตอร์ไม่ตรงกับเดือนที่ออกบิล")

	// Reading Recovery (Phase 5) — surfaced by RecoveryAdapter when the
	// current-month DRAFT bill is missing. Operator's path: run monthly
	// batch first, then commit recovery.
	//
	// Code "RECOVERY_NO_DRAFT_BILL" lets the FE distinguish this case from the
	// generic CONFLICT family without substring-matching the Thai message (which
	// would silently degrade on copy polish — flagged by ux-reviewer 2026-06-24).
	// The RecoveryDrawer surfaces the "ออกบิลรายเดือนของห้องนี้ก่อน → " CTA
	// only when the code matches.
	ErrRecoveryNoDraftBill = respond.New(
		"RECOVERY_NO_DRAFT_BILL",
		409,
		"ห้องนี้ยังไม่มีบิลร่างของเดือนนี้ — ออกบิลรายเดือนก่อนแล้วจึงปรับยอด",
	)
)

// Settlement-only sentinels (ErrMoveOutNotFound, ErrActualDateRequired,
// ErrExitReadingMissing) migrated to internal/billing/settlement/errors.go in
// W4 commit 3 (2026-06-19).

// IsDuplicateBillError detects a PG unique-constraint violation on the
// bills table. Translates a race-window duplicate INSERT into the
// ErrBillAlreadyExists business-conflict path so per-item commit loops
// can mark the row failed and continue rather than aborting via the
// infra-error break path.
//
// SQLSTATE 23505 + "duplicate key" string match mirrors the convention
// already used by meterreading/service.go isDuplicateKeyError — kept as
// substring match (not pgconn.PgError type assertion) for parity.
//
// Exported so both billing.ReconciliationAdapter and the monthly batch
// commit path (in internal/billing/monthly) can share a single
// implementation without forcing a billing↔monthly import cycle.
// Lives at billing root because it's a billing-domain helper (a single
// canonical duplicate-bill detector), not a generic DB helper.
func IsDuplicateBillError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "23505")
}
