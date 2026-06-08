package billing

import (
	"context"
	"errors"
	"testing"

	"nana/internal/billingreconciliation"
	"nana/internal/meterreading"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Port-level test for the BillsQuerier + BillsCommander adapters. Mirrors the
// signal/noise heuristic in testing-strategy.md: assert on the adapter's
// load-bearing logic (projection mapping, meter lookup, delegation, error
// propagation) and skip everything billing.Service.CreateMonthlyBill already
// tests on its own.

// --- BillsQuerier ---

func TestFindExistingBillsByContractsAndMonth_MapsBillToSnapshot(t *testing.T) {
	contractA := uuid.New()
	contractB := uuid.New()
	billA := uuid.New()
	billB := uuid.New()

	repo := &mockBillingRepo{
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*Bill, error) {
			return map[uuid.UUID]*Bill{
				contractA: {ID: billA, Status: BillStatusFinalized, TotalAmount: 700000},
				contractB: {ID: billB, Status: BillStatusDraft, TotalAmount: 450000},
			}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	got, err := svc.FindExistingBillsByContractsAndMonth(
		context.Background(),
		[]uuid.UUID{contractA, contractB},
		"2026-06",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	wantA := &billingreconciliation.BillSnapshot{BillID: billA, Status: "FINALIZED", TotalAmountSatang: 700000}
	if *got[contractA] != *wantA {
		t.Errorf("contract A snapshot = %+v, want %+v", *got[contractA], *wantA)
	}
	wantB := &billingreconciliation.BillSnapshot{BillID: billB, Status: "DRAFT", TotalAmountSatang: 450000}
	if *got[contractB] != *wantB {
		t.Errorf("contract B snapshot = %+v, want %+v", *got[contractB], *wantB)
	}
}

func TestFindExistingBillsByContractsAndMonth_EmptyShortCircuits(t *testing.T) {
	calls := 0
	repo := &mockBillingRepo{
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*Bill, error) {
			calls++
			return nil, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	got, err := svc.FindExistingBillsByContractsAndMonth(context.Background(), nil, "2026-06")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(got))
	}
	if calls != 0 {
		t.Errorf("expected zero repo calls for empty input, got %d", calls)
	}
}

// --- BillsCommander ---

func TestCreateMonthlyBillForReconciliation_DelegatesToCreateMonthlyBill(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-03")

	repo := &mockBillingRepo{}
	// reading set on the mock so both the adapter's
	// FindMonthlyByRoomsAndMonth lookup AND the underlying CreateMonthlyBill's
	// FindByIDSimple call resolve to the same reading.
	meters := &mockMeterQuerier{
		reading: reading,
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			if len(roomIDs) != 1 || roomIDs[0] != c.RoomID {
				t.Errorf("meter lookup roomIDs = %v, want [%s]", roomIDs, c.RoomID)
			}
			if month != "2026-03" {
				t.Errorf("meter lookup month = %s, want 2026-03", month)
			}
			return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: reading}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{contract: c}, meters, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	got, err := svc.CreateMonthlyBillForReconciliation(context.Background(),
		billingreconciliation.CreateMonthlyBillForReconciliationRequest{
			ContractID:   c.ID,
			BillingMonth: "2026-03",
		}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ID == uuid.Nil {
		t.Fatalf("expected non-nil CreatedBill with non-nil ID, got %+v", got)
	}
	if repo.createdBill == nil {
		t.Fatal("expected underlying CreateMonthlyBill to persist a bill")
	}
	if repo.createdBill.ID != got.ID {
		t.Errorf("CreatedBill.ID = %s, want %s", got.ID, repo.createdBill.ID)
	}
	if repo.createdBill.BillType != BillTypeMonthly {
		t.Errorf("delegated bill type = %s, want MONTHLY", repo.createdBill.BillType)
	}
	if repo.createdBill.Status != BillStatusDraft {
		t.Errorf("delegated bill status = %s, want DRAFT", repo.createdBill.Status)
	}
}

func TestCreateMonthlyBillForReconciliation_MissingMeterReturnsAppError(t *testing.T) {
	c := testContract()
	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBillForReconciliation(context.Background(),
		billingreconciliation.CreateMonthlyBillForReconciliationRequest{
			ContractID:   c.ID,
			BillingMonth: "2026-03",
		}, nil)
	if !errors.Is(err, ErrMeterNotFound) {
		t.Fatalf("expected ErrMeterNotFound, got %v", err)
	}
	if _, ok := respond.Is(err); !ok {
		t.Fatalf("expected AppError so reconciliation service can map → SKIPPED, got plain error %v", err)
	}
}

func TestCreateMonthlyBillForReconciliation_PropagatesContractNotFound(t *testing.T) {
	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{}, // empty → ErrRecordNotFound
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBillForReconciliation(context.Background(),
		billingreconciliation.CreateMonthlyBillForReconciliationRequest{
			ContractID:   uuid.New(),
			BillingMonth: "2026-03",
		}, nil)
	if !errors.Is(err, ErrContractNotFound) {
		t.Fatalf("expected ErrContractNotFound, got %v", err)
	}
}

func TestCreateMonthlyBillForReconciliation_BadBillingMonth(t *testing.T) {
	c := testContract()
	svc := newSvc(&mockBillingRepo{}, &mockContractQuerier{contract: c},
		&mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBillForReconciliation(context.Background(),
		billingreconciliation.CreateMonthlyBillForReconciliationRequest{
			ContractID:   c.ID,
			BillingMonth: "2026-3", // invalid format
		}, nil)
	if err == nil {
		t.Fatalf("expected error for malformed billing_month")
	}
	if _, ok := respond.Is(err); !ok {
		t.Fatalf("expected AppError for validation, got %v", err)
	}
}

func TestCreateMonthlyBillForReconciliation_PropagatesDuplicateBill(t *testing.T) {
	c := testContract()
	reading := testMonthlyReading(c.RoomID, "2026-03")

	repo := &mockBillingRepo{
		findByContractAndMonthFn: func(_ context.Context, _ uuid.UUID, _ string, _ BillType) (*Bill, error) {
			return &Bill{ID: uuid.New()}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: reading,
			findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
				return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: reading}, nil
			},
		},
		&mockConfigQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.CreateMonthlyBillForReconciliation(context.Background(),
		billingreconciliation.CreateMonthlyBillForReconciliationRequest{
			ContractID:   c.ID,
			BillingMonth: "2026-03",
		}, nil)
	if !errors.Is(err, ErrBillAlreadyExists) {
		t.Fatalf("expected ErrBillAlreadyExists (reconciliation maps → ALREADY_BILLED_BY_OTHER), got %v", err)
	}
}

// Compile-time interface checks — guarantees the billing service stays
// wired to the reconciliation ports at the type level, so a rename on
// either side surfaces immediately at build instead of at DI time.
var (
	_ billingreconciliation.BillsQuerier   = (*billingService)(nil)
	_ billingreconciliation.BillsCommander = (*billingService)(nil)
)
