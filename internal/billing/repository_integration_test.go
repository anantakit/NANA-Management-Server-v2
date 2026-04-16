//go:build integration

package billing

import (
	"strings"
	"testing"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestLineItemTypes_AllPersist verifies every LineItemType defined in Go can
// be inserted into bill_line_items without violating the DB CHECK constraint.
//
// This test exists because the OUTSTANDING_BILL constant was present in Go but
// absent from the migration CHECK list for multiple releases — unit tests used
// mocks and never hit the real constraint. Keeping this test ensures future
// line-type additions trigger either a migration update or an explicit
// decision to not persist that type.
func TestLineItemTypes_AllPersist(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	bill := Bill{
		ContractID:   c.ID,
		BillingMonth: "2026-04",
		BillType:     BillTypeSettlement,
		Status:       BillStatusDraft,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	// Every LineItemType declared in model.go must be persistable.
	allTypes := []LineItemType{
		LineItemRoomRent,
		LineItemElectricity,
		LineItemWater,
		LineItemCleaningFee,
		LineItemKeyService,
		LineItemProrateRent,
		LineItemPenalty,
		LineItemPrepaidCredit,
		LineItemOutstandingBill,
		LineItemOther,
	}

	for i, lt := range allTypes {
		li := BillLineItem{
			BillID:      bill.ID,
			LineType:    lt,
			Source:      LineItemSourceAuto,
			Description: string(lt),
			Amount:      int64(100 * (i + 1)),
			SortOrder:   i + 1,
		}
		if err := db.Create(&li).Error; err != nil {
			t.Fatalf("LineItemType %q rejected by DB: %v", lt, err)
		}
	}

	// Read-back verification: row count matches what we inserted.
	var count int64
	if err := db.Model(&BillLineItem{}).Where("bill_id = ?", bill.ID).Count(&count).Error; err != nil {
		t.Fatalf("count line items: %v", err)
	}
	if int(count) != len(allTypes) {
		t.Fatalf("expected %d line items, got %d", len(allTypes), count)
	}

	// Cross-check: every inserted LineType is actually present.
	var rows []BillLineItem
	if err := db.Where("bill_id = ?", bill.ID).Find(&rows).Error; err != nil {
		t.Fatalf("fetch line items: %v", err)
	}
	got := make(map[LineItemType]bool, len(rows))
	for _, r := range rows {
		got[r.LineType] = true
	}
	for _, lt := range allTypes {
		if !got[lt] {
			t.Errorf("LineItemType %q inserted but not read back", lt)
		}
	}
}

// TestLineItemType_InvalidRejected guards the direction the constraint exists
// to enforce: an unknown string should be rejected by the DB even if Go
// happens to type-cast it through the LineItemType alias.
func TestLineItemType_InvalidRejected(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	bill := Bill{
		ContractID:   c.ID,
		BillingMonth: "2026-04",
		BillType:     BillTypeSettlement,
		Status:       BillStatusDraft,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}

	li := BillLineItem{
		BillID:      bill.ID,
		LineType:    LineItemType("NOT_A_REAL_TYPE"),
		Source:      LineItemSourceAuto,
		Description: "bogus",
		Amount:      100,
		SortOrder:   1,
	}
	err := db.Create(&li).Error
	if err == nil {
		t.Fatal("expected DB to reject invalid line_type, but insert succeeded")
	}
	// Must be a CHECK-constraint violation specifically — not a NULL/FK/other error
	// that would also satisfy `err != nil` without proving what we want to prove.
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "check constraint") {
		t.Fatalf("expected CHECK constraint violation, got different DB error: %v", err)
	}
	if !strings.Contains(lower, "line_type") && !strings.Contains(lower, "bill_line_items_line_type_check") {
		t.Fatalf("CHECK error did not mention line_type/constraint name: %v", err)
	}
}
