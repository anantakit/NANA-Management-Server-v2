//go:build integration

// External test package — billing imports meterreading for
// ComputeMonthlyBillSnapshot, so an internal `meterreading` test importing
// billing would cycle. Mirrors the moveout_test choice for the cancel/close
// lineage tests.
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

// TestReplacement_NextMonthInheritsNewMeterStart locks the meter-replacement
// lineage invariant:
//
//	Replacement resets `previous` for the replacement month only.
//	The next month's `previous` MUST inherit the replaced meter's new
//	`current`, NOT the prior meter's `current` from before replacement.
//
// Scenario:
//
//	Month N-1 (2026-03): normal MONTHLY     — elec 100 / water 15 (old meter)
//	Month N   (2026-04): MONTHLY + replace  — elec curr 20 / water curr 3
//	                                          previous forced to 0/0
//	Month N+1 (2026-05): normal MONTHLY     — elec 200 / water 50
//	                                          previous MUST = 20/3 (Month N curr)
//
// Numbers picked so both the correct lineage AND the lineage-broken paths
// pass meter validation (current ≥ previous). That forces the bug to surface
// as a *quantity* / *usage* mismatch in the assertion, not as a fatal
// "current < previous" error that could mask the root cause.
//
//	Correct (prev = 20 / 3)   → Month N+1 usage = 180 / 47
//	Broken  (prev = 100 / 15) → Month N+1 usage = 100 / 35
//
// Both totals are different across enough places (line-item quantity, bill
// total) that any silent regression has nowhere to hide.
//
// What this guards against:
//   - A refactor that filters `is_meter_replaced=true` rows out of
//     FindLatestByRoomID — Month N+1 would then skip back to N-1.
//   - A refactor that makes the replacement flag "sticky" across months —
//     Month N+1 would get prev=0 instead of prev=Month N curr.
//   - A refactor that swaps `latest.Current` → `latest.Previous` in
//     populatePrevious — Month N+1 would get prev=0 (Month N's prev),
//     not 20 (Month N's curr).
func TestReplacement_NextMonthInheritsNewMeterStart(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "Z-303")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 4)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txMgr)

	// ── Month N-1 (2026-03): normal MONTHLY on the OLD meter ──
	// First reading → previous auto-populates to 0/0; usage = current.
	n1, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-03",
		ElectricityCurrent: 100,
		WaterCurrent:       15,
	})
	if err != nil {
		t.Fatalf("create Month N-1: %v", err)
	}
	if n1.ElectricityCurrent != 100 || n1.WaterCurrent != 15 {
		t.Fatalf("Month N-1 unexpected currents: elec=%d water=%d", n1.ElectricityCurrent, n1.WaterCurrent)
	}

	// ── Month N (2026-04): MONTHLY with both meters REPLACED ──
	// Domain forces previous → 0 for each replaced side, regardless of what
	// the latest reading carries. Curr represents the new meter's reading at
	// month-end (i.e. the new meter has accumulated 20 elec / 3 water units
	// since installation).
	mn, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:                     rm.ID.String(),
		BillingMonth:               "2026-04",
		ElectricityCurrent:         20,
		WaterCurrent:               3,
		IsElectricityMeterReplaced: true,
		IsWaterMeterReplaced:       true,
	})
	if err != nil {
		t.Fatalf("create Month N (replacement): %v", err)
	}
	if mn.ElectricityPrevious != 0 {
		t.Errorf("Month N elec previous = %d, want 0 (replacement forces reset)", mn.ElectricityPrevious)
	}
	if mn.WaterPrevious != 0 {
		t.Errorf("Month N water previous = %d, want 0 (replacement forces reset)", mn.WaterPrevious)
	}
	// NOTE: replacement is a WRITE-TIME signal, not a persisted column —
	// `meter_readings` has no `is_meter_replaced` field (cf. migration
	// 00007 added rollover columns but no replacement columns). The only
	// per-row evidence that Month N was a replacement is `previous=0`
	// mid-stream with a prior reading present. Lineage correctness must
	// therefore flow from populatePrevious() alone; do not assume future
	// code can disambiguate "first reading" from "replacement" by row
	// inspection.
	// Usage on the replacement month equals current (since prev=0). This is
	// the legitimate "first usage on new meter" billing case — not a bug.
	if got := mn.ElectricityUsed(); got != 20 {
		t.Errorf("Month N elec usage = %d, want 20 (current-0 on new meter)", got)
	}
	if got := mn.WaterUsed(); got != 3 {
		t.Errorf("Month N water usage = %d, want 3 (current-0 on new meter)", got)
	}

	// ── Pre-flight: lineage query must return Month N (the replacement row),
	//                not Month N-1. If a refactor adds an
	//                `is_meter_replaced=false` filter, this catches it before
	//                the next-month-inheritance assertions even fire. ──
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after Month N: %v", err)
	}
	if latest.ID != mn.ID {
		t.Errorf("FindLatestByRoomID = %v, want Month N %v (replacement row must remain visible to lineage)", latest.ID, mn.ID)
	}

	// ── Month N+1 (2026-05): normal MONTHLY on the new meter ──
	// Previous MUST = Month N curr (20/3), NOT Month N-1 curr (100/15).
	// Both numbers below are picked so a broken-lineage path stays valid
	// (current ≥ previous in both cases) — the bug surfaces in the quantity
	// assertion, not as a validation error.
	mNext, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-05",
		ElectricityCurrent: 200,
		WaterCurrent:       50,
	})
	if err != nil {
		t.Fatalf("create Month N+1: %v", err)
	}

	// ── Assert 1: Month N+1 inherits Month N CURR as its previous ──
	if mNext.ElectricityPrevious != mn.ElectricityCurrent {
		t.Errorf("Month N+1 elec previous = %d, want %d (Month N curr — replacement must NOT skip lineage to Month N-1)",
			mNext.ElectricityPrevious, mn.ElectricityCurrent)
	}
	if mNext.WaterPrevious != mn.WaterCurrent {
		t.Errorf("Month N+1 water previous = %d, want %d (Month N curr — replacement must NOT skip lineage to Month N-1)",
			mNext.WaterPrevious, mn.WaterCurrent)
	}

	// ── Assert 2: usage reflects the NEW-METER chain ──
	// Correct: 200 - 20 = 180 elec, 50 - 3 = 47 water
	// Broken (if lineage skipped to N-1): 200 - 100 = 100, 50 - 15 = 35
	if got := mNext.ElectricityUsed(); got != 180 {
		t.Errorf("Month N+1 elec usage = %d, want 180 (anchor on new meter — if it were 100, lineage broke back to old meter)", got)
	}
	if got := mNext.WaterUsed(); got != 47 {
		t.Errorf("Month N+1 water usage = %d, want 47 (anchor on new meter — if it were 35, lineage broke back to old meter)", got)
	}

	// ── Assert 3: ComputeMonthlyBillSnapshot quantities match the new chain ──
	// Tests that the bill the tenant ACTUALLY receives reflects the
	// new-meter chain. With apt rates 800/1800 satang per unit and
	// 300000 satang monthly rent:
	//   correct: 300000 + 180*800 + 47*1800 = 528600
	//   broken : 300000 + 100*800 + 35*1800 = 443000
	snapshot := billing.ComputeMonthlyBillSnapshot(
		"2026-05",
		c.MonthlyRent,
		c.ElectricityRatePerUnit,
		c.WaterRatePerUnit,
		&mNext.MeterReading,
	)
	var elecQty, waterQty int
	var sawElec, sawWater bool
	for _, li := range snapshot.LineItems {
		switch li.Type {
		case billing.LineItemElectricity:
			elecQty = li.Quantity
			sawElec = true
		case billing.LineItemWater:
			waterQty = li.Quantity
			sawWater = true
		}
	}
	if !sawElec || !sawWater {
		t.Fatalf("snapshot missing ELECTRICITY or WATER line item: %+v", snapshot.LineItems)
	}
	if elecQty != 180 {
		t.Errorf("Month N+1 bill electricity quantity = %d, want 180 (anchor on new meter; 100 would mean lineage broke to old meter)", elecQty)
	}
	if waterQty != 47 {
		t.Errorf("Month N+1 bill water quantity = %d, want 47 (anchor on new meter; 35 would mean lineage broke to old meter)", waterQty)
	}
	if snapshot.TotalAmount != 528600 {
		t.Errorf("Month N+1 bill total = %d, want 528600 (rent 300000 + elec 180*800 + water 47*1800)", snapshot.TotalAmount)
	}
}
