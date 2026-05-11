//go:build integration

package billing

import (
	"context"
	"sort"
	"testing"
	"time"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestPreviewSettlement_ParityWithCreate_Integration is the end-to-end guarantee that
// "what the user sees in preview is exactly what gets persisted".
//
// Both Preview and Generate share prepareSettlementPlan, but the persistence
// path (commitSettlementPlan + repo.Create + GORM cascade) could diverge in
// the future — mapping drift, ordering, rounding, or a silent field drop
// would all break the UI contract.
//
// Strategy:
//  1. Build a scenario that exercises all interesting line types (prorate
//     rent + utility + cleaning fee + OUTSTANDING_BILL) and a non-zero
//     settlement outcome (deposit insufficient → tenant owes).
//  2. Call PreviewSettlement (read-only) — snapshot the plan.
//  3. Call GenerateSettlement with identical inputs — persist to DB.
//  4. Fetch the persisted bill and assert every comparable field matches
//     the preview snapshot.
func TestPreviewSettlement_ParityWithCreate_Integration(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "A-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedCleaningFeeConfig(t, db, apt.ID.String(), 30000)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000) // ฿100/day — matches seed default

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -5)

	fixtures.SeedExitMeter(t, db, rm.ID.String(), moveOutDate, 100, 20)
	fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	// One FINALIZED monthly bill from 3 months ago — triggers OUTSTANDING_BILL.
	oldMonth := moveOutDate.AddDate(0, -3, 0).Format("2006-01")
	oldBill := Bill{
		ContractID:   c.ID,
		BillingMonth: oldMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  416000,
	}
	if err := db.Create(&oldBill).Error; err != nil {
		t.Fatalf("create old monthly bill: %v", err)
	}
	oldItems := []BillLineItem{
		{BillID: oldBill.ID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าเช่า", Amount: 300000, SortOrder: 1},
		{BillID: oldBill.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ 20 หน่วย", Amount: 36000, Quantity: 20, UnitPrice: 1800, SortOrder: 2},
		{BillID: oldBill.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า 100 หน่วย", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 3},
	}
	if err := db.Create(&oldItems).Error; err != nil {
		t.Fatalf("create old bill line items: %v", err)
	}

	// ── Wire the real service graph ──
	svc := buildRealBillingService(t, db)
	ctx := context.Background()

	// ── Step 1: Preview (read-only) ──
	preview, err := svc.PreviewSettlement(ctx, PreviewSettlementInput{
		ContractID: c.ID,
		// RentMode empty → defaults to PRORATED (matches Generate default)
	})
	if err != nil {
		t.Fatalf("PreviewSettlement failed: %v", err)
	}
	previewBill := preview.Plan.Bill // unpersisted copy
	previewDeposit := preview.Plan.Deposit
	previewAbsorbed := preview.Plan.BillsToAbsorb

	// ── Step 2: Generate (persists) ──
	result, err := svc.GenerateSettlement(ctx, c.ID, moveOutDate, "")
	if err != nil {
		t.Fatalf("GenerateSettlement failed: %v", err)
	}

	// ── Step 3: Load persisted bill ──
	var persisted Bill
	if err := db.Preload("LineItems").Where("id = ?", result.BillID).First(&persisted).Error; err != nil {
		t.Fatalf("reload persisted bill: %v", err)
	}

	// ── Step 4: Parity assertions ──

	// Bill-level scalar fields
	if got, want := persisted.ContractID, previewBill.ContractID; got != want {
		t.Errorf("ContractID: persisted=%s preview=%s", got, want)
	}
	if got, want := persisted.BillingMonth, previewBill.BillingMonth; got != want {
		t.Errorf("BillingMonth: persisted=%s preview=%s", got, want)
	}
	if got, want := persisted.BillType, previewBill.BillType; got != want {
		t.Errorf("BillType: persisted=%s preview=%s", got, want)
	}
	if got, want := persisted.Status, previewBill.Status; got != want {
		t.Errorf("Status: persisted=%s preview=%s", got, want)
	}
	if got, want := persisted.DepositAmount, previewBill.DepositAmount; got != want {
		t.Errorf("DepositAmount: persisted=%d preview=%d", got, want)
	}
	if got, want := persisted.TotalAmount, previewBill.TotalAmount; got != want {
		t.Errorf("TotalAmount: persisted=%d preview=%d", got, want)
	}
	if got, want := persisted.RentPaid, previewBill.RentPaid; got != want {
		t.Errorf("RentPaid: persisted=%v preview=%v", got, want)
	}
	if got, want := persisted.SettlementRentMode, previewBill.SettlementRentMode; got != want {
		t.Errorf("SettlementRentMode: persisted=%s preview=%s", got, want)
	}

	// Line items — normalize order by (line_type, sort_order, amount) since
	// we can't rely on DB retrieval preserving in-memory order.
	if got, want := len(persisted.LineItems), len(previewBill.LineItems); got != want {
		t.Fatalf("line item count: persisted=%d preview=%d", got, want)
	}
	pSorted := sortLineItems(persisted.LineItems)
	qSorted := sortLineItems(previewBill.LineItems)
	for i := range pSorted {
		p, q := pSorted[i], qSorted[i]
		if p.LineType != q.LineType {
			t.Errorf("line[%d].LineType: persisted=%s preview=%s", i, p.LineType, q.LineType)
		}
		if p.Source != q.Source {
			t.Errorf("line[%d].Source: persisted=%s preview=%s", i, p.Source, q.Source)
		}
		if p.Amount != q.Amount {
			t.Errorf("line[%d] (%s) Amount: persisted=%d preview=%d", i, p.LineType, p.Amount, q.Amount)
		}
		if p.Quantity != q.Quantity {
			t.Errorf("line[%d] (%s) Quantity: persisted=%d preview=%d", i, p.LineType, p.Quantity, q.Quantity)
		}
		if p.UnitPrice != q.UnitPrice {
			t.Errorf("line[%d] (%s) UnitPrice: persisted=%d preview=%d", i, p.LineType, p.UnitPrice, q.UnitPrice)
		}
		if p.SortOrder != q.SortOrder {
			t.Errorf("line[%d] (%s) SortOrder: persisted=%d preview=%d", i, p.LineType, p.SortOrder, q.SortOrder)
		}
		if p.Description != q.Description {
			t.Errorf("line[%d] (%s) Description: persisted=%q preview=%q", i, p.LineType, p.Description, q.Description)
		}
	}

	// Sum of persisted line items must equal bill.TotalAmount — same invariant
	// preview relies on (CalculateTotal ran before commit).
	var sum int64
	for _, li := range persisted.LineItems {
		sum += li.Amount
	}
	if sum != persisted.TotalAmount {
		t.Errorf("sum(persisted line items)=%d != persisted.TotalAmount=%d", sum, persisted.TotalAmount)
	}

	// Deposit-derived outcome (what UI computes and shows).
	// Preview's DepositSettlementState must produce the same net/used as the
	// SettlementBillResult the mutation returned.
	wantNet := int64(0)
	switch {
	case previewDeposit.AmountDue > 0:
		wantNet = previewDeposit.AmountDue
	case previewDeposit.RefundAmount > 0:
		wantNet = -previewDeposit.RefundAmount
	}
	if result.NetAmount != wantNet {
		t.Errorf("result.NetAmount=%d, preview-derived=%d (due=%d refund=%d)",
			result.NetAmount, wantNet, previewDeposit.AmountDue, previewDeposit.RefundAmount)
	}
	if result.DepositUsed != previewDeposit.AppliedAmount {
		t.Errorf("result.DepositUsed=%d preview.AppliedAmount=%d", result.DepositUsed, previewDeposit.AppliedAmount)
	}

	// Effective move-out date should be deterministic — preview exposes it
	// via the top-level SettlementPreview struct (used by the UI header).
	// Defaults to actual move-out date for PRORATED mode.
	if preview.EffectiveMoveOutDate.Format("2006-01-02") != moveOutDate.Format("2006-01-02") {
		t.Errorf("EffectiveMoveOutDate=%s moveOutDate=%s (PRORATED should equal actual)",
			preview.EffectiveMoveOutDate.Format("2006-01-02"), moveOutDate.Format("2006-01-02"))
	}

	// Rent mode on the persisted bill matches what preview reported.
	if string(persisted.SettlementRentMode) != string(preview.RentMode) {
		t.Errorf("persisted SettlementRentMode=%s preview.RentMode=%s",
			persisted.SettlementRentMode, preview.RentMode)
	}

	// Absorbed bills — preview listed them; commit must have voided them AND
	// created matching OUTSTANDING_BILL line items. Check both sides.
	var outstanding []BillLineItem
	for _, li := range persisted.LineItems {
		if li.LineType == LineItemOutstandingBill {
			outstanding = append(outstanding, li)
		}
	}
	if len(outstanding) != len(previewAbsorbed) {
		t.Fatalf("OUTSTANDING_BILL count=%d, preview absorbed count=%d",
			len(outstanding), len(previewAbsorbed))
	}
	// Sum of OUTSTANDING_BILL amounts equals sum of absorbed bill totals
	// (for bills strictly before M-1, which is the case here).
	var outstandingSum, absorbedSum int64
	for _, li := range outstanding {
		outstandingSum += li.Amount
	}
	for _, b := range previewAbsorbed {
		absorbedSum += b.TotalAmount
	}
	if outstandingSum != absorbedSum {
		t.Errorf("sum(OUTSTANDING_BILL)=%d sum(absorbed totals)=%d",
			outstandingSum, absorbedSum)
	}

	// Verify every absorbed bill was actually voided in DB.
	for _, pab := range previewAbsorbed {
		var reloaded Bill
		if err := db.Where("id = ?", pab.ID).First(&reloaded).Error; err != nil {
			t.Fatalf("reload absorbed bill %s: %v", pab.ID, err)
		}
		if reloaded.Status != BillStatusVoid {
			t.Errorf("absorbed bill %s status=%s, want VOID", pab.ID, reloaded.Status)
		}
	}
}

// sortLineItems returns a stable-sorted copy of items keyed by
// (line_type, sort_order, amount). Order-independent comparison.
func sortLineItems(items []BillLineItem) []BillLineItem {
	out := make([]BillLineItem, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LineType != out[j].LineType {
			return out[i].LineType < out[j].LineType
		}
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Amount < out[j].Amount
	})
	return out
}
