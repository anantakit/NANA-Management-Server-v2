package meterreading

import "nana/internal/shared/respond"

// Phase 5 Reading Recovery — settlement-boundary closure (Lock D).
// Returned by Service.CreateRecovery in three cases:
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
// /Users/anantakit/.claude/plans/hashed-gliding-crab.md (Lock D).
var ErrRecoverySettlementBoundaryCrossed = respond.ErrConflict.WithMessage(
	"ไม่สามารถปรับยอดได้ — สัญญาของมิเตอร์ต้นทางผ่านขั้นตอนย้ายออกแล้ว เรื่องการเงินช่วงนั้นปิดไปแล้ว",
)
