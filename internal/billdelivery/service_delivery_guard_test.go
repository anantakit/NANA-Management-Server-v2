package billdelivery

import (
	"context"
	"testing"

	"nana/internal/billing"

	"github.com/google/uuid"
)

// --- minimal mocks ---

type mockBillQuerier struct {
	bill *billing.Bill
	err  error
}

func (m *mockBillQuerier) FindByID(_ context.Context, _ uuid.UUID) (*billing.Bill, error) {
	return m.bill, m.err
}

var _ BillQuerier = (*mockBillQuerier)(nil)

type mockDeliveryRepo struct{}

func (m *mockDeliveryRepo) Create(_ context.Context, _ *BillDelivery) error { return nil }

var _ DeliveryRepository = (*mockDeliveryRepo)(nil)

// newSvc builds a delivery service backed by the given bill stub.
func newSvc(bill *billing.Bill) DeliveryService {
	return NewDeliveryService(&mockDeliveryRepo{}, &mockBillQuerier{bill: bill})
}

// ============================================================
// Group 4 — Delivery guard
// ============================================================

// TestDeliveryService_RecordDelivery_Accepts_ValidBill is the happy path:
// finalized monthly bill with a payment destination snapshot → delivery succeeds.
func TestDeliveryService_RecordDelivery_Accepts_ValidBill(t *testing.T) {
	bankName := "ธนาคารกรุงเทพ"
	acctNum := "123456789"
	acctName := "นานา รีซอร์ท"
	bill := &billing.Bill{
		ID:                   uuid.New(),
		BillType:             billing.BillTypeMonthly,
		Status:               billing.BillStatusFinalized,
		PaymentBankName:      &bankName,
		PaymentAccountNumber: &acctNum,
		PaymentAccountName:   &acctName,
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err != nil {
		t.Fatalf("expected no error for valid deliverable bill, got: %v", err)
	}
}

// TestDeliveryService_RecordDelivery_Accepts_NullDestination verifies that a
// finalized monthly bill without a payment snapshot is still deliverable.
// Missing payment destination is a signal (operator warning), not a lifecycle
// blocker — the bill amount and content are valid regardless of routing config.
func TestDeliveryService_RecordDelivery_Accepts_NullDestination(t *testing.T) {
	bill := &billing.Bill{
		ID:       uuid.New(),
		BillType: billing.BillTypeMonthly,
		Status:   billing.BillStatusFinalized,
		// PaymentBankName = nil → no destination snapshot; delivery still allowed
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err != nil {
		t.Fatalf("expected no error for finalized bill with null destination, got: %v", err)
	}
}

// TestDeliveryService_RecordDelivery_Rejects_Draft ensures draft bills are
// still rejected — IsDeliverable (MONTHLY+FINALIZED) remains the only gate.
func TestDeliveryService_RecordDelivery_Rejects_Draft(t *testing.T) {
	bill := &billing.Bill{
		ID:       uuid.New(),
		BillType: billing.BillTypeMonthly,
		Status:   billing.BillStatusDraft,
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err == nil {
		t.Fatal("expected ErrBillNotDeliverable for draft bill, got nil")
	}
	if err != ErrBillNotDeliverable {
		t.Errorf("expected ErrBillNotDeliverable, got: %v", err)
	}
}

// TestDeliveryService_RecordDelivery_Rejects_Settlement verifies that
// settlement bills are rejected even when finalized (wrong type).
func TestDeliveryService_RecordDelivery_Rejects_Settlement(t *testing.T) {
	bill := &billing.Bill{
		ID:       uuid.New(),
		BillType: billing.BillTypeSettlement,
		Status:   billing.BillStatusFinalized,
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err == nil {
		t.Fatal("expected ErrBillNotDeliverable for settlement bill, got nil")
	}
	if err != ErrBillNotDeliverable {
		t.Errorf("expected ErrBillNotDeliverable, got: %v", err)
	}
}

// TestDeliveryService_RecordDelivery_Rejects_Paid verifies that an already-paid
// monthly bill cannot receive another delivery record.
func TestDeliveryService_RecordDelivery_Rejects_Paid(t *testing.T) {
	bill := &billing.Bill{
		ID:       uuid.New(),
		BillType: billing.BillTypeMonthly,
		Status:   billing.BillStatusPaid,
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err == nil {
		t.Fatal("expected ErrBillNotDeliverable for paid bill, got nil")
	}
	if err != ErrBillNotDeliverable {
		t.Errorf("expected ErrBillNotDeliverable, got: %v", err)
	}
}

// TestDeliveryService_RecordDelivery_Rejects_Void verifies that a voided
// monthly bill cannot be delivered.
func TestDeliveryService_RecordDelivery_Rejects_Void(t *testing.T) {
	bill := &billing.Bill{
		ID:       uuid.New(),
		BillType: billing.BillTypeMonthly,
		Status:   billing.BillStatusVoid,
	}

	svc := newSvc(bill)
	_, err := svc.RecordDelivery(context.Background(), RecordDeliveryRequest{BillID: bill.ID.String()}, nil)
	if err == nil {
		t.Fatal("expected ErrBillNotDeliverable for void bill, got nil")
	}
	if err != ErrBillNotDeliverable {
		t.Errorf("expected ErrBillNotDeliverable, got: %v", err)
	}
}
