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

// TestMissedReadings_ActiveContract_NextMonthInheritsLastReading closes the
// meter lineage hardening cycle by locking the calendar-vs-reading rule:
//
//	Lineage is READING-based, not CALENDAR-MONTH-based.
//	Missed months MUST NOT reset the meter chain.
//
// All prior hardening tests verified what happens at events (cancel, close,
// correction, replacement, rollover). This one verifies what happens at
// NON-events — months where the operator simply missed taking a reading.
// The invariant is negative: nothing should happen. The chain skips over
// the gap as if the missed months weren't there.
//
// IMPORTANT — this is NOT a vacancy test:
//   - Contract stays ACTIVE through all four months.
//   - No tenant turnover, no contract end, no notice.
//   - The scenario models OPERATIONAL miss: admin forgot, mobile entry
//     skipped, tenant unavailable on read day, etc.
//
// True no-contract readings (separate concern):
//   - The meter service has no contract guard, so orphan readings are
//     technically creatable; they just can't become bills (every billing
//     entry point requires `IsActive() == true` or filters to active
//     contracts — see CreateMonthlyBill at billing/service.go and
//     loadBatchInputs at billing/monthly/planning_service.go).
//   - That's a separate policy axis, not what this test covers.
//
// Scenario (operational miss, single active contract throughout):
//
//	Month N   (2026-03): MONTHLY  elec curr 1000 / water curr 100
//	Month N+1 (2026-04): no reading, no bill   ← missed
//	Month N+2 (2026-05): no reading, no bill   ← missed
//	Month N+3 (2026-06): MONTHLY  elec curr 1150 / water curr 118
//	    prev MUST = 1000 / 100  (Month N curr, across the 2-month miss)
//	    elec usage = 150 / water usage = 18  (accumulated since last read)
//
// What this guards against:
//   - A future "stale reading expiry" feature (e.g. ignore readings older
//     than N months) that would silently make Month N+3 use prev=0 and
//     over-bill the tenant by the full Month N curr (1100 units' worth
//     in this scenario).
//   - A "consecutive months required" guard inside meter validation that
//     would reject Month N+3 with "previous month meter missing" — better
//     to skip than to block, because operational misses happen.
//   - Auto-creation of zero-usage phantom rows for skipped months by some
//     batch process — caught by the row-count assertion (exactly 2 rows,
//     no synthetic infill).
//   - A change to FindLatestByRoomID that filters by recency rather than
//     by absolute "most recent existing row".
func TestMissedReadings_ActiveContract_NextMonthInheritsLastReading(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph: one tenant, one contract that stays ACTIVE
	//    through the entire gap. The contract status is load-bearing
	//    for the test premise — this is operational miss under an
	//    active tenancy, not vacancy. ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "M-606")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingApplicationChecker{}, txMgr)

	// ── Month N (2026-03): normal MONTHLY ──
	// First reading on this room → previous=0; usage = current.
	// The role of this reading in the test is to be the lineage anchor
	// that Month N+3 must reach back across the gap to find.
	n, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-03",
		ElectricityCurrent: 1000,
		WaterCurrent:       100,
	})
	if err != nil {
		t.Fatalf("create Month N: %v", err)
	}

	// ── (Gap) Month N+1 and N+2: deliberately NO reads, NO bills ──
	// The whole test premise is that nothing happens here. The
	// row-count assertion below makes that premise observable: if any
	// background process auto-fills phantom rows, the count goes from
	// 2 to 4 and the test fails.

	// ── Month N+3 (2026-06): normal MONTHLY across the gap ──
	// previous MUST = Month N curr (1000/100), NOT 0 (which would mean
	// the system "forgot" the prior reading after a calendar threshold).
	mNext, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-06",
		ElectricityCurrent: 1150,
		WaterCurrent:       118,
	})
	if err != nil {
		t.Fatalf("create Month N+3 (after 2-month miss): %v", err)
	}

	// ── Assert 1: lineage hops over the 2-month gap ──
	// If a refactor adds a "previous month must exist" guard, the create
	// call above would have failed; if a "stale reading expiry" feature
	// trims readings older than X months, prev here would be 0 instead
	// of Month N curr. Both bugs land here.
	if mNext.ElectricityPrevious != n.ElectricityCurrent {
		t.Errorf("Month N+3 elec previous = %d, want %d (Month N curr — missed months must NOT reset lineage to 0)",
			mNext.ElectricityPrevious, n.ElectricityCurrent)
	}
	if mNext.WaterPrevious != n.WaterCurrent {
		t.Errorf("Month N+3 water previous = %d, want %d (Month N curr — missed months must NOT reset lineage to 0)",
			mNext.WaterPrevious, n.WaterCurrent)
	}

	// ── Assert 2: usage = post-gap curr − pre-gap curr ──
	// 1150 − 1000 = 150 elec; 118 − 100 = 18 water. The whole gap shows
	// up as Month N+3's usage, which is the intended recovery-month
	// behavior — admin re-reads the meter and bills the accumulated
	// usage in one cycle.
	if got := mNext.ElectricityUsed(); got != 150 {
		t.Errorf("Month N+3 elec usage = %d, want 150 (= 1150-1000 across the gap)", got)
	}
	if got := mNext.WaterUsed(); got != 18 {
		t.Errorf("Month N+3 water usage = %d, want 18 (= 118-100 across the gap)", got)
	}

	// ── Assert 3: no phantom rows infilled for skipped months ──
	// A future batch process that auto-creates zero-usage rows for
	// skipped months would change the row count from 2 to 4. The
	// lineage would still LOOK correct on the asserts above (phantom
	// rows with prev=curr have zero usage), so the only way to catch
	// this kind of "well-meaning" infill is a row-count assert.
	var count int64
	if err := db.Model(&meterreading.MeterReading{}).
		Where("room_id = ?", rm.ID).Count(&count).Error; err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 2 {
		t.Errorf("reading count = %d, want 2 (no phantom infill for missed months)", count)
	}

	// ── Assert 4: FindLatestByRoomID returns the post-gap row ──
	// Tautology given Assert 1 passed, but pinned explicitly so a future
	// refactor that splits the populatePrevious path from the "latest"
	// lookup can't silently regress one without the other.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after Month N+3: %v", err)
	}
	if latest.ID != mNext.ID {
		t.Errorf("FindLatestByRoomID = %v, want Month N+3 %v", latest.ID, mNext.ID)
	}

	// ── Assert 5: ComputeMonthlyBillSnapshot quantities = 150 / 18 ──
	//   300_000 + 150*800 + 18*1800 = 300_000 + 120_000 + 32_400 = 452_400
	// The recovery month's bill captures the full accumulated usage —
	// the tenant pays once for the gap, the apartment recovers the
	// revenue, no usage is lost.
	snapshot := billing.ComputeMonthlyBillSnapshot(
		"2026-06",
		c.MonthlyRent,
		c.ElectricityRatePerUnit,
		c.WaterRatePerUnit,
		&mNext.MeterReading,
		nil,
	)
	elecQty, waterQty := pickLineQuantitiesM(t, "Month N+3", snapshot.LineItems)
	if elecQty != 150 {
		t.Errorf("Month N+3 bill electricity quantity = %d, want 150 (full gap usage billed in the recovery month)", elecQty)
	}
	if waterQty != 18 {
		t.Errorf("Month N+3 bill water quantity = %d, want 18 (full gap usage billed in the recovery month)", waterQty)
	}
	if snapshot.TotalAmount != 452_400 {
		t.Errorf("Month N+3 snapshot total = %d, want 452400 (rent + 150*800 + 18*1800)", snapshot.TotalAmount)
	}
}

// pickLineQuantitiesM mirrors the helper in
// service_rollover_lineage_integration_test.go. Duplicated rather than
// shared because Go's test-file-level scoping makes a tiny helper cheaper
// than extracting a shared test-util package just for two callers.
func pickLineQuantitiesM(t *testing.T, label string, lines []billing.ComputedLineItem) (elec, water int) {
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
		t.Fatalf("%s: snapshot missing ELECTRICITY or WATER: %+v", label, lines)
	}
	return
}
