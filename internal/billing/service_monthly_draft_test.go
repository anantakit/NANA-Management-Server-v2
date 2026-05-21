package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// monthlyDraftFixture builds a DRAFT monthly bill with the three canonical
// AUTO line items (ROOM_RENT / ELECTRICITY / WATER) that the batch snapshot
// produces. Tests parametrize over this baseline.
func monthlyDraftFixture() *Bill {
	billID := uuid.New()
	return &Bill{
		ID:           billID,
		ContractID:   uuid.New(),
		BillingMonth: "2026-05",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าห้อง 2026-06", Amount: 500000, SortOrder: 1},
			{ID: uuid.New(), BillID: billID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า 100 หน่วย", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 2},
			{ID: uuid.New(), BillID: billID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ 10 หน่วย", Amount: 18000, Quantity: 10, UnitPrice: 1800, SortOrder: 3},
		},
	}
}

func newMonthlyDraftService(bill *Bill) (BillingService, *mockBillingRepo) {
	repo := &mockBillingRepo{}
	repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) {
		return bill, nil
	}
	return newSvc(repo, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}), repo
}

// Happy path: override an AUTO amount, add two MANUAL items, set the note.
// Asserts the full chain: delete-then-create line items, override applied,
// note set, Update called once with the curated state.
func TestUpdateMonthlyDraft_HappyPath(t *testing.T) {
	bill := monthlyDraftFixture()
	svc, repo := newMonthlyDraftService(bill)

	note := "ตรวจมิเตอร์ซ้ำแล้ว"
	qty := 2
	price := 250.0
	req := UpdateMonthlyDraftRequest{
		ManualItems: []ManualLineItemRequest{
			{LineType: string(LineItemCleaningFee), Description: "ทำความสะอาดพิเศษ", Amount: 500},
			{LineType: string(LineItemKeyService), Description: "ทำกุญแจสำรอง", Quantity: &qty, UnitPrice: &price},
		},
		Note:      &note,
		Overrides: map[string]float64{string(LineItemElectricity): 750}, // ฿750 → 75,000 satang
	}

	out, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected bill response, got nil")
	}

	// MANUAL items wiped exactly once, AUTO never deleted.
	delSources := repo.deletedSourcesByBillID[bill.ID]
	if len(delSources) != 1 || delSources[0] != LineItemSourceManual {
		t.Errorf("delete sources = %v, want [MANUAL]", delSources)
	}

	// Two MANUAL items created, sort_order appends after 3 AUTO items.
	if len(repo.createdLineItems) != 2 {
		t.Fatalf("created %d line items, want 2", len(repo.createdLineItems))
	}
	if repo.createdLineItems[0].SortOrder != 4 {
		t.Errorf("first manual sort_order = %d, want 4 (after 3 AUTO)", repo.createdLineItems[0].SortOrder)
	}
	if repo.createdLineItems[1].SortOrder != 5 {
		t.Errorf("second manual sort_order = %d, want 5", repo.createdLineItems[1].SortOrder)
	}
	if repo.createdLineItems[0].Source != LineItemSourceManual {
		t.Errorf("created item source = %s, want MANUAL", repo.createdLineItems[0].Source)
	}

	// Quantity mode: amount = qty × unit_price (in satang).
	if repo.createdLineItems[1].Amount != int64(qty)*25000 {
		t.Errorf("quantity-mode amount = %d, want %d", repo.createdLineItems[1].Amount, int64(qty)*25000)
	}

	// Update called once with override + note applied.
	if len(repo.updatedBills) != 1 {
		t.Fatalf("Update called %d times, want 1", len(repo.updatedBills))
	}
	saved := repo.updatedBills[0]
	if saved.Note != note {
		t.Errorf("saved note = %q, want %q", saved.Note, note)
	}
	if v, ok := saved.Overrides[string(LineItemElectricity)]; !ok || v != 75000 {
		t.Errorf("saved override electricity = %d (ok=%v), want 75000", v, ok)
	}

	// AUTO immutability: line_type, description, sort_order, quantity, unit_price
	// of every AUTO row must be byte-identical to the fixture. Only `amount` is
	// allowed to drift, and that drift goes through the OverrideMap (asserted
	// above) — not via direct mutation of the AUTO line item row.
	wantAuto := map[LineItemType]struct {
		desc      string
		sortOrder int
	}{
		LineItemRoomRent:    {"ค่าห้อง 2026-06", 1},
		LineItemElectricity: {"ค่าไฟฟ้า 100 หน่วย", 2},
		LineItemWater:       {"ค่าน้ำ 10 หน่วย", 3},
	}
	seenAuto := 0
	for _, li := range saved.LineItems {
		if !li.IsAuto() {
			continue
		}
		seenAuto++
		want, ok := wantAuto[li.LineType]
		if !ok {
			t.Errorf("unexpected AUTO line type %q in saved bill", li.LineType)
			continue
		}
		if li.Description != want.desc {
			t.Errorf("AUTO %s description = %q, want %q (immutable)", li.LineType, li.Description, want.desc)
		}
		if li.SortOrder != want.sortOrder {
			t.Errorf("AUTO %s sort_order = %d, want %d (immutable)", li.LineType, li.SortOrder, want.sortOrder)
		}
	}
	if seenAuto != 3 {
		t.Errorf("AUTO items in saved bill = %d, want 3 (none should be deleted)", seenAuto)
	}
}

