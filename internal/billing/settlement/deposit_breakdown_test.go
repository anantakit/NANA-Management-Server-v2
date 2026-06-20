package settlement

// Tests for computeDepositSettlementFromBill — the canonical deposit
// breakdown helper (single source of truth used by both preview and
// commit paths). Ported from billing/model_test.go in W4 settlement
// extraction (2026-06-19). The function being tested is unexported in
// the settlement package; tests build *billing.Bill literals and call
// the helper directly.

import (
	"testing"

	"nana/internal/billing"
)

// --- computeDepositSettlementFromBill invariants ---

func TestComputeDepositSettlementFromBill_Forfeited(t *testing.T) {
	// DepositForfeited=true → ForfeitedAmount = deposit, AvailableToApply = 0
	b := &billing.Bill{
		BillType:         billing.BillTypeSettlement,
		DepositAmount:    500000,
		DepositForfeited: true,
		TotalAmount:      200000,
	}
	ds := computeDepositSettlementFromBill(b)

	if ds.ForfeitedAmount != 500000 {
		t.Errorf("ForfeitedAmount = %d, want 500000", ds.ForfeitedAmount)
	}
	if ds.AvailableToApply != 0 {
		t.Errorf("AvailableToApply = %d, want 0 (fully forfeited)", ds.AvailableToApply)
	}
	if ds.AppliedAmount != 0 {
		t.Errorf("AppliedAmount = %d, want 0", ds.AppliedAmount)
	}
	if ds.AmountDue != 200000 {
		t.Errorf("AmountDue = %d, want 200000", ds.AmountDue)
	}
}

func TestComputeDepositSettlementFromBill_NoneMode_NotForfeited(t *testing.T) {
	// DepositForfeited=false + NONE → ForfeitedAmount = 0, withheld by admin choice
	b := &billing.Bill{
		BillType:      billing.BillTypeSettlement,
		DepositAmount: 500000,
		DepositApp:    billing.DepositAppNone,
		TotalAmount:   200000,
	}
	ds := computeDepositSettlementFromBill(b)

	if ds.ForfeitedAmount != 0 {
		t.Errorf("ForfeitedAmount = %d, want 0 (NONE is admin choice, not forfeiture)", ds.ForfeitedAmount)
	}
	if ds.AvailableToApply != 500000 {
		t.Errorf("AvailableToApply = %d, want 500000 (deposit not forfeited)", ds.AvailableToApply)
	}
	if ds.AppliedAmount != 0 {
		t.Errorf("AppliedAmount = %d, want 0 (NONE = not applied)", ds.AppliedAmount)
	}
	if ds.AmountDue != 200000 {
		t.Errorf("AmountDue = %d, want 200000", ds.AmountDue)
	}
}

func TestComputeDepositSettlementFromBill_FullReturnable(t *testing.T) {
	// FULL + returnable → ForfeitedAmount = 0, AvailableToApply = deposit
	b := &billing.Bill{
		BillType:      billing.BillTypeSettlement,
		DepositAmount: 500000,
		TotalAmount:   300000,
	}
	ds := computeDepositSettlementFromBill(b)

	if ds.ForfeitedAmount != 0 {
		t.Errorf("ForfeitedAmount = %d, want 0", ds.ForfeitedAmount)
	}
	if ds.AvailableToApply != 500000 {
		t.Errorf("AvailableToApply = %d, want 500000", ds.AvailableToApply)
	}
	if ds.AppliedAmount != 300000 {
		t.Errorf("AppliedAmount = %d, want 300000", ds.AppliedAmount)
	}
	if ds.RefundAmount != 200000 {
		t.Errorf("RefundAmount = %d, want 200000", ds.RefundAmount)
	}
	if ds.AmountDue != 0 {
		t.Errorf("AmountDue = %d, want 0", ds.AmountDue)
	}
}

func TestComputeDepositSettlementFromBill_CustomMode(t *testing.T) {
	// CUSTOM with amount < deposit → applied = custom, no forfeiture
	b := &billing.Bill{
		BillType:             billing.BillTypeSettlement,
		DepositAmount:        500000,
		DepositApp:           billing.DepositAppCustom,
		CustomDepositApplied: 200000,
		TotalAmount:          300000,
	}
	ds := computeDepositSettlementFromBill(b)

	if ds.ForfeitedAmount != 0 {
		t.Errorf("ForfeitedAmount = %d, want 0", ds.ForfeitedAmount)
	}
	if ds.AvailableToApply != 500000 {
		t.Errorf("AvailableToApply = %d, want 500000", ds.AvailableToApply)
	}
	if ds.AppliedAmount != 200000 {
		t.Errorf("AppliedAmount = %d, want 200000", ds.AppliedAmount)
	}
	if ds.RefundAmount != 300000 {
		t.Errorf("RefundAmount = %d, want 300000 (500000 - 200000)", ds.RefundAmount)
	}
	if ds.AmountDue != 100000 {
		t.Errorf("AmountDue = %d, want 100000 (300000 - 200000)", ds.AmountDue)
	}
}

func TestComputeDepositSettlementFromBill_CustomExceedsDeposit(t *testing.T) {
	// CUSTOM > deposit → clamped to deposit
	b := &billing.Bill{
		BillType:             billing.BillTypeSettlement,
		DepositAmount:        200000,
		DepositApp:           billing.DepositAppCustom,
		CustomDepositApplied: 999999, // exceeds deposit
		TotalAmount:          300000,
	}
	ds := computeDepositSettlementFromBill(b)

	if ds.AppliedAmount != 200000 {
		t.Errorf("AppliedAmount = %d, want 200000 (clamped to deposit)", ds.AppliedAmount)
	}
	if ds.AmountDue != 100000 {
		t.Errorf("AmountDue = %d, want 100000", ds.AmountDue)
	}
}

func TestComputeDepositSettlementFromBill_Invariant_AvailableEqualsDepositMinusForfeited(t *testing.T) {
	// Invariant: AvailableToApply = DepositAmount - ForfeitedAmount (always)
	cases := []struct {
		name string
		bill billing.Bill
	}{
		{"FULL returnable", billing.Bill{BillType: billing.BillTypeSettlement, DepositAmount: 500000, TotalAmount: 200000}},
		{"FULL forfeited", billing.Bill{BillType: billing.BillTypeSettlement, DepositAmount: 500000, DepositForfeited: true, TotalAmount: 200000}},
		{"NONE", billing.Bill{BillType: billing.BillTypeSettlement, DepositAmount: 500000, DepositApp: billing.DepositAppNone, TotalAmount: 200000}},
		{"CUSTOM", billing.Bill{BillType: billing.BillTypeSettlement, DepositAmount: 500000, DepositApp: billing.DepositAppCustom, CustomDepositApplied: 100000, TotalAmount: 200000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := computeDepositSettlementFromBill(&tc.bill)
			expected := tc.bill.DepositAmount - ds.ForfeitedAmount
			if ds.AvailableToApply != expected {
				t.Errorf("AvailableToApply = %d, want %d (deposit %d - forfeited %d)",
					ds.AvailableToApply, expected, tc.bill.DepositAmount, ds.ForfeitedAmount)
			}
		})
	}
}
