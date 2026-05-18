package billing

import (
	"testing"
	"time"
)

// --- BillDueDate ---

func TestBillDueDate_HappyPath(t *testing.T) {
	got, ok := BillDueDate("2026-05")
	if !ok {
		t.Fatal("ok = false, want true for valid YYYY-MM")
	}
	want := time.Date(2026, 5, BillingDueDay, 23, 59, 59, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBillDueDate_InvalidInput(t *testing.T) {
	cases := []string{"", "2026", "2026-13", "20260-05", "2026/05", "May 2026"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, ok := BillDueDate(in)
			if ok {
				t.Errorf("ok = true for %q, want false", in)
			}
		})
	}
}

// --- Bill.OverdueDays ---

func makeBill(status BillStatus, billingMonth string, billType BillType) *Bill {
	return &Bill{Status: status, BillingMonth: billingMonth, BillType: billType}
}

// makeMonthlyBill is the common case helper — most tests need a
// FINALIZED MONTHLY bill, which is the only state that actually
// produces an overdue count.
func makeMonthlyBill(status BillStatus, billingMonth string) *Bill {
	return makeBill(status, billingMonth, BillTypeMonthly)
}

func TestBill_OverdueDays_CalendarDayBoundary(t *testing.T) {
	// Due date for 2026-05 is day-5 (end-of-day boundary, but counting
	// uses calendar-day arithmetic). Same calendar date as due = 0, next
	// calendar day = 1, etc.
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")

	cases := []struct {
		name string
		t    time.Time
		want int
	}{
		{"day-5 morning", time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC), 0},
		{"day-5 noon", time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), 0},
		{"day-5 last second", time.Date(2026, 5, 5, 23, 59, 59, 0, time.UTC), 0},
		{"day-6 first second", time.Date(2026, 5, 6, 0, 0, 1, 0, time.UTC), 1},
		{"day-6 morning", time.Date(2026, 5, 6, 8, 0, 0, 0, time.UTC), 1},
		{"day-6 last second", time.Date(2026, 5, 6, 23, 59, 59, 0, time.UTC), 1},
		{"day-8", time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC), 3},
		{"day-30", time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC), 25},
		{"next month day-5", time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), 31},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := b.OverdueDays(c.t); got != c.want {
				t.Errorf("OverdueDays(%v) = %d, want %d", c.t, got, c.want)
			}
		})
	}
}

func TestBill_OverdueDays_NonFinalizedReturnsZero(t *testing.T) {
	// Past day-5 by a lot — but the bill is in a state other than
	// FINALIZED. Overdue is meaningless and must return 0.
	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, status := range []BillStatus{BillStatusDraft, BillStatusPaid, BillStatusVoid} {
		t.Run(string(status), func(t *testing.T) {
			b := makeMonthlyBill(status, "2026-05")
			if got := b.OverdueDays(today); got != 0 {
				t.Errorf("status=%q got %d, want 0", status, got)
			}
		})
	}
}

func TestBill_OverdueDays_SettlementBillReturnsZero(t *testing.T) {
	// Settlement bills follow the move-out workflow, NOT the day-5
	// monthly cadence. Even a FINALIZED settlement bill that's past
	// day-5 of its billing month must surface zero overdue days —
	// the late-payment context only applies to monthly bills (v1 lock).
	b := makeBill(BillStatusFinalized, "2026-05", BillTypeSettlement)
	today := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	if got := b.OverdueDays(today); got != 0 {
		t.Errorf("settlement bill must always return 0 overdue days, got %d", got)
	}
}

func TestBill_OverdueDays_MalformedBillingMonth(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "not-a-month")
	if got := b.OverdueDays(time.Now()); got != 0 {
		t.Errorf("malformed billing_month must return 0, got %d", got)
	}
}

// --- Bill.IsOverdue (derived) ---

func TestBill_IsOverdue_DerivedFromOverdueDays(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")
	t.Run("day-5 = not overdue", func(t *testing.T) {
		if b.IsOverdue(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)) {
			t.Error("day-5 must report not overdue (0 days late)")
		}
	})
	t.Run("day-6 = overdue", func(t *testing.T) {
		if !b.IsOverdue(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)) {
			t.Error("day-6 must report overdue (1 day late)")
		}
	})
}

// --- ComputeLatePenaltyReference ---

func TestComputeLatePenaltyReference_OverdueWithActiveRate(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")
	today := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC) // 5 days past due
	got := ComputeLatePenaltyReference(b, 10000, today)
	if got != 10000 {
		t.Errorf("got %d, want 10000 (rate echoed back when overdue + rate>0)", got)
	}
}

func TestComputeLatePenaltyReference_NotOverdueZero(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")
	today := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) // within due day
	if got := ComputeLatePenaltyReference(b, 10000, today); got != 0 {
		t.Errorf("got %d, want 0 (not yet overdue)", got)
	}
}

func TestComputeLatePenaltyReference_ZeroRateZero(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")
	today := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	if got := ComputeLatePenaltyReference(b, 0, today); got != 0 {
		t.Errorf("got %d, want 0 (rate=0 must suppress reference)", got)
	}
}

func TestComputeLatePenaltyReference_NegativeRateZero(t *testing.T) {
	b := makeMonthlyBill(BillStatusFinalized, "2026-05")
	today := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	if got := ComputeLatePenaltyReference(b, -100, today); got != 0 {
		t.Errorf("got %d, want 0 (negative rate must be treated as zero)", got)
	}
}

func TestComputeLatePenaltyReference_NonFinalizedZero(t *testing.T) {
	today := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	for _, status := range []BillStatus{BillStatusDraft, BillStatusPaid, BillStatusVoid} {
		t.Run(string(status), func(t *testing.T) {
			b := makeMonthlyBill(status, "2026-05")
			if got := ComputeLatePenaltyReference(b, 10000, today); got != 0 {
				t.Errorf("status=%q got %d, want 0", status, got)
			}
		})
	}
}

func TestComputeLatePenaltyReference_SettlementBillZero(t *testing.T) {
	// Settlement bill, FINALIZED, way past day-5 of billing month,
	// rate is active — but result must be 0 because settlement bills
	// do not participate in late-payment context (v1 lock).
	b := makeBill(BillStatusFinalized, "2026-05", BillTypeSettlement)
	today := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	if got := ComputeLatePenaltyReference(b, 10000, today); got != 0 {
		t.Errorf("settlement bill must always return 0, got %d", got)
	}
}

func TestComputeLatePenaltyReference_NilBillZero(t *testing.T) {
	if got := ComputeLatePenaltyReference(nil, 10000, time.Now()); got != 0 {
		t.Errorf("got %d, want 0 (nil bill defensively returns zero)", got)
	}
}
