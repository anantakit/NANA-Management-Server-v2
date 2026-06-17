package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// editedTestBill returns a minimal FINALIZED bill the repo mock can return
// from FindByIDWithRelations. Status doesn't affect is_edited — the flag is
// derived from audit log presence, not bill state.
func editedTestBill(id uuid.UUID) *BillWithRelations {
	return &BillWithRelations{
		Bill: Bill{
			ID:           id,
			ContractID:   uuid.New(),
			BillingMonth: "2026-05",
			BillType:     BillTypeMonthly,
			Status:       BillStatusFinalized,
			TotalAmount:  500000,
		},
	}
}

func newIsEditedService(detail *BillWithRelations, listBills []BillWithRelations) (BillingService, *mockBillingRepo, *mockBillAuditRepo) {
	repo := &mockBillingRepo{}
	if detail != nil {
		repo.findByIDWithRelationsFn = func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
			return detail, nil
		}
	}
	if listBills != nil {
		repo.findAllFn = func(_ context.Context, _ BillListParams) ([]BillWithRelations, int64, error) {
			return listBills, int64(len(listBills)), nil
		}
	}
	audit := &mockBillAuditRepo{}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{}, nil, &mockTxManager{})
	return svc, repo, audit
}

// GetByID populates is_edited=true when the audit log carries any edit event.
// Asserts the domain rule binding (IsEditEvent → EditedBillIDs → IsEdited)
// works end-to-end through the service layer.
func TestGetByID_IsEdited_TrueWhenEditEventRecorded(t *testing.T) {
	billID := uuid.New()
	svc, _, audit := newIsEditedService(editedTestBill(billID), nil)
	audit.logs = []BillAuditLog{
		// One UPDATE_OVERRIDE is enough — IsEditEvent classifies it as edit.
		{ID: uuid.New(), BillID: billID, Action: AuditUpdateOverride},
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsEdited {
		t.Errorf("IsEdited = false, want true (UPDATE_OVERRIDE event was recorded)")
	}
}

// GetByID populates is_edited=false when only lifecycle events are recorded.
// Locks the rule that CREATE_DRAFT / FINALIZE / VOID do NOT count toward
// the Edited badge — those represent system state transitions, not user
// curation. A bill that was created + finalized + voided without any
// override/manual/note edit must NEVER show as edited.
func TestGetByID_IsEdited_FalseOnLifecycleOnly(t *testing.T) {
	billID := uuid.New()
	svc, _, audit := newIsEditedService(editedTestBill(billID), nil)
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: billID, Action: AuditCreateDraft},
		{ID: uuid.New(), BillID: billID, Action: AuditFinalize},
		{ID: uuid.New(), BillID: billID, Action: AuditVoid},
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsEdited {
		t.Errorf("IsEdited = true, want false (only lifecycle events recorded, no edit events)")
	}
}

// Freshly generated DRAFT bill (only CREATE_DRAFT in audit) must surface
// is_edited=false. Locks the rule that "just created" never shows as edited.
func TestGetByID_IsEdited_FalseOnFreshDraft(t *testing.T) {
	billID := uuid.New()
	bill := editedTestBill(billID)
	bill.Status = BillStatusDraft
	svc, _, audit := newIsEditedService(bill, nil)
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: billID, Action: AuditCreateDraft},
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsEdited {
		t.Errorf("IsEdited = true on fresh DRAFT, want false")
	}
}

// FINALIZED-after-edit bills must keep is_edited=true. The audit log is
// append-only — a later FINALIZE event doesn't erase the prior edit history.
// Critical for billing forensics: an admin who edited then finalized must
// stay visible in the Edited count for audit/dispute review.
func TestGetByID_IsEdited_StaysTrueAfterFinalize(t *testing.T) {
	billID := uuid.New()
	svc, _, audit := newIsEditedService(editedTestBill(billID), nil)
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: billID, Action: AuditCreateDraft},
		{ID: uuid.New(), BillID: billID, Action: AuditAddManualItem},  // edit
		{ID: uuid.New(), BillID: billID, Action: AuditFinalize},        // lifecycle after
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsEdited {
		t.Errorf("IsEdited = false after FINALIZE, want true (edit history must persist)")
	}
}

