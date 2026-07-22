//go:build integration

// External test package — see service_replacement_lineage_integration_test.go
// for the import-cycle rationale (billing imports meterreading for
// ComputeMonthlyBillSnapshot).
package meterreading_test

import (
	"context"
	"testing"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRollover_NextMonthUsesNewLowValue is the rollover-side pair of
// TestReplacement_NextMonthInheritsNewMeterStart. Together they lock the
// two distinct write-time meter events with deliberately asymmetric shapes:
//
//	Replacement:  Month N    prev = 0           (force reset)
//	              Month N+1  prev = Month N curr (continue on new meter)
//
//	Rollover:     Month N    prev = old high    (preserve — formula needs it)
//	              Month N+1  prev = Month N curr (continue on new low value)
//
// Critical asymmetry: rollover PRESERVES the old high `previous` so the
// (digitMax(prev) - prev) + curr formula computes the right wrap-around
// usage for the rollover month. Then for Month N+1, populatePrevious()
// reads `latest.Current` (the new LOW value), not `latest.Previous`
// (the old high). If a refactor swapped that lookup, Month N+1 would
// re-anchor on 9970 and either fail validation (80 < 9970) or compute
// a huge negative usage that ships an obviously wrong bill.
//
// Scenario (near 4-digit elec meter max, 3-digit water meter max):
//
//	Month N-1 (2026-03): MONTHLY            elec curr 9970 / water curr 990
//	Month N   (2026-04): MONTHLY + rollover elec curr   20 / water curr   5
//	    elec usage  = (9999 - 9970) + 20 = 49   ← wrap formula
//	    water usage = ( 999 -  990) +  5 = 14   ← wrap formula
//	Month N+1 (2026-05): MONTHLY            elec curr   80 / water curr  25
//	    prev MUST = 20 / 5 (Month N curr, the new low)
//	    elec usage = 60 / water usage = 20      ← normal subtraction
//
// What this guards against:
//   - populatePrevious() swapped from latest.Current to latest.Previous —
//     Month N+1 would anchor on 9970 and over-bill the tenant by ~9890 units.
//   - digitMax() inference breaking on a digit-boundary number. Elec's
//     digitMax(9970)=9999 (4-digit) and water's digitMax(990)=999 (3-digit)
//     exercise two independent digit lengths in one test, so a bug that
//     hardcoded one max only breaks one side and gets caught.
//   - is_rollover_* flag failing to persist on the row — the read-time
//     formula at ElectricityUsed() / WaterUsed() depends on the flag, so
//     losing it on persist would silently switch the row to subtraction
//     and compute usage = 20 - 9970 = -9950.
//   - Rollover flag becoming "sticky" so Month N+1's normal subtraction
//     accidentally falls into the rollover branch — caught by the
//     Month N+1 flag asserts.
func TestRollover_NextMonthUsesNewLowValue(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "W-404")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 4)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingApplicationChecker{}, txMgr)

	// ── Month N-1 (2026-03): normal MONTHLY near meter max ──
	// First reading → previous=0; usage = current. Realistic interpretation:
	// a long-lived meter installed before the system started tracking
	// readings, now sitting just below its rollover boundary.
	n1, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-03",
		ElectricityCurrent: 9970,
		WaterCurrent:       990,
	})
	if err != nil {
		t.Fatalf("create Month N-1: %v", err)
	}
	if n1.ElectricityCurrent != 9970 || n1.WaterCurrent != 990 {
		t.Fatalf("Month N-1 unexpected currents: elec=%d water=%d", n1.ElectricityCurrent, n1.WaterCurrent)
	}

	// ── Month N (2026-04): MONTHLY with rollover on BOTH meters ──
	mn, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:                     rm.ID.String(),
		BillingMonth:               "2026-04",
		ElectricityCurrent:         20,
		WaterCurrent:               5,
		IsElectricityMeterRollover: true,
		IsWaterMeterRollover:       true,
	})
	if err != nil {
		t.Fatalf("create Month N (rollover): %v", err)
	}

	// ── Assert 1: rollover PRESERVES previous (the asymmetric half) ──
	// Replacement resets to 0; rollover preserves the old high value because
	// the (digitMax - prev) + curr formula needs it. If this returned 0
	// mid-stream, the rollover formula would compute (0-0)+20=20 instead of
	// 49 — under-bill by 29 units. Worse, a refactor that confused the two
	// flags could swap rollover behavior with replacement.
	if mn.ElectricityPrevious != 9970 {
		t.Errorf("Month N elec previous = %d, want 9970 (rollover PRESERVES old high, unlike replacement)", mn.ElectricityPrevious)
	}
	if mn.WaterPrevious != 990 {
		t.Errorf("Month N water previous = %d, want 990 (rollover PRESERVES old high, unlike replacement)", mn.WaterPrevious)
	}

	// ── Assert 2: rollover flags PERSIST on the row ──
	// Distinguishes rollover from replacement at the persistence layer:
	// rollover IS a column (migration 00007); replacement is NOT (see
	// service_replacement_lineage_integration_test.go). The read-time
	// ElectricityUsed() / WaterUsed() formula branches on these flags,
	// so losing them on persist would silently switch the row to
	// subtraction → usage = 20 - 9970 = -9950 (almost certainly visible
	// as a 500 or a wildly negative bill total).
	if !mn.IsRolloverElectricity {
		t.Error("Month N is_rollover_electricity = false, want true (must persist for read-time formula)")
	}
	if !mn.IsRolloverWater {
		t.Error("Month N is_rollover_water = false, want true (must persist for read-time formula)")
	}

	// ── Assert 3: rollover usage formula on TWO independent digit lengths ──
	// elec: digitMax(9970)=9999 → usage = (9999-9970)+20 = 49
	// water: digitMax( 990)= 999 → usage = ( 999- 990)+ 5 = 14
	// Asserting both ensures digitMax inference holds across the 4-digit /
	// 3-digit boundary, not just one.
	if got := mn.ElectricityUsed(); got != 49 {
		t.Errorf("Month N elec usage = %d, want 49 (= (9999-9970)+20 wrap formula)", got)
	}
	if got := mn.WaterUsed(); got != 14 {
		t.Errorf("Month N water usage = %d, want 14 (= (999-990)+5 wrap formula)", got)
	}

	// ── Assert 4: Month N snapshot ──
	//   300_000 + 49*800 + 14*1800 = 300_000 + 39_200 + 25_200 = 364_400 satang
	snapN := billing.ComputeMonthlyBillSnapshot(
		"2026-04",
		c.MonthlyRent,
		c.ElectricityRatePerUnit,
		c.WaterRatePerUnit,
		&mn.MeterReading,
		nil,
		nil,
	)
	if snapN.TotalAmount != 364_400 {
		t.Errorf("Month N (rollover) snapshot total = %d, want 364400 (rent + 49*800 + 14*1800)", snapN.TotalAmount)
	}
	elecQtyN, waterQtyN := pickLineQuantities(t, "Month N", snapN.LineItems)
	if elecQtyN != 49 {
		t.Errorf("Month N elec quantity = %d, want 49 (rollover wrap)", elecQtyN)
	}
	if waterQtyN != 14 {
		t.Errorf("Month N water quantity = %d, want 14 (rollover wrap)", waterQtyN)
	}

	// ── Pre-flight: lineage query must return Month N (the rollover row),
	//                not Month N-1. If a refactor filters rollover rows out
	//                of FindLatestByRoomID, this catches it before the
	//                next-month-inheritance asserts even fire. ──
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after Month N: %v", err)
	}
	if latest.ID != mn.ID {
		t.Errorf("FindLatestByRoomID = %v, want Month N %v (rollover row must remain visible to lineage)", latest.ID, mn.ID)
	}

	// ── Month N+1 (2026-05): normal MONTHLY on the new low value ──
	// previous MUST = Month N curr (20/5), NOT Month N prev (9970/990).
	// populatePrevious() reads latest.Current — if it ever read
	// latest.Previous instead, Month N+1 would anchor on 9970/990 and
	// either error out (curr 80 < prev 9970) or wrap to a huge negative
	// usage. Either failure mode ships a clearly broken bill.
	mNext, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-05",
		ElectricityCurrent: 80,
		WaterCurrent:       25,
	})
	if err != nil {
		t.Fatalf("create Month N+1: %v", err)
	}

	// ── Assert 5: Month N+1 inherits Month N CURR (the new low) ──
	if mNext.ElectricityPrevious != mn.ElectricityCurrent {
		t.Errorf("Month N+1 elec previous = %d, want %d (Month N curr — rollover must continue from new LOW, NOT loop back to old high 9970)",
			mNext.ElectricityPrevious, mn.ElectricityCurrent)
	}
	if mNext.WaterPrevious != mn.WaterCurrent {
		t.Errorf("Month N+1 water previous = %d, want %d (Month N curr — rollover must continue from new LOW, NOT loop back to old high 990)",
			mNext.WaterPrevious, mn.WaterCurrent)
	}

	// ── Assert 6: rollover flag is NOT sticky ──
	// Month N+1 is a normal cycle on the new meter run. If the flag
	// carried over (e.g. populatePrevious wrote back from latest's flags
	// instead of taking them from the request), the read-time formula
	// would mis-compute usage as (digitMax(20)-20)+80 = (99-20)+80 = 159
	// instead of 60 — a ~2.65× over-bill.
	if mNext.IsRolloverElectricity {
		t.Error("Month N+1 is_rollover_electricity = true, want false (rollover is single-month, not sticky)")
	}
	if mNext.IsRolloverWater {
		t.Error("Month N+1 is_rollover_water = true, want false (rollover is single-month, not sticky)")
	}

	// ── Assert 7: Month N+1 usage = normal subtraction ──
	if got := mNext.ElectricityUsed(); got != 60 {
		t.Errorf("Month N+1 elec usage = %d, want 60 (= 80-20 normal subtraction)", got)
	}
	if got := mNext.WaterUsed(); got != 20 {
		t.Errorf("Month N+1 water usage = %d, want 20 (= 25-5 normal subtraction)", got)
	}

	// ── Assert 8: Month N+1 snapshot ──
	//   300_000 + 60*800 + 20*1800 = 300_000 + 48_000 + 36_000 = 384_000 satang
	snapNext := billing.ComputeMonthlyBillSnapshot(
		"2026-05",
		c.MonthlyRent,
		c.ElectricityRatePerUnit,
		c.WaterRatePerUnit,
		&mNext.MeterReading,
		nil,
		nil,
	)
	if snapNext.TotalAmount != 384_000 {
		t.Errorf("Month N+1 snapshot total = %d, want 384000 (rent + 60*800 + 20*1800)", snapNext.TotalAmount)
	}
	elecQty, waterQty := pickLineQuantities(t, "Month N+1", snapNext.LineItems)
	if elecQty != 60 {
		t.Errorf("Month N+1 elec quantity = %d, want 60 (normal subtraction)", elecQty)
	}
	if waterQty != 20 {
		t.Errorf("Month N+1 water quantity = %d, want 20 (normal subtraction)", waterQty)
	}
}

// pickLineQuantities extracts ELECTRICITY and WATER line-item quantities
// from a computed snapshot. Fatals if either is missing — both must always
// appear in a monthly snapshot, so a missing item is a structural bug
// rather than a quantity mismatch.
func pickLineQuantities(t *testing.T, label string, lines []billing.ComputedLineItem) (elec, water int) {
	t.Helper()
	var sawElec, sawWater bool
	for _, li := range lines {
		switch li.Type {
		case billing.LineItemElectricity:
			elec = li.Quantity
			sawElec = true
		case billing.LineItemWater:
			water = li.Quantity
			sawWater = true
		}
	}
	if !sawElec || !sawWater {
		t.Fatalf("%s: snapshot missing ELECTRICITY or WATER line: %+v", label, lines)
	}
	return
}
