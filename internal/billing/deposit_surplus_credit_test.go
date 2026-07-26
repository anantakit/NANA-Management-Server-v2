package billing

import "testing"

// Regression suite for the surplus-recovery-credit fix (2026-07-26).
//
// LOCK (owner): "Negative settlement total is a legitimate tenant credit.
// Deposit application may clamp charges at zero, but the surplus credit must
// be added to the tenant's refund and must never disappear."
//
// Before the fix, DepositBreakdown clamped charges to zero when the settlement
// total went negative (recovery credits > charges) and dropped the surplus, so
// the itemized RefundAmount / notice net_amount undercounted the tenant refund
// while DepositBalance (deposit - total) counted it — the two disagreed.
//
// These cases exercise the real chain (LineItems -> CalculateTotal ->
// applyDepositCalculation -> DepositBreakdown) and lock the invariant that ties
// the operator bill, tenant statement, and payment recording to one number:
//
//	DepositBalance == RefundAmount - AmountDue   (for every deposit mode)
//	AppliedAmount  >= 0 and AmountDue >= 0        (never a negative "applied")
func TestDepositSurplusCredit_Scenarios(t *testing.T) {
	const deposit = 500000 // 5,000 baht

	cases := []struct {
		name         string
		lineItems    []int64 // satang; summed into TotalAmount
		forfeited    bool
		wantTotal    int64
		wantApplied  int64
		wantRefund   int64
		wantWithheld int64
		wantDue      int64
		wantBalance  int64 // = refund - due
	}{
		{
			// #1 charges < deposit → refund the unused difference
			name:        "charges below deposit refunds difference",
			lineItems:   []int64{300000},
			wantTotal:   300000,
			wantApplied: 300000,
			wantRefund:  200000,
			wantDue:     0,
			wantBalance: 200000,
		},
		{
			// #2 charges = deposit → net zero
			name:        "charges equal deposit net zero",
			lineItems:   []int64{500000},
			wantTotal:   500000,
			wantApplied: 500000,
			wantRefund:  0,
			wantDue:     0,
			wantBalance: 0,
		},
		{
			// #3 charges > deposit → tenant owes the remainder
			name:        "charges above deposit tenant owes",
			lineItems:   []int64{800000},
			wantTotal:   800000,
			wantApplied: 500000,
			wantRefund:  0,
			wantDue:     300000,
			wantBalance: -300000,
		},
		{
			// #4 total = 0 → full deposit refunded
			name:        "zero total refunds full deposit",
			lineItems:   []int64{300000, -300000},
			wantTotal:   0,
			wantApplied: 0,
			wantRefund:  500000,
			wantDue:     0,
			wantBalance: 500000,
		},
		{
			// #5 total < 0 (pure credit) → deposit + surplus credit refunded.
			// This is the OR-MOVEOUT shape: deposit 5,000 + credit 7,650 = 12,650.
			name:        "negative total refunds deposit plus surplus credit",
			lineItems:   []int64{-765000},
			wantTotal:   -765000,
			wantApplied: 0,
			wantRefund:  1265000, // 500000 deposit + 765000 surplus
			wantDue:     0,
			wantBalance: 1265000,
		},
		{
			// #6 charges present but recovery credit exceeds them → the surplus
			// portion flows into the refund; the deposit is untouched (0 applied
			// against zero net charges).
			name:        "credit exceeds charges surplus flows to refund",
			lineItems:   []int64{300000, -1000000},
			wantTotal:   -700000,
			wantApplied: 0,
			wantRefund:  1200000, // 500000 deposit + 700000 surplus
			wantDue:     0,
			wantBalance: 1200000,
		},
		{
			// #7 deposit forfeited but a surplus recovery credit still exists →
			// deposit is withheld, yet the tenant's overpayment is refunded.
			name:         "forfeited deposit still refunds surplus credit",
			lineItems:    []int64{-765000},
			forfeited:    true,
			wantTotal:    -765000,
			wantApplied:  0,
			wantRefund:   765000, // surplus only; deposit withheld
			wantWithheld: 500000,
			wantDue:      0,
			wantBalance:  765000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]BillLineItem, len(tc.lineItems))
			for i, amt := range tc.lineItems {
				items[i] = BillLineItem{Amount: amt}
			}
			b := &Bill{
				BillType:         BillTypeSettlement,
				DepositAmount:    deposit,
				DepositForfeited: tc.forfeited,
				LineItems:        items,
			}
			b.CalculateTotal()
			bd := b.DepositBreakdown()

			if b.TotalAmount != tc.wantTotal {
				t.Errorf("TotalAmount = %d, want %d", b.TotalAmount, tc.wantTotal)
			}
			if bd.AppliedAmount != tc.wantApplied {
				t.Errorf("AppliedAmount = %d, want %d", bd.AppliedAmount, tc.wantApplied)
			}
			if bd.RefundAmount != tc.wantRefund {
				t.Errorf("RefundAmount = %d, want %d", bd.RefundAmount, tc.wantRefund)
			}
			if bd.WithheldAmount != tc.wantWithheld {
				t.Errorf("WithheldAmount = %d, want %d", bd.WithheldAmount, tc.wantWithheld)
			}
			if bd.AmountDue != tc.wantDue {
				t.Errorf("AmountDue = %d, want %d", bd.AmountDue, tc.wantDue)
			}
			if b.DepositBalance != tc.wantBalance {
				t.Errorf("DepositBalance = %d, want %d", b.DepositBalance, tc.wantBalance)
			}

			// Invariants that must hold for EVERY case.
			if got := bd.RefundAmount - bd.AmountDue; got != b.DepositBalance {
				t.Errorf("reconciliation broken: RefundAmount-AmountDue = %d, DepositBalance = %d", got, b.DepositBalance)
			}
			if bd.AppliedAmount < 0 {
				t.Errorf("AppliedAmount must never be negative, got %d", bd.AppliedAmount)
			}
			if bd.AmountDue < 0 {
				t.Errorf("AmountDue must never be negative, got %d", bd.AmountDue)
			}
		})
	}
}
