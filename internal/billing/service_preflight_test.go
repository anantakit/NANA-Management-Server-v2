package billing

import (
	"context"
	"testing"
	"time"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// --- Domain test: classifyContractForBatch (pure function) ---
//
// Mandatory per testing-strategy.md — pure function with all batch / preflight
// logic. If the rule changes silently, this test catches it.

func TestClassifyContractForBatch(t *testing.T) {
	roomID := uuid.New()
	contractID := uuid.New()
	existingBillID := uuid.New()

	startOfMonth := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	base := ContractWithRoom{
		ContractID: contractID,
		RoomID:     roomID,
		StartDate:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	withMeter := map[uuid.UUID]*meterreading.MeterReading{roomID: {}}
	withExisting := map[uuid.UUID]*Bill{contractID: {ID: existingBillID}}

	cases := []struct {
		name        string
		mutate      func(c ContractWithRoom) ContractWithRoom
		moveOuts    map[uuid.UUID]bool
		meters      map[uuid.UUID]*meterreading.MeterReading
		existing    map[uuid.UUID]*Bill
		wantResult  ResultType
		wantReason  string
		wantBillSet bool
	}{
		{
			name:       "ready: meter present, no existing, no pending",
			meters:     withMeter,
			wantResult: ResultCreated,
		},
		{
			name:       "move-out pending wins over meter + existing bill",
			moveOuts:   map[uuid.UUID]bool{roomID: true},
			meters:     withMeter,
			existing:   withExisting,
			wantResult: ResultSkipped,
			wantReason: ReasonMoveOutPending,
		},
		{
			name: "not billable: contract starts after billing month",
			mutate: func(c ContractWithRoom) ContractWithRoom {
				c.StartDate = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
				return c
			},
			meters:     withMeter,
			wantResult: ResultSkipped,
			wantReason: ReasonNotBillable,
		},
		{
			name: "not billable: contract ended before billing month",
			mutate: func(c ContractWithRoom) ContractWithRoom {
				end := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
				c.EndDate = &end
				return c
			},
			meters:     withMeter,
			wantResult: ResultSkipped,
			wantReason: ReasonNotBillable,
		},
		{
			name:       "missing meter for billable contract",
			wantResult: ResultSkipped,
			wantReason: ReasonMissingMeterReading,
		},
		{
			name:        "already exists when bill present for contract",
			meters:      withMeter,
			existing:    withExisting,
			wantResult:  ResultAlreadyExists,
			wantReason:  ReasonAlreadyExists,
			wantBillSet: true,
		},
		{
			name:       "billability check runs before meter check (no meter + start in future → NOT_BILLABLE)",
			mutate:     func(c ContractWithRoom) ContractWithRoom { c.StartDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC); return c },
			wantResult: ResultSkipped,
			wantReason: ReasonNotBillable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			if tc.mutate != nil {
				c = tc.mutate(c)
			}
			moveOuts := tc.moveOuts
			if moveOuts == nil {
				moveOuts = map[uuid.UUID]bool{}
			}
			meters := tc.meters
			if meters == nil {
				meters = map[uuid.UUID]*meterreading.MeterReading{}
			}
			existing := tc.existing
			if existing == nil {
				existing = map[uuid.UUID]*Bill{}
			}

			got := classifyContractForBatch(c, startOfMonth, endOfMonth, moveOuts, meters, existing)
			if got.ResultType != tc.wantResult {
				t.Errorf("ResultType = %s, want %s", got.ResultType, tc.wantResult)
			}
			if got.ReasonCode != tc.wantReason {
				t.Errorf("ReasonCode = %q, want %q", got.ReasonCode, tc.wantReason)
			}
			if tc.wantBillSet {
				if got.BillID == nil || *got.BillID != existingBillID {
					t.Errorf("BillID = %v, want %v", got.BillID, existingBillID)
				}
			} else if got.BillID != nil {
				t.Errorf("BillID = %v, want nil", got.BillID)
			}
		})
	}
}

// --- Service test: PreflightMonthly happy path ---
//
// One scenario covers all 5 buckets in a single run — verifies counts +
// no-persistence invariant. Validation errors are skipped per testing-strategy
// (handler/validator catches).

func TestPreflightMonthly_HappyPath_AllBuckets(t *testing.T) {
	cwr1, c1 := testContractWithRoom(1, "101") // ready
	cwr2, c2 := testContractWithRoom(2, "201") // already exists
	cwr3, _ := testContractWithRoom(3, "301")  // missing meter
	cwr4, _ := testContractWithRoom(4, "401")  // move-out pending
	cwr5, _ := testContractWithRoom(5, "501")
	cwr5.StartDate = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // not billable

	r1 := testMonthlyReading(c1.RoomID, "2026-03")
	r2 := testMonthlyReading(c2.RoomID, "2026-03")
	existingBillID := uuid.New()

	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{cwr1, cwr2, cwr3, cwr4, cwr5}, nil
		},
		findExistingByContractsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*Bill, error) {
			return map[uuid.UUID]*Bill{c2.ID: {ID: existingBillID}}, nil
		},
	}
	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{
				c1.RoomID: r1,
				c2.RoomID: r2,
			}, nil
		},
	}
	moveOuts := &mockMoveOutQuerier{
		findRoomIDsWithMoveOutInMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]bool, error) {
			return map[uuid.UUID]bool{cwr4.RoomID: true}, nil
		},
	}

	svc := batchSvc(repo, meters, moveOuts)
	result, err := svc.PreflightMonthly(context.Background(), MonthlyPreflightRequest{
		ApartmentID:  uuid.New().String(),
		BillingMonth: "2026-03",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalRooms != 5 {
		t.Errorf("total_rooms = %d, want 5", result.TotalRooms)
	}
	if result.ReadyCount != 1 {
		t.Errorf("ready = %d, want 1", result.ReadyCount)
	}
	if result.AlreadyExistsCount != 1 {
		t.Errorf("already_exists = %d, want 1", result.AlreadyExistsCount)
	}
	if result.MissingMeterCount != 1 {
		t.Errorf("missing_meter = %d, want 1", result.MissingMeterCount)
	}
	if result.MoveOutPendingCount != 1 {
		t.Errorf("move_out_pending = %d, want 1", result.MoveOutPendingCount)
	}
	if result.NotBillableCount != 1 {
		t.Errorf("not_billable = %d, want 1", result.NotBillableCount)
	}

	// Preflight must NOT persist anything — the whole point of the endpoint
	// is that the user gets to decide before committing.
	if repo.createdBatch != nil {
		t.Error("preflight created a batch row — should be read-only")
	}
	if repo.createdBill != nil {
		t.Error("preflight created a bill — should be read-only")
	}
}

func TestPreflightMonthly_EmptyApartment(t *testing.T) {
	repo := &mockBillingRepo{
		findActiveContractsByApartmentIDFn: func(_ context.Context, _ uuid.UUID) ([]ContractWithRoom, error) {
			return []ContractWithRoom{}, nil
		},
	}
	svc := batchSvc(repo, &mockMeterQuerier{}, &mockMoveOutQuerier{})
	result, err := svc.PreflightMonthly(context.Background(), MonthlyPreflightRequest{
		ApartmentID:  uuid.New().String(),
		BillingMonth: "2026-03",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalRooms != 0 || result.ReadyCount != 0 {
		t.Errorf("empty apartment should return zeros, got total=%d ready=%d", result.TotalRooms, result.ReadyCount)
	}
}
