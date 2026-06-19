package monthly

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// runBatch invokes the service and returns (batch header, items captured by the mock store).
func runBatch(t *testing.T, svc Service, store *mockStore, month string) (*billing.BillGenerationBatch, []billing.BillGenerationBatchItem) {
	t.Helper()
	result, err := svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: uuid.New().String(), BillingMonth: month,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return result, store.createdBatchItems
}

func TestBatchCreateMonthlyBills_HappyPath(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101")
	cwr2, c2 := testContractWithRoom(2, "201")
	cwr3, c3 := testContractWithRoom(3, "301")

	r1 := testMonthlyReading(c1.RoomID, "2026-03")
	r2 := testMonthlyReading(c2.RoomID, "2026-03")
	r3 := testMonthlyReading(c3.RoomID, "2026-03")

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: r1, c2.RoomID: r2, c3.RoomID: r3,
			}, nil
		},
	}

	svc := batchSvc(store, meters, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.TotalContracts != 3 {
		t.Fatalf("total = %d, want 3", batch.TotalContracts)
	}
	if batch.CreatedCount != 3 {
		t.Fatalf("created = %d, want 3", batch.CreatedCount)
	}
	if batch.Status != billing.BatchStatusCompleted {
		t.Errorf("status = %s, want COMPLETED", batch.Status)
	}
	for _, d := range items {
		if d.ResultType != billing.ResultCreated {
			t.Errorf("room %s: expected CREATED, got %s", d.RoomNumber, d.ResultType)
		}
		if d.BillID != nil {
			t.Errorf("room %s: bill_id should be nil (compute-only)", d.RoomNumber)
		}
		if len(d.ComputedSnapshot.LineItems) != 3 {
			t.Errorf("room %s: expected 3 snapshot line items, got %d",
				d.RoomNumber, len(d.ComputedSnapshot.LineItems))
		}
		if d.ComputedSnapshot.TotalAmount != 638000 {
			t.Errorf("room %s: snapshot total = %d, want 638000",
				d.RoomNumber, d.ComputedSnapshot.TotalAmount)
		}
		if d.ComputedSnapshot.Version != billing.ComputedSnapshotVersion {
			t.Errorf("room %s: snapshot version = %d, want %d",
				d.RoomNumber, d.ComputedSnapshot.Version, billing.ComputedSnapshotVersion)
		}
	}
	if store.createdBill != nil {
		t.Error("batch should not persist any bill row (compute-only)")
	}
}

func TestBatchCreateMonthlyBills_MixedResults(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101") // will have meter → created
	cwr2, c2 := testContractWithRoom(2, "201") // existing bill
	cwr3, _ := testContractWithRoom(3, "301")  // no meter → skipped

	r1 := testMonthlyReading(c1.RoomID, "2026-03")
	existingBillID := uuid.New()

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*billing.Bill, error) {
			return map[uuid.UUID]*billing.Bill{
				c2.ID: {ID: existingBillID},
			}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: r1,
				c2.RoomID: testMonthlyReading(c2.RoomID, "2026-03"),
			}, nil
		},
	}

	svc := batchSvc(store, meters, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.CreatedCount != 1 {
		t.Errorf("created = %d, want 1", batch.CreatedCount)
	}
	if batch.AlreadyExistsCount != 1 {
		t.Errorf("already_exists = %d, want 1", batch.AlreadyExistsCount)
	}
	if batch.SkippedCount != 1 {
		t.Errorf("skipped = %d, want 1", batch.SkippedCount)
	}
	if batch.Status != billing.BatchStatusPartial {
		t.Errorf("status = %s, want PARTIAL", batch.Status)
	}
	for _, d := range items {
		if d.ResultType == billing.ResultAlreadyExists && (d.BillID == nil || *d.BillID != existingBillID) {
			t.Error("already_exists result should have correct bill_id")
		}
		if d.ResultType == billing.ResultSkipped && d.ReasonCode != billing.ReasonMissingMeterReading {
			t.Errorf("skipped reason = %s, want MISSING_METER_READING", d.ReasonCode)
		}
	}
}

func TestBatchCreateMonthlyBills_MoveOutPending(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr}, nil
		},
	}

	moveOuts := &mockMoveOutQuerier{
		findRoomIDsWithMoveOutInMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
			return map[uuid.UUID]bool{cwr.RoomID: true}, nil
		},
	}

	svc := batchSvc(store, &mockMeterQuerier{}, moveOuts)
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != billing.ReasonMoveOutPending {
		t.Errorf("reason = %s, want MOVE_OUT_PENDING", items[0].ReasonCode)
	}
}

