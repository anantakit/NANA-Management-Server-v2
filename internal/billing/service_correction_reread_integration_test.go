//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// TestCorrection_RereadsMeterAfterEdit_Idempotent proves that bill correction
// re-reads the meter row from `meter_readings` on every iteration — it never
// reuses the prior bill's line-item snapshot.
//
// The risk this guards against: a future refactor that caches the meter
// lookup (or sources `quantity` from the old bill instead of the meter) would
// be invisible to single-correction tests — they'd still pass because the
// first correction does a fresh read. Only a multi-correction cycle with
// meter edits between iterations surfaces the bug.
//
// Cycle exercised:
//
//	T0  meter: 50 elec / 5 water used    → correct → new1 totals 349_000
//	     ↓ finalize new1
//	T1  meter: 100 elec / 10 water used  → correct → new2 totals 398_000
//	     ↓ finalize new2
//	T2  meter:  75 elec /  7 water used  → correct → new3 totals 372_600
//
// If any iteration anchored on the prior bill's snapshot instead of re-reading
// the meter, the totals would be stuck at the earlier iteration's value.
//
// Companion to the monthly correction architecture lock
// (project_billing_correction_arch_lock.md): single-correction invariants
// (unique index, deferred FK, row-lock, audit rollback) are already pinned
// by service_correction_integration_test.go; this test pins the
// re-read-from-source invariant across N iterations.
func TestCorrection_RereadsMeterAfterEdit_Idempotent(t *testing.T) {
	env := newCorrectionTestEnv(t, nil)
	ctx := context.Background()

	// ── T0: first correction reads the as-seeded meter (50 elec / 5 water) ──
	// Sanity-checks that the FIRST correction also re-reads (env.oldBill's
	// total is intentionally bogus at 999999 to surface a cloned-snapshot bug).
	new1, err := env.svc.CorrectBill(ctx, env.oldBill.ID,
		CorrectBillRequest{CorrectionReason: "round 1"}, nilActor)
	if err != nil {
		t.Fatalf("correction round 1: %v", err)
	}
	assertCorrectionTotals(t, "round 1 (T0 meter)", new1, 50, 5, 349_000)

	// CanCorrect requires FINALIZED. Promote new1 so it's correctable again.
	new1Finalized, err := env.svc.FinalizeBill(ctx, new1.ID, nilActor)
	if err != nil {
		t.Fatalf("finalize new1: %v", err)
	}

	// ── Admin edit #1: bump usage to 100 elec / 10 water ──
	setMeterCurrents(t, env, 1100, 110)

	// ── T1: second correction MUST re-read the edited meter ──
	// If correction reused new1's snapshot (50/5), this assertion fails.
	new2, err := env.svc.CorrectBill(ctx, new1Finalized.ID,
		CorrectBillRequest{CorrectionReason: "round 2"}, nilActor)
	if err != nil {
		t.Fatalf("correction round 2: %v", err)
	}
	assertCorrectionTotals(t, "round 2 (T1 meter)", new2, 100, 10, 398_000)

	new2Finalized, err := env.svc.FinalizeBill(ctx, new2.ID, nilActor)
	if err != nil {
		t.Fatalf("finalize new2: %v", err)
	}

	// ── Admin edit #2: drop usage to 75 elec / 7 water ──
	// Direction matters: USAGE GOING DOWN guards against a "max-of-snapshots"
	// bug. A correct implementation overwrites; a buggy one might pick up the
	// HIGHEST historical usage if it accumulated from any prior snapshot.
	setMeterCurrents(t, env, 1075, 107)

	// ── T2: third correction MUST again re-read the edited meter ──
	new3, err := env.svc.CorrectBill(ctx, new2Finalized.ID,
		CorrectBillRequest{CorrectionReason: "round 3"}, nilActor)
	if err != nil {
		t.Fatalf("correction round 3: %v", err)
	}
	assertCorrectionTotals(t, "round 3 (T2 meter)", new3, 75, 7, 372_600)

	// ── Chain invariant: exactly 1 non-VOID bill on (contract, month). ──
	// The correction chain orig → new1 → new2 → new3 should leave exactly
	// new3 active; all three prior bills must be VOID. This pins the partial
	// unique index against subtle reorder bugs in multi-correction scenarios.
	var nonVoidCount int64
	if err := env.db.Model(&Bill{}).
		Where("contract_id = ? AND billing_month = ? AND status != ?",
			env.c.ID, env.oldBill.BillingMonth, BillStatusVoid).
		Count(&nonVoidCount).Error; err != nil {
		t.Fatalf("count non-VOID: %v", err)
	}
	if nonVoidCount != 1 {
		t.Errorf("non-VOID count = %d, want 1 (only the latest correction active)", nonVoidCount)
	}

	// Distinct IDs at every step — proves each correction is a NEW row,
	// not an in-place mutation. Mutation in place would silently make
	// correction history un-auditable.
	ids := []uuid.UUID{env.oldBill.ID, new1.ID, new2.ID, new3.ID}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate bill ID in correction chain: %v", id)
		}
		seen[id] = true
	}

	// Each prior bill VOID with reason CORRECTION + superseded chain forward.
	for i, link := range []struct {
		prev, next uuid.UUID
		label      string
	}{
		{env.oldBill.ID, new1.ID, "orig→new1"},
		{new1.ID, new2.ID, "new1→new2"},
		{new2.ID, new3.ID, "new2→new3"},
	} {
		var prior Bill
		if err := env.db.Where("id = ?", link.prev).First(&prior).Error; err != nil {
			t.Fatalf("reload prior bill %d (%s): %v", i, link.label, err)
		}
		if !prior.IsVoid() {
			t.Errorf("link %s: prior status = %s, want VOID", link.label, prior.Status)
		}
		if prior.VoidReason == nil || *prior.VoidReason != "CORRECTION" {
			t.Errorf("link %s: prior void_reason = %v, want CORRECTION", link.label, prior.VoidReason)
		}
		if prior.SupersededByBillID == nil || *prior.SupersededByBillID != link.next {
			t.Errorf("link %s: superseded_by = %v, want %v", link.label, prior.SupersededByBillID, link.next)
		}
	}
}

