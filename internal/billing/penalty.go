package billing

import "time"

// BillingDueDay encodes the day-of-month when a monthly bill's collection
// window closes — mirrors `BILLING_DUE_DAY` in
// `frontend/src/shared/utils/billingCadence.ts`. Promoted from frontend-only
// to shared cadence when the overdue-awareness hint needed the same
// boundary on the server side.
//
// IMPORTANT: this cadence applies to MONTHLY bills only. Settlement bills
// follow the move-out workflow and never use this due day.
const BillingDueDay = 5

// BillDueDate returns end-of-day on the due date for a given billing month
// (`YYYY-MM`). Returns (zero time, false) when the input is malformed —
// callers decide fallback behavior.
//
// Uses end-of-day so that any moment within day-5 still counts as "due
// today, not yet overdue". Overdue-day counting downstream normalizes
// to calendar dates, so the time-of-day component here is informational.
func BillDueDate(billingMonth string) (time.Time, bool) {
	t, err := time.Parse("2006-01", billingMonth)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), BillingDueDay, 23, 59, 59, 0, time.UTC), true
}

// ComputeLatePenaltyReference returns the apartment's configured late-
// payment penalty as a POLICY REFERENCE — never as a recommendation
// or suggestion. v1 lock (memory/backlog_late_payment_penalty.md):
// the value surfaces as the muted secondary "ค่าปรับล่าช้าตามนโยบายหอ"
// line beneath the primary "เลยกำหนด N วัน" hint; it does not bind
// admin to any action.
//
// Pure function — no DB, no clock side-effects. Callers inject `today`
// so tests pin the boundary.
//
// Returns:
//   - 0 when the bill is nil.
//   - 0 when the bill is not overdue (Bill.IsOverdue gates on
//     FINALIZED + MONTHLY + calendar-day past due).
//   - 0 when `lateRate` is zero or negative (config inactive, unset,
//     or invalid — admin must explicitly configure to enable).
//   - `lateRate` otherwise.
//
// Display-only — never mutates the bill.
func ComputeLatePenaltyReference(b *Bill, lateRate int64, today time.Time) int64 {
	if b == nil || !b.IsOverdue(today) {
		return 0
	}
	if lateRate <= 0 {
		return 0
	}
	return lateRate
}
