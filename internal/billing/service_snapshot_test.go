package billing

// Group 2 — Snapshot coverage
//
// Verifies that every DRAFT creation path injects payment routing into the
// bill's snapshot fields (PaymentBankName / PaymentAccountNumber /
// PaymentAccountName) when rules are configured, and leaves them nil when no
// rules are configured.
//
// Paths covered:
//   - CreateMonthlyBill (monthly, with and without rules)
//   - CorrectBill (correction re-snapshots from current rules, not old bill)
//
// Settlement snapshot tests (TestGenerateSettlement_SnapshotsPaymentDestination
// + TestRegenerateSettlement_SnapshotsPaymentDestination) migrate alongside
// the settlement workflow in a follow-up commit — see W4 commit 3 message.

import (
	"context"
	"testing"

	"nana/internal/apartment"
	"nana/internal/contract"
	"nana/internal/meterreading"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// mockPaymentRoutingQuerier lets tests control what ResolveDestination returns.
type mockPaymentRoutingQuerier struct {
	dest *apartment.PaymentDestinationInfo
	err  error
}

var _ PaymentRoutingQuerier = (*mockPaymentRoutingQuerier)(nil)

func (m *mockPaymentRoutingQuerier) ResolveDestination(_ context.Context, _ uuid.UUID, _ string) (*apartment.PaymentDestinationInfo, error) {
	return m.dest, m.err
}

// snapshotDest is the test destination injected by routing mocks.
var snapshotDest = &apartment.PaymentDestinationInfo{
	BankName:      "ธนาคารกรุงเทพ",
	AccountNumber: "123456789",
	AccountName:   "นานา รีซอร์ท",
}

// snapshotSvc builds a billing service wired with the given routing querier.
// All other deps satisfy the minimum required for CreateMonthlyBill to succeed.
func snapshotSvc(repo *mockBillingRepo, c *contract.Contract, reading *meterreading.MeterReading, routing *mockPaymentRoutingQuerier) BillingService {
	return NewBillingService(
		repo,
		&mockBillAuditRepo{},
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading},
		&mockConfigQuerier{},
		routing,
		&mockTxManager{},
	)
}

// repoWithFindByID returns a mockBillingRepo whose FindByID and
// LockBillForCorrection return the given bill, and whose
// FindByIDWithRelations returns BillWithRelations wrapping the created bill
// (set after Create() is called).
func repoWithFindByID(existing *Bill) *mockBillingRepo {
	repo := &mockBillingRepo{}
	repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) {
		if existing != nil {
			return existing, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	repo.findByIDWithRelationsFn = func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
		if repo.createdBill != nil {
			return &BillWithRelations{Bill: *repo.createdBill}, nil
		}
		// Correction path: new bill not created yet on first call — return existing.
		if existing != nil {
			return &BillWithRelations{Bill: *existing}, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	return repo
}

// ============================================================
// TestMonthlyBill_SnapshotsPaymentDestination
// ============================================================

func TestMonthlyBill_SnapshotsPaymentDestination(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-06")
	routing := &mockPaymentRoutingQuerier{dest: snapshotDest}
	repo := &mockBillingRepo{}
	repo.findByIDWithRelationsFn = func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
		if repo.createdBill != nil {
			return &BillWithRelations{Bill: *repo.createdBill}, nil
		}
		return nil, gorm.ErrRecordNotFound
	}

	svc := snapshotSvc(repo, c, reading, routing)
	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-06", MeterReadingID: reading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := repo.createdBill
	if got == nil {
		t.Fatal("no bill was created")
	}
	if got.PaymentBankName == nil || *got.PaymentBankName != snapshotDest.BankName {
		t.Errorf("PaymentBankName = %v, want %q", got.PaymentBankName, snapshotDest.BankName)
	}
	if got.PaymentAccountNumber == nil || *got.PaymentAccountNumber != snapshotDest.AccountNumber {
		t.Errorf("PaymentAccountNumber = %v, want %q", got.PaymentAccountNumber, snapshotDest.AccountNumber)
	}
	if got.PaymentAccountName == nil || *got.PaymentAccountName != snapshotDest.AccountName {
		t.Errorf("PaymentAccountName = %v, want %q", got.PaymentAccountName, snapshotDest.AccountName)
	}
}

// ============================================================
// TestMonthlyBill_NullSnapshotWhenNoRules
// ============================================================

func TestMonthlyBill_NullSnapshotWhenNoRules(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-06")
	// Routing returns nil (no rules configured).
	routing := &mockPaymentRoutingQuerier{dest: nil}
	repo := &mockBillingRepo{}
	repo.findByIDWithRelationsFn = func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
		if repo.createdBill != nil {
			return &BillWithRelations{Bill: *repo.createdBill}, nil
		}
		return nil, gorm.ErrRecordNotFound
	}

	svc := snapshotSvc(repo, c, reading, routing)
	_, err := svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: "2026-06", MeterReadingID: reading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := repo.createdBill
	if got == nil {
		t.Fatal("no bill was created")
	}
	if got.PaymentBankName != nil {
		t.Errorf("PaymentBankName = %v, want nil (no rules)", got.PaymentBankName)
	}
	if got.PaymentAccountNumber != nil {
		t.Errorf("PaymentAccountNumber = %v, want nil", got.PaymentAccountNumber)
	}
}