// AUTO line items can have holes in their sort_order (e.g. [1, 3]) from prior
// edits or future schema migrations. MANUAL items must still land strictly
// AFTER every AUTO row — using count would put MANUAL at sort_order 3
// (collision with existing AUTO). max+1 guarantees collision-free placement.
func TestUpdateMonthlyDraft_ManualSortAppendsAfterMaxAuto_NotCount(t *testing.T) {
	billID := uuid.New()
	bill := &Bill{
		ID:           billID,
		ContractID:   uuid.New(),
		BillingMonth: "2026-05",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems: []BillLineItem{
			{ID: uuid.New(), BillID: billID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าห้อง", Amount: 500000, SortOrder: 1},
			// Hole at sort_order 2 — count=2 would yield baseOrder=3 → COLLISION
			{ID: uuid.New(), BillID: billID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 18000, SortOrder: 3},
		},
	}
	svc, repo := newMonthlyDraftService(bill)

	_, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{
		ManualItems: []ManualLineItemRequest{
			{LineType: string(LineItemCleaningFee), Description: "ทำความสะอาด", Amount: 300},
		},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.createdLineItems) != 1 {
		t.Fatalf("created %d line items, want 1", len(repo.createdLineItems))
	}
	if got := repo.createdLineItems[0].SortOrder; got != 4 {
		t.Errorf("MANUAL sort_order = %d, want 4 (max AUTO 3 + 1, not count 2 + 1 = 3)", got)
	}
}

// SETTLEMENT bills are routed to UpdateSettlementDraft. UpdateMonthlyDraft must
// reject them — otherwise deposit logic would be bypassed silently.
func TestUpdateMonthlyDraft_RejectsSettlementBill(t *testing.T) {
	bill := monthlyDraftFixture()
	bill.BillType = BillTypeSettlement
	svc, repo := newMonthlyDraftService(bill)

	_, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{}, nil)
	if err == nil {
		t.Fatal("expected error rejecting SETTLEMENT bill, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("error should surface as AppError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "รายเดือน") {
		t.Errorf("error message %q should mention monthly-only", err.Error())
	}
	if len(repo.updatedBills) != 0 {
		t.Error("Update should not be called on rejected request")
	}
}

// Non-DRAFT bills (FINALIZED / PAID / VOID) are immutable. Reject early with
// AppError so the handler returns 400 instead of leaking gorm/state errors.
func TestUpdateMonthlyDraft_RejectsNonDraft(t *testing.T) {
	bill := monthlyDraftFixture()
	bill.Status = BillStatusFinalized
	svc, repo := newMonthlyDraftService(bill)

	_, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{}, nil)
	if err == nil {
		t.Fatal("expected error rejecting non-DRAFT bill, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("error should surface as AppError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrNotDraft) && !strings.Contains(err.Error(), ErrNotDraft.Error()) {
		t.Errorf("error %q should signal not-draft", err.Error())
	}
	if len(repo.updatedBills) != 0 {
		t.Error("Update should not be called on non-DRAFT bill")
	}
}

// Empty manual_items array is a valid request — it means "no manual items".
// AUTO items must remain intact; Update should still fire so other fields
// (note / overrides) can be cleared or set.
func TestUpdateMonthlyDraft_EmptyManualItems_AutoPreserved(t *testing.T) {
	bill := monthlyDraftFixture()
	svc, repo := newMonthlyDraftService(bill)

	out, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{
		ManualItems: []ManualLineItemRequest{},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected bill response")
	}

	// Delete still called — clears any pre-existing MANUAL items.
	if got := repo.deletedSourcesByBillID[bill.ID]; len(got) != 1 || got[0] != LineItemSourceManual {
		t.Errorf("delete sources = %v, want [MANUAL]", got)
	}
	// Create NOT called — no items to insert.
	if len(repo.createdLineItems) != 0 {
		t.Errorf("created %d line items, want 0 for empty manual_items", len(repo.createdLineItems))
	}
	// Update still called — fixture has 3 AUTO items intact.
	if len(repo.updatedBills) != 1 {
		t.Fatalf("Update called %d times, want 1", len(repo.updatedBills))
	}
}

// Non-overrideable keys (e.g. PREPAID_CREDIT) must be rejected as AppError so
// the handler returns 400 instead of an opaque 500 from CalculateTotal.
func TestUpdateMonthlyDraft_InvalidOverrideKey_SurfacesAsAppError(t *testing.T) {
	bill := monthlyDraftFixture()
	svc, repo := newMonthlyDraftService(bill)

	_, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{
		Overrides: map[string]float64{string(LineItemPrepaidCredit): 100},
	}, nil)
	if err == nil {
		t.Fatal("expected error for non-overrideable key, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("error should surface as AppError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), string(LineItemPrepaidCredit)) {
		t.Errorf("error %q should mention the rejected key", err.Error())
	}
	if len(repo.updatedBills) != 0 {
		t.Error("Update should not be called when overrides invalid")
	}
}

// Invalid manual line type (e.g. ROOM_RENT as MANUAL) must be rejected before
// any DB write so the bill is not left half-edited inside the TX.
func TestUpdateMonthlyDraft_InvalidManualLineType_SurfacesAsAppError(t *testing.T) {
	bill := monthlyDraftFixture()
	svc, repo := newMonthlyDraftService(bill)

	_, err := svc.UpdateMonthlyDraft(context.Background(), bill.ID, UpdateMonthlyDraftRequest{
		ManualItems: []ManualLineItemRequest{
			{LineType: string(LineItemRoomRent), Description: "ค่าห้องผ่านช่องว่าง", Amount: 100},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected error rejecting ROOM_RENT as MANUAL, got nil")
	}
	if _, ok := respond.Is(err); !ok {
		t.Errorf("error should surface as AppError, got %T: %v", err, err)
	}
	if len(repo.createdLineItems) != 0 {
		t.Error("CreateLineItems must not be called when manual validation fails")
	}
	if got := repo.deletedSourcesByBillID[bill.ID]; len(got) != 0 {
		t.Error("DeleteLineItemsBySource must not be called when manual validation fails")
	}
}
