package billing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestToBillResponse_UsageBreakdownExposed is the G1 contract-exposure guard:
// a metered line's frozen usage_breakdown must reach the read DTO verbatim
// (bill-time evidence, no recompute), and ordinary single-segment lines must
// omit it. Without this the FE cannot render "200 = 120 + 80".
func TestToBillResponse_UsageBreakdownExposed(t *testing.T) {
	iptr := func(v int) *int { return &v }
	b := Bill{
		ID:           uuid.New(),
		BillingMonth: "2026-07",
		LineItems: []BillLineItem{
			{
				LineType: LineItemElectricity, Source: LineItemSourceAuto, Quantity: 200, Amount: 160000,
				UsageBreakdown: UsageBreakdown{
					{Kind: "REPLACEMENT_OLD_TAIL", Previous: 1000, Current: 1120, Usage: 120},
					{Kind: "READING", Previous: 350, Current: 430, Usage: 80},
				},
			},
			{ // ordinary line — single meter pair, no breakdown
				LineType: LineItemWater, Source: LineItemSourceAuto, Quantity: 50, Amount: 90000,
				MeterPrevious: iptr(40), MeterCurrent: iptr(90),
			},
		},
	}

	resp := ToBillResponse(b)
	var elec, water *LineItemResponse
	for i := range resp.LineItems {
		switch resp.LineItems[i].LineType {
		case string(LineItemElectricity):
			elec = &resp.LineItems[i]
		case string(LineItemWater):
			water = &resp.LineItems[i]
		}
	}
	if elec == nil || water == nil {
		t.Fatalf("missing elec/water in response: %+v", resp.LineItems)
	}

	// Electricity: breakdown exposed verbatim, sums to the line quantity.
	if len(elec.UsageBreakdown) != 2 {
		t.Fatalf("elec usage_breakdown = %d segments, want 2 (%+v)", len(elec.UsageBreakdown), elec.UsageBreakdown)
	}
	if s := elec.UsageBreakdown[0]; s.Kind != "REPLACEMENT_OLD_TAIL" || s.Previous != 1000 || s.Current != 1120 || s.Usage != 120 {
		t.Errorf("segment[0] = %+v, want old-tail 1000→1120 = 120", s)
	}
	if s := elec.UsageBreakdown[1]; s.Kind != "READING" || s.Usage != 80 {
		t.Errorf("segment[1] = %+v, want reading = 80", s)
	}
	if elec.UsageBreakdown.SumUsage() != elec.Quantity {
		t.Errorf("breakdown sum %d != line quantity %d (contract-boundary drift)", elec.UsageBreakdown.SumUsage(), elec.Quantity)
	}

	// Water: ordinary line omits the breakdown (omitempty) and keeps the meter pair.
	if len(water.UsageBreakdown) != 0 {
		t.Errorf("ordinary water line must omit usage_breakdown, got %+v", water.UsageBreakdown)
	}
	if water.MeterPrevious == nil || water.MeterCurrent == nil {
		t.Error("ordinary water line must keep meter_previous/meter_current")
	}
}

// TestToBillListItemResponse_VoidReason verifies that void_reason flows from
// the Bill model through the list-item DTO converter.
//
// Pure converter test — no DB, no mocks. The contract being tested is:
// "if Bill.VoidReason is set, BillListItemResponse.VoidReason must mirror it
// (and remain nil otherwise)". Critical because the list endpoint is the only
// place the FE can distinguish a manually-cancelled bill from one absorbed by
// settlement (reason = ABSORBED_BY_SETTLEMENT).
func TestToBillListItemResponse_VoidReason(t *testing.T) {
	t.Run("non-void bill — VoidReason nil", func(t *testing.T) {
		b := BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       BillStatusFinalized,
				BillingMonth: "2026-04",
				BillType:     BillTypeMonthly,
				CreatedAt:    time.Now(),
			},
		}
		got := ToBillListItemResponse(b)
		if got.VoidReason != nil {
			t.Fatalf("expected nil VoidReason, got %q", *got.VoidReason)
		}
	})

	t.Run("void bill with manual reason — VoidReason mirrored", func(t *testing.T) {
		reason := "ข้อมูลผิด"
		b := BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       BillStatusVoid,
				VoidReason:   &reason,
				BillingMonth: "2026-04",
				BillType:     BillTypeMonthly,
				CreatedAt:    time.Now(),
			},
		}
		got := ToBillListItemResponse(b)
		if got.VoidReason == nil {
			t.Fatal("expected non-nil VoidReason")
		}
		if *got.VoidReason != reason {
			t.Fatalf("expected VoidReason %q, got %q", reason, *got.VoidReason)
		}
	})

	t.Run("void bill absorbed by settlement — distinguishable from manual void", func(t *testing.T) {
		reason := voidReasonAbsorbed
		b := BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       BillStatusVoid,
				VoidReason:   &reason,
				BillingMonth: "2026-04",
				BillType:     BillTypeMonthly,
				CreatedAt:    time.Now(),
			},
		}
		got := ToBillListItemResponse(b)
		if got.VoidReason == nil || *got.VoidReason != voidReasonAbsorbed {
			t.Fatalf("absorbed reason did not flow through, got %v", got.VoidReason)
		}
	})
}