// setMeterCurrents mutates the seeded MONTHLY meter row in-place. Direct DB
// UPDATE (not via service) so the test simulates an out-of-band admin edit
// happening between corrections — the exact scenario that a snapshot-reuse
// bug would mask.
func setMeterCurrents(t *testing.T, env *correctionTestEnv, elecCurrent, waterCurrent int) {
	t.Helper()
	if err := env.db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND billing_month = ?", env.roomID, env.oldBill.BillingMonth).
		Updates(map[string]any{
			"electricity_current": elecCurrent,
			"water_current":       waterCurrent,
		}).Error; err != nil {
		t.Fatalf("edit meter (elec=%d water=%d): %v", elecCurrent, waterCurrent, err)
	}
}

// assertCorrectionTotals checks the bill total + per-line quantities against
// the meter usage that was current at the time of correction. Asserts on
// quantity (not just total) so a future bug that fakes the right total via
// wrong-quantity × wrong-rate still trips the test.
func assertCorrectionTotals(t *testing.T, label string, bill *BillWithRelations, wantElec, wantWater int, wantTotal int64) {
	t.Helper()
	if bill.TotalAmount != wantTotal {
		t.Errorf("%s: total = %d, want %d", label, bill.TotalAmount, wantTotal)
	}
	var elecQty, waterQty int
	var sawElec, sawWater bool
	for _, li := range bill.LineItems {
		switch li.LineType {
		case LineItemElectricity:
			elecQty = li.Quantity
			sawElec = true
		case LineItemWater:
			waterQty = li.Quantity
			sawWater = true
		}
	}
	if !sawElec || !sawWater {
		t.Fatalf("%s: missing ELECTRICITY or WATER line item: %+v", label, bill.LineItems)
	}
	if elecQty != wantElec {
		t.Errorf("%s: electricity quantity = %d, want %d (meter not re-read?)", label, elecQty, wantElec)
	}
	if waterQty != wantWater {
		t.Errorf("%s: water quantity = %d, want %d (meter not re-read?)", label, waterQty, wantWater)
	}
}
