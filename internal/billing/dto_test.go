package billing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

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