// Settlement bills derive is_edited from the same audit semantics — no
// per-type special-casing. Locks the cross-type consistency rule so FE
// can render the Edited badge identically for monthly + settlement.
func TestGetByID_IsEdited_TrueForEditedSettlement(t *testing.T) {
	billID := uuid.New()
	bill := editedTestBill(billID)
	bill.BillType = BillTypeSettlement
	bill.Status = BillStatusDraft
	svc, _, audit := newIsEditedService(bill, nil)
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: billID, Action: AuditUpdateNote},
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsEdited {
		t.Errorf("IsEdited = false on edited settlement, want true (shared audit semantics)")
	}
}

// List endpoint must batch the audit query — exactly ONE EditedBillIDs call
// per page, never one per row. Locks the N+1 prevention contract from the
// commit 6 spec.
func TestList_IsEdited_BatchedSingleAuditQuery(t *testing.T) {
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	page := []BillWithRelations{
		{Bill: Bill{ID: id1, BillType: BillTypeMonthly, Status: BillStatusDraft}},
		{Bill: Bill{ID: id2, BillType: BillTypeMonthly, Status: BillStatusFinalized}},
		{Bill: Bill{ID: id3, BillType: BillTypeMonthly, Status: BillStatusDraft}},
	}
	svc, _, audit := newIsEditedService(nil, page)
	// Only id1 and id3 have edit events; id2 has only lifecycle.
	audit.logs = []BillAuditLog{
		{ID: uuid.New(), BillID: id1, Action: AuditUpdateOverride},
		{ID: uuid.New(), BillID: id2, Action: AuditFinalize},
		{ID: uuid.New(), BillID: id3, Action: AuditAddManualItem},
	}

	bills, _, err := svc.List(context.Background(), BillListParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audit.editedQueryCalls != 1 {
		t.Errorf("EditedBillIDs call count = %d, want 1 (single batched query per page, no N+1)", audit.editedQueryCalls)
	}
	if len(bills) != 3 {
		t.Fatalf("len(bills) = %d, want 3", len(bills))
	}
	// Per-bill flagging is correct.
	expect := map[uuid.UUID]bool{id1: true, id2: false, id3: true}
	for _, b := range bills {
		if b.IsEdited != expect[b.ID] {
			t.Errorf("bill %s IsEdited = %v, want %v", b.ID, b.IsEdited, expect[b.ID])
		}
	}
}

// Empty page must NOT issue an audit query (zero-bill optimization). Saves
// a round-trip on filtered lists that return no rows.
func TestList_IsEdited_EmptyPage_NoAuditQuery(t *testing.T) {
	svc, _, audit := newIsEditedService(nil, []BillWithRelations{})

	bills, _, err := svc.List(context.Background(), BillListParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bills) != 0 {
		t.Errorf("len(bills) = %d, want 0", len(bills))
	}
	if audit.editedQueryCalls != 0 {
		t.Errorf("EditedBillIDs called %d times on empty page, want 0", audit.editedQueryCalls)
	}
}

// Audit-store failure on the read path must propagate — never silently
// drop is_edited to false. Per the lock: "If audit.EditedBillIDs fails,
// return error rather than silently hiding edited state."
func TestList_IsEdited_FailurePropagates(t *testing.T) {
	page := []BillWithRelations{
		{Bill: Bill{ID: uuid.New(), BillType: BillTypeMonthly, Status: BillStatusDraft}},
	}
	svc, _, audit := newIsEditedService(nil, page)
	audit.editedErr = errors.New("audit-store unreachable")

	bills, total, err := svc.List(context.Background(), BillListParams{})
	if err == nil {
		t.Fatal("expected error when EditedBillIDs fails, got nil")
	}
	if bills != nil {
		t.Errorf("bills should be nil on error, got %d rows", len(bills))
	}
	if total != 0 {
		t.Errorf("total should be 0 on error, got %d", total)
	}
}

// Same propagation rule on the single-bill detail path. A drawer that
// rendered is_edited=false because the audit query failed would be
// misleading — admin would see "no edits" on a bill that may have been
// curated heavily, eroding the forensic surface.
func TestGetByID_IsEdited_FailurePropagates(t *testing.T) {
	billID := uuid.New()
	svc, _, audit := newIsEditedService(editedTestBill(billID), nil)
	audit.editedErr = errors.New("audit-store unreachable")

	got, err := svc.GetByID(context.Background(), billID)
	if err == nil {
		t.Fatal("expected error when EditedBillIDs fails, got nil")
	}
	if got != nil {
		t.Errorf("response should be nil on error, got %+v", got)
	}
}
