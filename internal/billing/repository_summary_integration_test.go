//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestGetSummary_AggregatesByStatus seeds one bill in each status (DRAFT,
// FINALIZED, PAID, VOID) with distinct totals and verifies the summary query
// reports the right count per status and the right amount per AR-lite bucket.
//
// Critical formulas (status-derived under the atomic 1-bill-1-payment model):
//   - pending_amount = SUM total of DRAFT + FINALIZED
//   - paid_amount    = SUM total of PAID
//   - voided_amount  = SUM total of VOID
//   - total_amount   = SUM total of non-VOID (existing — must not regress)
//   - pending_count  = COUNT of FINALIZED only (existing — must not regress;
//     intentionally asymmetric with pending_amount because DRAFT is admin
//     workflow state, not user-facing "ออกบิลแล้วรอชำระ")
func TestGetSummary_AggregatesByStatus(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "S-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	// Distinct totals so any cross-status leak shows up as a wrong number.
	const (
		draftAmt     int64 = 1_000
		finalizedAmt int64 = 2_000
		paidAmt      int64 = 3_000
		voidAmt      int64 = 4_000
	)

	bills := []Bill{
		{ContractID: c.ID, BillingMonth: "2026-04", BillType: BillTypeMonthly, Status: BillStatusDraft, TotalAmount: draftAmt},
		{ContractID: c.ID, BillingMonth: "2026-05", BillType: BillTypeMonthly, Status: BillStatusFinalized, TotalAmount: finalizedAmt},
		{ContractID: c.ID, BillingMonth: "2026-06", BillType: BillTypeMonthly, Status: BillStatusPaid, TotalAmount: paidAmt},
	}
	for i := range bills {
		if err := db.Create(&bills[i]).Error; err != nil {
			t.Fatalf("seed bill %d: %v", i, err)
		}
	}

	// VOID bill needs a separate billing month from the others to avoid the
	// unique-monthly partial index on (contract_id, billing_month) for non-VOID.
	voidBill := Bill{
		ContractID:   c.ID,
		BillingMonth: "2026-07",
		BillType:     BillTypeMonthly,
		Status:       BillStatusVoid,
		TotalAmount:  voidAmt,
	}
	reason := "ทดสอบ"
	voidBill.VoidReason = &reason
	if err := db.Create(&voidBill).Error; err != nil {
		t.Fatalf("seed void bill: %v", err)
	}

	repo := NewBillingRepository(db)
	got, err := repo.GetSummary(context.Background(), BillSummaryParams{
		ApartmentID: apt.ID.String(),
	})
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}

	// Counts (existing semantics — guard against regression)
	if got.TotalCount != 4 {
		t.Errorf("TotalCount = %d, want 4", got.TotalCount)
	}
	if got.PendingCount != 1 {
		t.Errorf("PendingCount (FINALIZED only) = %d, want 1", got.PendingCount)
	}
	if got.PaidCount != 1 {
		t.Errorf("PaidCount = %d, want 1", got.PaidCount)
	}
	if got.VoidedCount != 1 {
		t.Errorf("VoidedCount = %d, want 1", got.VoidedCount)
	}

	// Existing total_amount must stay = sum of non-VOID
	wantTotal := draftAmt + finalizedAmt + paidAmt
	if got.TotalAmount != wantTotal {
		t.Errorf("TotalAmount = %d, want %d (DRAFT+FINALIZED+PAID, VOID excluded)", got.TotalAmount, wantTotal)
	}

	// New AR-lite amounts
	wantPending := draftAmt + finalizedAmt
	if got.PendingAmount != wantPending {
		t.Errorf("PendingAmount = %d, want %d (DRAFT+FINALIZED)", got.PendingAmount, wantPending)
	}
	if got.PaidAmount != paidAmt {
		t.Errorf("PaidAmount = %d, want %d", got.PaidAmount, paidAmt)
	}
	if got.VoidedAmount != voidAmt {
		t.Errorf("VoidedAmount = %d, want %d", got.VoidedAmount, voidAmt)
	}
}

// TestGetSummary_VoidExcludedFromTotalAmount is a focused regression test
// for the existing total_amount semantics. The summary endpoint has been live
// long enough that any change here would silently break consumers.
func TestGetSummary_VoidExcludedFromTotalAmount(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "S-201")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	paid := Bill{ContractID: c.ID, BillingMonth: "2026-04", BillType: BillTypeMonthly, Status: BillStatusPaid, TotalAmount: 1_000}
	if err := db.Create(&paid).Error; err != nil {
		t.Fatalf("seed paid: %v", err)
	}
	voidReason := "ทดสอบ"
	voided := Bill{ContractID: c.ID, BillingMonth: "2026-05", BillType: BillTypeMonthly, Status: BillStatusVoid, TotalAmount: 9_999, VoidReason: &voidReason}
	if err := db.Create(&voided).Error; err != nil {
		t.Fatalf("seed void: %v", err)
	}

	repo := NewBillingRepository(db)
	got, err := repo.GetSummary(context.Background(), BillSummaryParams{ApartmentID: apt.ID.String()})
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}

	if got.TotalAmount != 1_000 {
		t.Errorf("TotalAmount = %d, want 1000 (VOID 9999 must be excluded)", got.TotalAmount)
	}
	if got.VoidedAmount != 9_999 {
		t.Errorf("VoidedAmount = %d, want 9999 (must capture VOID separately)", got.VoidedAmount)
	}
}
