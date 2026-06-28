package meterreading

import "nana/internal/shared/respond"

// Phase 5 Reading Recovery — settlement-boundary closure (Lock D).
// Returned by Service.CreateBaselineCorrection in three cases:
//   - source contract not found (source from a vacant period; defensive).
//   - room currently vacant (no current active contract).
//   - source contract has a COMPLETED move-out notice (the crisp doctrinal
//     boundary — financial story closed at move-out).
//
// Single error covers all three for operator-facing simplicity. Thai copy
// frames around move-out as the closed-matter event, not "tenant departed"
// — the boundary is the settlement, not the person.
//
// See feedback_reading_recovery_doctrine.md +
// /Users/anantakit/.claude/plans/smooth-coalescing-flute.md (Phase 7 / Lock D).
// Code "BASELINE_CORRECTION_SETTLEMENT_BOUNDARY_CROSSED" lets the FE distinguish
// this closed-matter case from the generic CONFLICT family without substring-
// matching the Thai message (which would silently degrade on copy polish —
// flagged by ux-reviewer 2026-06-24). Lock D's "no force/override" doctrine
// surfaces via this code in BaselineCorrectionDrawer.
var ErrBaselineCorrectionSettlementBoundaryCrossed = respond.New(
	"BASELINE_CORRECTION_SETTLEMENT_BOUNDARY_CROSSED",
	409,
	"ไม่สามารถปรับฐานได้ — สัญญาของมิเตอร์ต้นทางผ่านขั้นตอนย้ายออกแล้ว เรื่องการเงินช่วงนั้นปิดไปแล้ว",
)

// ErrCorrectionAlreadyApplied surfaces from SoftDeletePendingBaselineCorrection
// when an inverse-FK probe (BillingApplicationChecker) finds a non-VOID
// bill_line_item referencing the correction row. Phase 7 doctrine line 178:
// "ลบการปรับฐานที่บันทึกในบิลแล้วเป็นกระบวนการบนบิล (ยกเลิกผ่านการแก้บิล)".
//
// Code "BASELINE_CORRECTION_ALREADY_APPLIED" lets the FE distinguish this
// case from the generic CONFLICT family without substring-matching the Thai
// message.
var ErrCorrectionAlreadyApplied = respond.New(
	"BASELINE_CORRECTION_ALREADY_APPLIED",
	409,
	"ปรับฐานนี้บันทึกในบิลแล้ว — ยกเลิกผ่านการแก้บิลแทน",
)

// ErrCorrectionNotLatest surfaces from SoftDeletePendingBaselineCorrection
// when the target row is not the most recent READING_RECOVERY anchor for
// the room. Older corrections are immutable historical record — operator
// can only fix-via-delete the latest one. Phase 7 doctrine line 144:
// "Correction ยังแก้ได้ แต่แก้ได้เฉพาะ latest baseline".
var ErrCorrectionNotLatest = respond.New(
	"BASELINE_CORRECTION_NOT_LATEST",
	409,
	"แก้ไขได้เฉพาะการปรับฐานล่าสุดเท่านั้น — รายการเก่ากว่านี้ถูกล็อกแล้ว",
)
