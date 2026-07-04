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

// TestHasPendingRecoveryByContractID_MultiRow locks the Q1 finalization-gate
// predicate on real Postgres, specifically the multi-row case the owner
// required: a room with a MIX of resolved + pending recoveries must still
// report pending (block finalize) until EVERY recovery is resolved.
//
// Also pins two edge rules that a mock can't model:
//   - a WAIVED (zero-amount) ADJUSTMENT line counts as resolved;
//   - an ADJUSTMENT line on a VOID bill does NOT count (recovery returns to
//     pending on void) — b.status <> 'VOID' in the join.
func TestHasPendingRecoveryByContractID_MultiRow(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()
	repo := NewBillingRepository(db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "GATE-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	seedRecovery := func(month string) *meterreading.MeterReading {
		reason := meterreading.AnchorReasonReadingRecovery
		note := "จดมิเตอร์ผิด " + month
		elecRecorded := 200 // over-record: recorded 200 > physical 100 (elec affected)
		r := &meterreading.MeterReading{
			RoomID:              rm.ID,
			ReadingType:         meterreading.ReadingTypeMonthly,
			BillingMonth:        &month,
			ElectricityPrevious: 100, ElectricityCurrent: 100, // prev=curr (recovery invariant, migration 00040)
			WaterPrevious: 50, WaterCurrent: 50,
			ElectricityRecorded: &elecRecorded,
			AnchorReason:        &reason, AnchorNote: &note,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed recovery %s: %v", month, err)
		}
		return r
	}
	seedBill := func(status BillStatus) *Bill {
		b := &Bill{ContractID: c.ID, BillingMonth: "2026-06", BillType: BillTypeMonthly, Status: status}
		if err := db.Create(b).Error; err != nil {
			t.Fatalf("seed bill: %v", err)
		}
		return b
	}
	elecUtil := AdjustmentUtilityElectricity
	resolve := func(billID, recID uuid.UUID, amount int64, reason AdjustmentReasonCode) {
		note := "resolved 10-char note"
		li := &BillLineItem{
			BillID: billID, LineType: LineItemAdjustment, Source: LineItemSourceManual,
			Description: "adj", Amount: amount,
			AdjustmentRecoveryReadingID: &recID, AdjustmentUtility: &elecUtil,
			AdjustmentReasonCode: &reason, AdjustmentNote: &note,
		}
		if err := db.Create(li).Error; err != nil {
			t.Fatalf("resolve recovery: %v", err)
		}
	}

	recA := seedRecovery("2026-03")
	recB := seedRecovery("2026-04")
	recC := seedRecovery("2026-05")

	draftBill := seedBill(BillStatusDraft)
	voidBill := seedBill(BillStatusVoid)

	// recA resolved (refund on a non-VOID bill); recC "resolved" only on a VOID
	// bill (must NOT count); recB untouched → pending.
	resolve(draftBill.ID, recA.ID, -15000, AdjustmentReasonMeterRecovery)
	resolve(voidBill.ID, recC.ID, -15000, AdjustmentReasonMeterRecovery)

	// Multi-row: recB pending + recC void-only → still pending overall → block.
	pending, err := repo.HasUnresolvedOverRecordByContractID(ctx, c.ID)
	if err != nil {
		t.Fatalf("HasUnresolvedOverRecordByContractID: %v", err)
	}
	if !pending {
		t.Fatal("expected pending=true (recB pending, recC only resolved on a VOID bill)")
	}

	// Resolve recC on a non-VOID bill and recB via WAIVE (zero-amount line).
	resolve(draftBill.ID, recC.ID, -15000, AdjustmentReasonMeterRecovery)
	resolve(draftBill.ID, recB.ID, 0, AdjustmentReasonMeterRecoveryWaived)

	pending, err = repo.HasUnresolvedOverRecordByContractID(ctx, c.ID)
	if err != nil {
		t.Fatalf("HasUnresolvedOverRecordByContractID (after resolve): %v", err)
	}
	if pending {
		t.Fatal("expected pending=false after all over-records resolved (refund + refund + waive)")
	}
}