// Move-out scheduled NEXT month must not skip the current-month billing.
// Tenant is still in the room for billing_month and must receive the normal
// monthly bill; settlement will replace the next-month bill instead.
func TestBatchCreateMonthlyBills_MoveOutNextMonth_StillBills(t *testing.T) {
	cwr, c := testContractWithRoom(1, "101")
	r := testMonthlyReading(c.RoomID, "2026-03")

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr}, nil
		},
	}

	// Repo filters by month, so the next-month notice simply doesn't appear
	// in the map returned for the current billing month — mock that contract.
	moveOuts := &mockMoveOutQuerier{
		findRoomIDsWithMoveOutInMonthFn: func(_ context.Context, _ []uuid.UUID, billingMonth string) (map[uuid.UUID]bool, error) {
			if billingMonth == "2026-03" {
				return map[uuid.UUID]bool{}, nil // not skipped for current month
			}
			return map[uuid.UUID]bool{cwr.RoomID: true}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: r}, nil
		},
	}

	svc := batchSvc(store, meters, moveOuts)
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.CreatedCount != 1 {
		t.Fatalf("created = %d, want 1 (next-month move-out must not block this month's bill)", batch.CreatedCount)
	}
	if batch.SkippedCount != 0 {
		t.Errorf("skipped = %d, want 0", batch.SkippedCount)
	}
	if items[0].ResultType != billing.ResultCreated {
		t.Errorf("result = %s, want CREATED", items[0].ResultType)
	}
}

func TestBatchCreateMonthlyBills_NotBillable_StartAfterMonth(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")
	cwr.StartDate = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // starts May, billing March

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr}, nil
		},
	}

	svc := batchSvc(store, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != billing.ReasonNotBillable {
		t.Errorf("reason = %s, want NOT_BILLABLE", items[0].ReasonCode)
	}
}

func TestBatchCreateMonthlyBills_NotBillable_EndedBeforeMonth(t *testing.T) {
	cwr, _ := testContractWithRoom(1, "101")
	endDate := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) // ended Feb, billing March
	cwr.EndDate = &endDate

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr}, nil
		},
	}

	svc := batchSvc(store, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, items := runBatch(t, svc, store, "2026-03")

	if batch.SkippedCount != 1 {
		t.Fatalf("skipped = %d, want 1", batch.SkippedCount)
	}
	if items[0].ReasonCode != billing.ReasonNotBillable {
		t.Errorf("reason = %s, want NOT_BILLABLE", items[0].ReasonCode)
	}
}

func TestBatchCreateMonthlyBills_EmptyApartment(t *testing.T) {
	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return nil, nil
		},
	}

	svc := batchSvc(store, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	batch, _ := runBatch(t, svc, store, "2026-03")

	if batch.TotalContracts != 0 {
		t.Fatalf("total = %d, want 0", batch.TotalContracts)
	}
}

func TestBatchCreateMonthlyBills_InvalidInput(t *testing.T) {
	svc := batchSvc(&mockStore{}, &mockMeterQuerier{}, &mockMoveOutQuerier{})

	_, err := svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: "bad-uuid", BillingMonth: "2026-03",
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}

	_, err = svc.BatchCreateMonthlyBills(context.Background(), BatchCreateMonthlyBillsRequest{
		ApartmentID: uuid.New().String(), BillingMonth: "2026-1",
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid month format")
	}
}

func TestBatchCreateMonthlyBills_DeterministicOrdering(t *testing.T) {
	cwr3, _ := testContractWithRoom(3, "301")
	cwr1, _ := testContractWithRoom(1, "101")
	cwr2, _ := testContractWithRoom(2, "201")
	_ = cwr3

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			// repo returns sorted by floor, room_number
			return []billing.ContractWithRoom{cwr1, cwr2, cwr3}, nil
		},
	}

	svc := batchSvc(store, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	_, items := runBatch(t, svc, store, "2026-03")

	if len(items) != 3 {
		t.Fatalf("items count = %d, want 3", len(items))
	}
	if items[0].RoomNumber != "101" || items[1].RoomNumber != "201" || items[2].RoomNumber != "301" {
		t.Errorf("order = %s,%s,%s — want 101,201,301",
			items[0].RoomNumber, items[1].RoomNumber, items[2].RoomNumber)
	}
}

func TestBatchCreateMonthlyBills_IdempotentRerun(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101")
	cwr2, c2 := testContractWithRoom(2, "201")
	bill1ID, bill2ID := uuid.New(), uuid.New()

	store := &mockStore{
		findActiveContractsByApartmentFn: func(_ context.Context, _ uuid.UUID) ([]billing.ContractWithRoom, error) {
			return []billing.ContractWithRoom{cwr1, cwr2}, nil
		},
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*billing.Bill, error) {
			return map[uuid.UUID]*billing.Bill{
				c1.ID: {ID: bill1ID},
				c2.ID: {ID: bill2ID},
			}, nil
		},
	}

	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: testMonthlyReading(c1.RoomID, "2026-03"),
				c2.RoomID: testMonthlyReading(c2.RoomID, "2026-03"),
			}, nil
		},
	}

	svc := batchSvc(store, meters, &mockMoveOutQuerier{})
	batch, _ := runBatch(t, svc, store, "2026-03")

	if batch.CreatedCount != 0 {
		t.Errorf("created = %d, want 0 (idempotent)", batch.CreatedCount)
	}
	if batch.AlreadyExistsCount != 2 {
		t.Errorf("already_exists = %d, want 2", batch.AlreadyExistsCount)
	}
	if batch.Status != billing.BatchStatusCompleted {
		t.Errorf("status = %s, want COMPLETED", batch.Status)
	}
}
