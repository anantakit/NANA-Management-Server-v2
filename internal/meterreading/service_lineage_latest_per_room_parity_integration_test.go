//go:build integration

// External test package — see service_replacement_lineage_integration_test.go
// for the import-cycle rationale.
package meterreading_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestLineage_FindLatestPerRoomMatchesFindLatestByRoomID pins the BATCH lineage
// query to its single-room twin.
//
//	GUARD: feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//	FIXES: the monthly entry path showed `previous = 0` for a room whose latest
//	       reading was an EXIT row, because the frontend derived `previous` from
//	       two MONTH-SCOPED queries while BatchCreate validates against
//	       FindLatestByRoomID (which orders over ALL rows). The operator entered
//	       a value on a baseline of 0 and the save was rejected.
//
// FindLatestPerRoom exists ONLY to serve that path with the same truth, so the
// invariant worth pinning is agreement, not a hand-written expectation:
//
//	∀ room: FindLatestPerRoom(rooms)[room] == FindLatestByRoomID(room)
//
// The room set below is chosen so each row covers a way the two queries could
// diverge:
//
//	R1  plain MONTHLY chain            — the ordinary case
//	R2  EXIT is the latest row         — billing_month IS NULL; the actual defect
//	R3  EXIT older than a later MONTHLY— temporal ordering must not just sort NULLs
//	R4  PHYSICAL_REPLACEMENT anchor    — an anchor IS the next baseline
//	R5  READING_RECOVERY anchor        — must NOT be filtered as "analytics"
//	R6  no readings at all             — absent from the map, never a zero row
//
// If someone copies FindRecentByRoomIDs' `anchor_reason IS NULL` /
// recovery-source exclusions into FindLatestPerRoom by reflex, R4 and R5 fail
// immediately — which is the whole point of writing this as a parity assertion.
func TestLineage_FindLatestPerRoomMatchesFindLatestByRoomID(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	txMgr := database.NewTxManager(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(
		meterRepo,
		room.NewRoomRepository(db),
		contract.NewContractRepository(db),
		moveout.NewMoveOutRepository(db),
		fixtures.NoopBillingApplicationChecker{},
		txMgr,
	)

	newRoom := func(number string) *room.Room {
		rm := fixtures.SeedRoom(t, db, apt.ID.String(), number)
		tn := fixtures.SeedTenant(t, db)
		_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)
		return rm
	}
	monthly := func(rm *room.Room, month string, elec, water int) *meterreading.MeterReadingWithRoom {
		t.Helper()
		got, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
			RoomID:             rm.ID.String(),
			BillingMonth:       month,
			ElectricityCurrent: elec,
			WaterCurrent:       water,
		})
		if err != nil {
			t.Fatalf("create monthly %s for %s: %v", month, rm.Number, err)
		}
		return got
	}

	// R1 — plain MONTHLY chain.
	r1 := newRoom("L-101")
	monthly(r1, "2026-03", 100, 30)
	r1Latest := monthly(r1, "2026-04", 150, 40)

	// R2 — the defect: a room whose ONLY reading is an EXIT row. billing_month
	// is NULL, so no month-scoped query can ever see it.
	r2 := newRoom("L-102")
	r2Exit := fixtures.SeedExitMeter(t, db, r2.ID.String(), time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), 120, 15)

	// R3 — an EXIT row that is NOT the latest. Guards against "NULL sorts last"
	// accidentally passing R2: temporalDateExpr must date the EXIT by
	// reading_date_actual and still rank the later MONTHLY above it.
	r3 := newRoom("L-103")
	fixtures.SeedExitMeter(t, db, r3.ID.String(), time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), 50, 5)
	r3Latest := monthly(r3, "2026-05", 2000, 200)

	// R4 — a PHYSICAL_REPLACEMENT anchor is the baseline the next reading
	// starts from, so it must win over the MONTHLY that precedes it.
	r4 := newRoom("L-104")
	monthly(r4, "2026-03", 800, 80)
	oldFinal, newInitial := 900, 0
	r4Anchor, err := meterSvc.CreateMeterReplacement(ctx, apt.ID, meterreading.CreateReplacementRequest{
		RoomID:          r4.ID.String(),
		ReplacementDate: "2026-04-10",
		Note:            "มิเตอร์ไฟเสีย เปลี่ยนใหม่",
		Electricity: meterreading.ReplacementUtilityRequest{
			Replaced:   true,
			OldFinal:   &oldFinal,
			NewInitial: &newInitial,
		},
	})
	if err != nil {
		t.Fatalf("create replacement for %s: %v", r4.Number, err)
	}

	// R5 — a READING_RECOVERY anchor: the row the analytics pool deliberately
	// EXCLUDES, and that lineage must therefore still return.
	r5 := newRoom("L-105")
	r5Source := monthly(r5, "2026-03", 500, 50)
	recoveryReason := meterreading.AnchorReasonReadingRecovery
	recoveryNote := "เดือนมีนาคมจดเกิน — re-anchor"
	r5SourceID := r5Source.ID
	r5AnchorMonth := "2026-04"
	r5Anchor := &meterreading.MeterReading{
		RoomID:                  r5.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &r5AnchorMonth,
		ElectricityPrevious:     420,
		ElectricityCurrent:      420,
		WaterPrevious:           50,
		WaterCurrent:            50,
		AnchorReason:            &recoveryReason,
		AnchorNote:              &recoveryNote,
		RecoverySourceReadingID: &r5SourceID,
	}
	if err := meterRepo.Create(ctx, r5Anchor); err != nil {
		t.Fatalf("seed recovery anchor for %s: %v", r5.Number, err)
	}

	// R6 — no readings at all.
	r6 := newRoom("L-106")

	// ── The parity assertion ──
	roomIDs := []uuid.UUID{r1.ID, r2.ID, r3.ID, r4.ID, r5.ID, r6.ID}
	batch, err := meterRepo.FindLatestPerRoom(ctx, roomIDs)
	if err != nil {
		t.Fatalf("FindLatestPerRoom: %v", err)
	}

	for _, rm := range []*room.Room{r1, r2, r3, r4, r5, r6} {
		single, singleErr := meterRepo.FindLatestByRoomID(ctx, rm.ID)
		got, inBatch := batch[rm.ID]

		if singleErr != nil {
			// No reading for this room — the batch must omit it rather than
			// hand the entry path a zero-valued baseline.
			if inBatch {
				t.Errorf("%s: FindLatestByRoomID found nothing but batch returned %v — a phantom baseline of 0 is exactly the bug being fixed", rm.Number, got.ID)
			}
			continue
		}
		if !inBatch {
			t.Errorf("%s: batch omitted the room, FindLatestByRoomID returned %v", rm.Number, single.ID)
			continue
		}
		if got.ID != single.ID {
			t.Errorf("%s: batch latest = %v, single latest = %v — the two lineage queries disagree", rm.Number, got.ID, single.ID)
			continue
		}
		// The baseline VALUES are what the entry path renders, so pin them too:
		// agreeing on the row id while projecting different currents would
		// still mislead the operator.
		if got.ElectricityCurrent != single.ElectricityCurrent || got.WaterCurrent != single.WaterCurrent {
			t.Errorf("%s: batch baseline = (elec %d, water %d), single = (elec %d, water %d)",
				rm.Number, got.ElectricityCurrent, got.WaterCurrent, single.ElectricityCurrent, single.WaterCurrent)
		}
	}

	// ── Named expectations, so a failure says WHICH shape regressed ──
	if got := batch[r1.ID]; got.ID != r1Latest.ID {
		t.Errorf("R1 plain chain: got %v, want the later MONTHLY %v", got.ID, r1Latest.ID)
	}
	if got, ok := batch[r2.ID]; !ok || got.ID != r2Exit.ID {
		t.Errorf("R2 EXIT-only: got %v (present=%v), want the EXIT row %v — this is the reported defect", got.ID, ok, r2Exit.ID)
	}
	if got := batch[r3.ID]; got.ID != r3Latest.ID {
		t.Errorf("R3 EXIT then MONTHLY: got %v, want the later MONTHLY %v — EXIT must be dated by reading_date_actual, not sorted last", got.ID, r3Latest.ID)
	}
	if got := batch[r4.ID]; got.ID != r4Anchor.ID {
		t.Errorf("R4 replacement anchor: got %v, want the anchor %v — an anchor IS the next baseline", got.ID, r4Anchor.ID)
	}
	if got := batch[r5.ID]; got.ID != r5Anchor.ID {
		t.Errorf("R5 recovery anchor: got %v, want the anchor %v — lineage must see what analytics excludes", got.ID, r5Anchor.ID)
	}
	if got, ok := batch[r6.ID]; ok {
		t.Errorf("R6 no readings: batch returned %v, want absent", got.ID)
	}
}
