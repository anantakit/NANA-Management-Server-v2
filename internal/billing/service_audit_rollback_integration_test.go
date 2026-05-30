//go:build integration

package billing

import (
	"context"
	"errors"
	"testing"

	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// failingAuditRepo wraps a real BillAuditRepository but forces Create to fail.
// EditedBillIDs delegates to the wrapped repo so non-audit reads still work.
// Used by the rollback integration test to drive the parent TX into rollback
// without modifying the production code path.
type failingAuditRepo struct {
	wrapped BillAuditRepository
	err     error
}

func (f *failingAuditRepo) Create(_ context.Context, _ *BillAuditLog) error {
	return f.err
}

func (f *failingAuditRepo) EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return f.wrapped.EditedBillIDs(ctx, billIDs)
}

func (f *failingAuditRepo) FindLatestSupersedeReason(ctx context.Context, billID uuid.UUID) (string, error) {
	return f.wrapped.FindLatestSupersedeReason(ctx, billID)
}

// TestUpdateMonthlyDraft_AuditFailureRollsBackDBState locks the production
// invariant for the locked spec: "audit fail = transaction fail. No swallow,
// no log-only, no background async."
//
// Goes beyond the unit-level mock test (which only proves error propagation)
// by exercising real PostgreSQL TX semantics:
//
//   1. Seed a DRAFT monthly bill with one MANUAL line item + a note +
//      one override in the bills row.
//   2. Build the real billing service but inject a failingAuditRepo so
//      recordAudit returns error immediately.
//   3. Call UpdateMonthlyDraft with edits that would change every field.
//   4. Re-query the bill + line items DIRECTLY from the DB.
//   5. Assert every field is byte-identical to the seeded state — proves
//      PostgreSQL rolled back the INSERT/UPDATE/DELETE chain when the
//      audit Create at the end of the TX returned error.
//
// If this test ever fails, audit failure is leaking state into the bill
// (forensic gap — see project_billing_editable_monthly_arch_lock.md).
func TestUpdateMonthlyDraft_AuditFailureRollsBackDBState(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	// ── Seed: apartment + contract + DRAFT monthly bill with edited state ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "A-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	billID := uuid.New()
	originalNote := "ก่อนถูกแก้"
	originalOverrides := OverrideMap{string(LineItemElectricity): 99999}
	bill := &Bill{
		ID:           billID,
		ContractID:   c.ID,
		BillingMonth: "2026-05",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		Note:         originalNote,
		Overrides:    originalOverrides,
		TotalAmount:  600000,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าห้อง", Amount: 500000, SortOrder: 1},
			{ID: uuid.New(), BillID: billID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 80000, SortOrder: 2},
			// Pre-existing MANUAL item we'll assert survives the failed edit.
			{ID: uuid.New(), BillID: billID, LineType: LineItemCleaningFee, Source: LineItemSourceManual, Description: "ของเดิม MANUAL", Amount: 20000, SortOrder: 3},
		},
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	// ── Build service with failing audit repo ──
	txMgr := database.NewTxManager(db)
	billRepo := NewBillingRepository(db)
	realAuditRepo := NewBillAuditRepository(db)
	audit := &failingAuditRepo{wrapped: realAuditRepo, err: errors.New("audit-store-down (rollback probe)")}
	svc := NewBillingService(billRepo, audit, nil, nil, nil, nil, txMgr)

	// ── Drive the mutation that would touch every editable field ──
	newNote := "หลังถูกแก้ — ห้ามบันทึก"
	newAmount := 999.0
	_, err := svc.UpdateMonthlyDraft(context.Background(), billID, UpdateMonthlyDraftRequest{
		ManualItems: []ManualLineItemRequest{
			{LineType: string(LineItemKeyService), Description: "ของใหม่ MANUAL", Amount: newAmount},
		},
		Overrides: map[string]float64{string(LineItemElectricity): 1.00}, // 100 satang
		Note:      &newNote,
	}, nil)
	if err == nil {
		t.Fatal("expected error from failing audit, got nil")
	}

	// ── Re-query DB and prove nothing changed ──
	var after Bill
	if err := db.Where("id = ?", billID).First(&after).Error; err != nil {
		t.Fatalf("re-query bill: %v", err)
	}

	if after.Note != originalNote {
		t.Errorf("note after rollback = %q, want %q (TX should have rolled back the note update)", after.Note, originalNote)
	}
	if got := after.Overrides[string(LineItemElectricity)]; got != 99999 {
		t.Errorf("override electricity after rollback = %d, want 99999 (TX should have rolled back the override change)", got)
	}
	if after.Status != BillStatusDraft {
		t.Errorf("status after rollback = %s, want DRAFT", after.Status)
	}

	var lineItems []BillLineItem
	if err := db.Where("bill_id = ?", billID).Order("sort_order").Find(&lineItems).Error; err != nil {
		t.Fatalf("re-query line items: %v", err)
	}
	if len(lineItems) != 3 {
		t.Fatalf("line items after rollback = %d, want 3 (2 AUTO + 1 original MANUAL — DELETE of MANUAL + INSERT of new MANUAL must have rolled back)", len(lineItems))
	}
	// AUTO items intact + MANUAL is the ORIGINAL not the new one.
	wantTypes := []LineItemType{LineItemRoomRent, LineItemElectricity, LineItemCleaningFee}
	wantDescs := []string{"ค่าห้อง", "ค่าไฟฟ้า", "ของเดิม MANUAL"}
	for i, li := range lineItems {
		if li.LineType != wantTypes[i] {
			t.Errorf("line[%d].line_type = %s, want %s", i, li.LineType, wantTypes[i])
		}
		if li.Description != wantDescs[i] {
			t.Errorf("line[%d].description = %q, want %q (rollback must restore original row, not the new MANUAL)", i, li.Description, wantDescs[i])
		}
	}

	// ── Audit table must be empty: failing repo never persists, and the
	// in-band assertion above proves the parent TX rolled back. ──
	var auditCount int64
	if err := db.Model(&BillAuditLog{}).Where("bill_id = ?", billID).Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 0 {
		t.Errorf("audit rows = %d, want 0 (failing recorder must not persist)", auditCount)
	}

	_ = gorm.ErrRecordNotFound // keep import live for symmetry with sibling integration tests
}
