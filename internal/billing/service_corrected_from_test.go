package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Replacement bills (the DRAFT/FINALIZED/PAID side of a correction chain)
// must surface CorrectedFromBillID via GetByID so the BillDrawer can render
// the "บิลนี้สร้างจากการแก้ไขบิลเดิม" hint. Locks the reverse-link
// contract end-to-end through the service layer.
func TestGetByID_CorrectedFromBillID_PopulatedForReplacement(t *testing.T) {
	billID := uuid.New()
	oldID := uuid.New()
	svc, repo, _ := newIsEditedService(editedTestBill(billID), nil)
	repo.findCorrectedFromBillIDFn = func(_ context.Context, q uuid.UUID) (*uuid.UUID, error) {
		if q != billID {
			t.Errorf("FindCorrectedFromBillID query id = %s, want %s", q, billID)
		}
		return &oldID, nil
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CorrectedFromBillID == nil {
		t.Fatal("CorrectedFromBillID = nil, want pointer to the superseded old bill")
	}
	if *got.CorrectedFromBillID != oldID {
		t.Errorf("CorrectedFromBillID = %s, want %s", *got.CorrectedFromBillID, oldID)
	}
}

// Normal bills (not the replacement side of any correction chain) must
// surface CorrectedFromBillID = nil so the FE drawer hides the hint.
// Default mock returns (nil, nil) — locks that GetByID propagates this as
// "no lineage" rather than inventing a value.
func TestGetByID_CorrectedFromBillID_NilForNormalBill(t *testing.T) {
	billID := uuid.New()
	svc, _, _ := newIsEditedService(editedTestBill(billID), nil)

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CorrectedFromBillID != nil {
		t.Errorf("CorrectedFromBillID = %v, want nil (no correction lineage)", got.CorrectedFromBillID)
	}
}

// Reverse-link lookup failure must propagate — a transient DB blip should
// NOT make a replacement bill silently render as a "normal" DRAFT and
// erase the audit context. Mirrors the is_edited failure-propagation lock
// (TestGetByID_IsEdited_FailurePropagates).
func TestGetByID_CorrectedFromBillID_FailurePropagates(t *testing.T) {
	billID := uuid.New()
	svc, repo, _ := newIsEditedService(editedTestBill(billID), nil)
	repo.findCorrectedFromBillIDFn = func(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
		return nil, errors.New("db unreachable")
	}

	got, err := svc.GetByID(context.Background(), billID)
	if err == nil {
		t.Fatal("expected error when FindCorrectedFromBillID fails, got nil")
	}
	if got != nil {
		t.Errorf("response should be nil on error, got %+v", got)
	}
}

// DTO mapping locks that ToBillResponseWithRelations carries the field
// across to the API response. Without this the BE wires up the lookup
// but never hands it to the FE.
func TestToBillResponseWithRelations_CarriesCorrectedFromBillID(t *testing.T) {
	billID := uuid.New()
	oldID := uuid.New()
	wr := BillWithRelations{
		Bill: Bill{
			ID:           billID,
			ContractID:   uuid.New(),
			BillingMonth: "2026-05",
			BillType:     BillTypeMonthly,
			Status:       BillStatusDraft,
		},
		CorrectedFromBillID: &oldID,
	}

	resp := ToBillResponseWithRelations(wr)
	if resp.CorrectedFromBillID == nil {
		t.Fatal("response CorrectedFromBillID = nil, want pointer")
	}
	if *resp.CorrectedFromBillID != oldID {
		t.Errorf("response CorrectedFromBillID = %s, want %s", *resp.CorrectedFromBillID, oldID)
	}
}
