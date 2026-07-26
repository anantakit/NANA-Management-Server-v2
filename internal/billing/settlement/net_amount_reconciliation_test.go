package settlement

import (
	"testing"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// Locks the payment-recording projection (notice net_amount) to the canonical
// signed result (Bill.DepositBalance) for surplus-recovery-credit settlements.
//
// Owner lock: the operator bill, tenant statement, and payment step must show
// ONE number. deposit_balance = refund - amount_due (positive = refund);
// net_amount is its inverse (negative = refund). Regression target: OR-MOVEOUT,
// where deposit 5,000 + recovery credit 7,650 must refund 12,650, not 5,000.

// buildSurplusBill mirrors the OR-MOVEOUT shape: charges fully offset by a
// larger recovery credit → negative total → surplus flows to the refund.
func buildSurplusBill(forfeited bool) *billing.Bill {
	b := &billing.Bill{
		BillType:         billing.BillTypeSettlement,
		DepositAmount:    500000, // 5,000 baht
		DepositForfeited: forfeited,
		LineItems: []billing.BillLineItem{
			{Amount: 120000},  // this cycle's charges
			{Amount: -885000}, // recovery credit (over-recorded meter refund)
		},
	}
	b.CalculateTotal() // total = -765000; DepositBalance set here
	return b
}

func TestNetAmount_ReconcilesWithDepositBalance_SurplusCredit(t *testing.T) {
	b := buildSurplusBill(false)

	// Correction path: computeDepositSettlementFromBill -> toSettlementResult.
	ds := computeDepositSettlementFromBill(b)
	res := toSettlementResult(uuid.New(), ds)

	// deposit 5,000 + surplus 7,650 = 12,650 refund.
	if b.DepositBalance != 1265000 {
		t.Fatalf("DepositBalance = %d, want 1265000 (deposit + surplus)", b.DepositBalance)
	}
	if res.NetAmount != -1265000 {
		t.Errorf("net_amount = %d, want -1265000 (refund of deposit + surplus)", res.NetAmount)
	}
	// The invariant: net_amount is the exact inverse of DepositBalance.
	if res.NetAmount != -b.DepositBalance {
		t.Errorf("net_amount %d != -DepositBalance %d", res.NetAmount, -b.DepositBalance)
	}
}

func TestNetAmount_ReconcilesWithDepositBalance_ForfeitedSurplus(t *testing.T) {
	// Even with the deposit forfeited, the surplus recovery credit is the
	// tenant's overpayment and must still be refunded.
	b := buildSurplusBill(true)

	ds := computeDepositSettlementFromBill(b)
	res := toSettlementResult(uuid.New(), ds)

	if b.DepositBalance != 765000 {
		t.Fatalf("DepositBalance = %d, want 765000 (surplus only, deposit withheld)", b.DepositBalance)
	}
	if res.NetAmount != -765000 {
		t.Errorf("net_amount = %d, want -765000 (surplus refund)", res.NetAmount)
	}
}

// TestComputeDepositSettlement_PlanningPath_SurplusCredit locks the planning /
// generate path (planning_service uses computeDepositSettlement directly, not
// the *FromBill variant) to the same surplus behavior.
func TestComputeDepositSettlement_PlanningPath_SurplusCredit(t *testing.T) {
	// deposit 5,000, grossCharges -7,650 (credits exceed charges), returnable.
	ds := computeDepositSettlement(500000, -765000, true)

	if ds.AppliedAmount != 0 {
		t.Errorf("AppliedAmount = %d, want 0 (nothing to offset)", ds.AppliedAmount)
	}
	if ds.RefundAmount != 1265000 {
		t.Errorf("RefundAmount = %d, want 1265000 (deposit + surplus)", ds.RefundAmount)
	}
	if ds.AmountDue != 0 {
		t.Errorf("AmountDue = %d, want 0", ds.AmountDue)
	}

	res := toSettlementResult(uuid.New(), ds)
	if res.NetAmount != -1265000 {
		t.Errorf("net_amount = %d, want -1265000", res.NetAmount)
	}
}