// TestToBillListItemResponse_ARLiteAmounts verifies the converter routes
// the status-derived AR-lite amounts (paid / outstanding) through the DTO
// correctly and converts satang → baht. The status-formula itself is locked
// in model_test (TestBill_PaidAmount_OutstandingAmount) — this test guards
// the projection layer separately so a converter regression doesn't hide
// behind correct domain logic.
func TestToBillListItemResponse_ARLiteAmounts(t *testing.T) {
	mk := func(status BillStatus, totalSatang int64) BillWithRelations {
		return BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       status,
				TotalAmount:  totalSatang,
				BillingMonth: "2026-04",
				BillType:     BillTypeMonthly,
				CreatedAt:    time.Now(),
			},
		}
	}

	t.Run("PAID — paid=total, outstanding=0 (baht)", func(t *testing.T) {
		got := ToBillListItemResponse(mk(BillStatusPaid, 350_000)) // ฿3,500
		if got.PaidAmount != 3500 {
			t.Errorf("PaidAmount = %v, want 3500", got.PaidAmount)
		}
		if got.OutstandingAmount != 0 {
			t.Errorf("OutstandingAmount = %v, want 0", got.OutstandingAmount)
		}
	})

	t.Run("FINALIZED — paid=0, outstanding=total (baht)", func(t *testing.T) {
		got := ToBillListItemResponse(mk(BillStatusFinalized, 250_000)) // ฿2,500
		if got.PaidAmount != 0 {
			t.Errorf("PaidAmount = %v, want 0", got.PaidAmount)
		}
		if got.OutstandingAmount != 2500 {
			t.Errorf("OutstandingAmount = %v, want 2500", got.OutstandingAmount)
		}
	})

	t.Run("DRAFT — paid=0, outstanding=total (baht)", func(t *testing.T) {
		got := ToBillListItemResponse(mk(BillStatusDraft, 150_000)) // ฿1,500
		if got.OutstandingAmount != 1500 {
			t.Errorf("OutstandingAmount = %v, want 1500", got.OutstandingAmount)
		}
		if got.PaidAmount != 0 {
			t.Errorf("PaidAmount = %v, want 0", got.PaidAmount)
		}
	})

	t.Run("VOID — both zero (excluded from AR)", func(t *testing.T) {
		got := ToBillListItemResponse(mk(BillStatusVoid, 450_000)) // ฿4,500
		if got.PaidAmount != 0 || got.OutstandingAmount != 0 {
			t.Errorf("VOID bill: paid=%v outstanding=%v, want 0/0", got.PaidAmount, got.OutstandingAmount)
		}
	})
}

// TestToBillListItemResponse_FinalizedAt covers DTO mirroring of the new
// finalized_at column. DRAFT bills (which have no FinalizedAt) must keep
// the JSON field nil so the FE can distinguish "not yet finalized" from
// "finalized at instant zero".
func TestToBillListItemResponse_FinalizedAt(t *testing.T) {
	t.Run("DRAFT bill — FinalizedAt nil", func(t *testing.T) {
		b := BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       BillStatusDraft,
				BillingMonth: "2026-04",
				BillType:     BillTypeSettlement,
				CreatedAt:    time.Now(),
			},
		}
		got := ToBillListItemResponse(b)
		if got.FinalizedAt != nil {
			t.Fatalf("DRAFT bill should not expose FinalizedAt, got %v", got.FinalizedAt)
		}
	})

	t.Run("FINALIZED bill — FinalizedAt mirrored", func(t *testing.T) {
		stamp := time.Now().Add(-3 * time.Hour)
		b := BillWithRelations{
			Bill: Bill{
				ID:           uuid.New(),
				Status:       BillStatusFinalized,
				FinalizedAt:  &stamp,
				BillingMonth: "2026-04",
				BillType:     BillTypeMonthly,
				CreatedAt:    time.Now(),
			},
		}
		got := ToBillListItemResponse(b)
		if got.FinalizedAt == nil || !got.FinalizedAt.Equal(stamp) {
			t.Fatalf("FinalizedAt = %v, want %v", got.FinalizedAt, stamp)
		}
	})
}