// ============================================================
// TestCorrection_ReSnapshotsFromCurrentRules
// ============================================================

// TestCorrection_ReSnapshotsFromCurrentRules verifies that CorrectBill applies
// the CURRENT routing rules, not the old bill's snapshot. The old bill is set up
// with a different bank name to prove the new bill gets the fresh snapshot.
func TestCorrection_ReSnapshotsFromCurrentRules(t *testing.T) {
	c := testContract()

	// Old bill: FINALIZED MONTHLY, with a DIFFERENT bank name than what routing
	// will now return — correction must NOT carry this over.
	oldBankName := "ธนาคารเก่า"
	oldBill := &Bill{
		ID:                   uuid.New(),
		ContractID:           c.ID,
		BillingMonth:         "2026-05",
		BillType:             BillTypeMonthly,
		Status:               BillStatusFinalized,
		PaymentBankName:      &oldBankName,
		PaymentAccountNumber: strPtr("999"),
		PaymentAccountName:   strPtr("บัญชีเก่า"),
	}
	oldBill.LineItems = []BillLineItem{{
		LineType: LineItemRoomRent,
		Amount:   500000,
		Source:   LineItemSourceAuto,
	}}

	reading := testMonthlyReading(c.RoomID, "2026-05")
	reading.BillingMonth = strPtr("2026-05")

	routing := &mockPaymentRoutingQuerier{dest: snapshotDest} // current routing = snapshotDest

	repo := repoWithFindByID(oldBill)
	// FindMonthlyByRoomsAndMonth must return the reading so correction can recompute.
	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, roomIDs []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: reading}, nil
		},
	}

	svc := NewBillingService(
		repo,
		&mockBillAuditRepo{},
		&mockContractQuerier{contract: c},
		meters,
		&mockConfigQuerier{},
		routing,
		&mockTxManager{},
	)

	_, err := svc.CorrectBill(context.Background(), oldBill.ID,
		CorrectBillRequest{CorrectionReason: "ทดสอบการ re-snapshot"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// repo.createdBill is the NEW corrected bill.
	got := repo.createdBill
	if got == nil {
		t.Fatal("no corrected bill was created")
	}
	if got.PaymentBankName == nil || *got.PaymentBankName != snapshotDest.BankName {
		t.Errorf("PaymentBankName = %v, want %q (current rules)", got.PaymentBankName, snapshotDest.BankName)
	}
	if *got.PaymentBankName == oldBankName {
		t.Errorf("new bill inherited old bank name — re-snapshot did not happen")
	}
}

// strPtr is a convenience helper for *string literals in tests.
func strPtr(s string) *string { return &s }
