//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
)

// TestFindUnreflectedOverRecords_ParityWithGate locks Epic B INV-1 on real
// Postgres: the finalize DETECTOR (HasUnreflectedOverRecordByContractID, bool)
// and the settlement RESOLVER's discovery (FindUnreflectedOverRecordsByContractID,
// rows) evaluate ONE predicate. It asserts, at every state, that
//
//	gate == (len(discovery) > 0)
//
// AND that the discovery row-set is exactly the expected (recovery, utility)
// pairs across every edge a mock can't model: source-less (excluded), S0
// unbilled source (excluded), soft-deleted recovery (excluded), already-reflected
// on a non-VOID bill (excluded), reflected only on a VOID bill (still included),
// multi-utility (both pairs), multi-recovery (N rows). If these two ever drift,
// deadlock D1 returns — so this test is the structural guard behind the wrapper.
func TestFindUnreflectedOverRecords_ParityWithGate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()
	repo := NewBillingRepository(db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "PARITY-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	elec := AdjustmentUtilityElectricity
	water := AdjustmentUtilityWater

	type pair struct {
		rec  uuid.UUID
		util AdjustmentUtility
	}

	// assertParity is the heart of the test: it proves INV-1 (gate == len>0) and
	// pins the exact discovery set. Called at multiple states so the equivalence
	// is exercised repeatedly, not once.
	assertParity := func(label string, want ...pair) {
		t.Helper()
		rows, err := repo.FindUnreflectedOverRecordsByContractID(ctx, c.ID)
		if err != nil {
			t.Fatalf("%s: discovery: %v", label, err)
		}
		gate, err := repo.HasUnreflectedOverRecordByContractID(ctx, c.ID)
		if err != nil {
			t.Fatalf("%s: gate: %v", label, err)
		}
		if gate != (len(rows) > 0) {
			t.Fatalf("%s: INV-1 BROKEN — gate=%v but len(discovery)=%d (%+v)", label, gate, len(rows), rows)
		}
		got := map[pair]bool{}
		for _, r := range rows {
			got[pair{r.RecoveryID, r.Utility}] = true
		}
		if len(got) != len(rows) {
			t.Fatalf("%s: discovery returned duplicate pairs: %+v", label, rows)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: discovery pair count = %d, want %d (%+v)", label, len(got), len(want), rows)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("%s: missing expected pair {%s,%s} in %+v", label, w.rec, w.util, rows)
			}
		}
	}

	// Empty contract → no recoveries → gate false, discovery empty.
	assertParity("empty")

	// seedSource creates a source MONTHLY reading for `month`; if billed, also a
	// FINALIZED monthly bill for that month (the S0 gate only requires the bill to
	// exist, not its lines).
	seedSource := func(month string, billed bool) *meterreading.MeterReading {
		src := &meterreading.MeterReading{
			RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
			ElectricityPrevious: 100, ElectricityCurrent: 300,
			WaterPrevious: 50, WaterCurrent: 90,
		}
		if err := db.Create(src).Error; err != nil {
			t.Fatalf("seed source %s: %v", month, err)
		}
		if billed {
			sb := &Bill{ContractID: c.ID, BillingMonth: month, BillType: BillTypeMonthly, Status: BillStatusFinalized}
			if err := db.Create(sb).Error; err != nil {
				t.Fatalf("seed source bill %s: %v", month, err)
			}
		}
		return src
	}

	// seedRecovery creates a READING_RECOVERY row (prev=curr per migration 00040)
	// pointing optionally at src, with optional per-utility over-records
	// (recorded > current); optionally soft-deleted.
	seedRecovery := func(month string, src *meterreading.MeterReading, elecRec, waterRec *int, deleted bool) *meterreading.MeterReading {
		reason := meterreading.AnchorReasonReadingRecovery
		note := "recovery " + month
		r := &meterreading.MeterReading{
			RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
			ElectricityPrevious: 100, ElectricityCurrent: 100,
			WaterPrevious: 50, WaterCurrent: 50,
			ElectricityRecorded: elecRec, WaterRecorded: waterRec,
			AnchorReason: &reason, AnchorNote: &note,
		}
		if src != nil {
			r.RecoverySourceReadingID = &src.ID
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed recovery %s: %v", month, err)
		}
		if deleted {
			if err := db.Delete(r).Error; err != nil {
				t.Fatalf("soft-delete recovery %s: %v", month, err)
			}
		}
		return r
	}

	reflect := func(bill *Bill, recID uuid.UUID, util AdjustmentUtility, amount int64) {
		reason := AdjustmentReasonMeterRecovery
		note := "resolved 10-char note"
		li := &BillLineItem{
			BillID: bill.ID, LineType: LineItemAdjustment, Source: LineItemSourceManual,
			Description: "adj", Amount: amount,
			AdjustmentRecoveryReadingID: &recID, AdjustmentUtility: &util,
			AdjustmentReasonCode: &reason, AdjustmentNote: &note,
		}
		if err := db.Create(li).Error; err != nil {
			t.Fatalf("reflect recovery %s: %v", recID, err)
		}
	}
	seedBill := func(month string, status BillStatus) *Bill {
		b := &Bill{ContractID: c.ID, BillingMonth: month, BillType: BillTypeMonthly, Status: status}
		if err := db.Create(b).Error; err != nil {
			t.Fatalf("seed bill %s: %v", month, err)
		}
		return b
	}

	over := func(v int) *int { return &v }

	billedSrc := seedSource("2025-10", true)
	unbilledSrc := seedSource("2025-11", false)

	recEdge := seedRecovery("2026-01", billedSrc, over(300), nil, false)       // elec over-record, billed source → in set
	seedRecovery("2026-02", nil, over(300), nil, false)                        // source-less → excluded
	seedRecovery("2026-03", unbilledSrc, over(300), nil, false)                // S0 unbilled source → excluded
	seedRecovery("2026-04", billedSrc, over(300), nil, true)                   // soft-deleted → excluded
	recMulti := seedRecovery("2026-05", billedSrc, over(300), over(90), false) // elec+water → two pairs
	recReflected := seedRecovery("2026-06", billedSrc, over(300), nil, false)  // reflected on non-VOID → excluded
	recVoidOnly := seedRecovery("2026-07", billedSrc, over(300), nil, false)   // reflected only on VOID → in set

	draftBill := seedBill("2026-06", BillStatusDraft)
	voidBill := seedBill("2026-07", BillStatusVoid)
	reflect(draftBill, recReflected.ID, elec, -24000) // non-VOID reflection → recReflected resolved
	reflect(voidBill, recVoidOnly.ID, elec, -24000)   // VOID-only reflection → recVoidOnly still pending

	// Full set: recEdge(elec), recMulti(elec+water), recVoidOnly(elec) = 4 pairs.
	// Excluded: source-less, S0, soft-deleted, non-VOID-reflected.
	assertParity("full set",
		pair{recEdge.ID, elec},
		pair{recMulti.ID, elec},
		pair{recMulti.ID, water},
		pair{recVoidOnly.ID, elec},
	)

	// Resolve every remaining pair on the non-VOID draft bill → discovery empty,
	// gate false. Proves the equivalence holds at the boundary too.
	reflect(draftBill, recEdge.ID, elec, -24000)
	reflect(draftBill, recMulti.ID, elec, -24000)
	reflect(draftBill, recMulti.ID, water, -16200)
	reflect(draftBill, recVoidOnly.ID, elec, -24000)
	assertParity("all resolved")
}
