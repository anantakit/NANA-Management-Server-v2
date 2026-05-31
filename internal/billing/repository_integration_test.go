//go:build integration

package billing

import (
	"context"
	"strings"
	"testing"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
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

// TestFindExistingByContractsAndMonth_MatchesUniqueConstraint pins the contract
// between the planner query and the `idx_bills_unique_monthly` partial unique
// index. Pre-fix the query only counted FINALIZED/PAID, so a leftover DRAFT
// (from a correction flow) was invisible to the planner — classification said
// CREATED, commit then hit the constraint and the whole batch wedged. The
// query now mirrors the constraint exactly (status != VOID).
//
// Three rows on the same (contract, month):
//   - DRAFT   → must be returned (constraint blocks INSERTing another)
//   - VOID    → must be excluded (constraint ignores it; FE shows in attention)
//   - DRAFT for a different month → must be excluded (scoping check)
func TestFindExistingByContractsAndMonth_MatchesUniqueConstraint(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-201")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	const month = "2026-05"

	// Same-month VOID — must be filtered out (matches constraint predicate).
	// Reason string mirrors the unexported `voidReasonCorrection` sentinel; the
	// integration test only needs a valid value, not the literal constant.
	voidReason := "CORRECTION"
	if err := db.Create(&Bill{
		ContractID:   c.ID,
		BillingMonth: month,
		BillType:     BillTypeMonthly,
		Status:       BillStatusVoid,
		VoidReason:   &voidReason,
	}).Error; err != nil {
		t.Fatalf("seed VOID bill: %v", err)
	}

	// Same-month DRAFT — must be returned. Pre-fix, this was invisible.
	draftBill := Bill{
		ContractID:   c.ID,
		BillingMonth: month,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
	}
	if err := db.Create(&draftBill).Error; err != nil {
		t.Fatalf("seed DRAFT bill: %v", err)
	}

	// Different-month DRAFT — must be excluded (month scoping).
	if err := db.Create(&Bill{
		ContractID:   c.ID,
		BillingMonth: "2026-04",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
	}).Error; err != nil {
		t.Fatalf("seed other-month DRAFT bill: %v", err)
	}

	repo := NewBillingRepository(db)
	got, err := repo.FindExistingByContractsAndMonth(context.Background(), []uuid.UUID{c.ID}, month)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (DRAFT only)", len(got))
	}
	bill, ok := got[c.ID]
	if !ok {
		t.Fatalf("no entry for contract %s", c.ID)
	}
	if bill.ID != draftBill.ID {
		t.Errorf("returned bill ID = %s, want DRAFT bill ID %s", bill.ID, draftBill.ID)
	}
	if bill.Status != BillStatusDraft {
		t.Errorf("returned bill status = %s, want DRAFT", bill.Status)
	}
}
