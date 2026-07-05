//go:build integration

// service_pending_list_per_utility_integration_test.go — Q1.5 P3-C.
// The pending LIST is a DISPLAY model, not the finalize model: a recovery stays
// visible until EVERY affected utility is resolved. A partial resolve
// (electricity done, water still pending) must NOT hide the row — otherwise the
// operator loses the water decision even though the finalize gate still blocks.
package meterreading_test

import (
	"context"
	"testing"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type pendingListEnv struct {
	db     *gorm.DB
	meter  meterreading.MeterReadingService
	bill   billing.BillingService
	ctx    context.Context
	roomID uuid.UUID
	billID uuid.UUID
}

func setupPendingList(t *testing.T) pendingListEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "PL-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)
	meterSvc, billSvc, _ := buildPhase7Services(t, db)

	// DRAFT bill with electricity + water AUTO lines (re-baseline targets).
	bill := &billing.Bill{ContractID: con.ID, BillingMonth: "2026-05", BillType: billing.BillTypeMonthly, Status: billing.BillStatusDraft}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	for _, li := range []billing.BillLineItem{
		{BillID: bill.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า", Quantity: 100, UnitPrice: con.ElectricityRatePerUnit, Amount: 100 * con.ElectricityRatePerUnit, SortOrder: 1},
		{BillID: bill.ID, LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ", Quantity: 20, UnitPrice: con.WaterRatePerUnit, Amount: 20 * con.WaterRatePerUnit, SortOrder: 2},
	} {
		if err := db.Create(&li).Error; err != nil {
			t.Fatalf("seed auto line: %v", err)
		}
	}
	return pendingListEnv{db: db, meter: meterSvc, bill: billSvc, ctx: context.Background(), roomID: rm.ID, billID: bill.ID}
}

func (e pendingListEnv) pendingCount(t *testing.T) int {
	t.Helper()
	list, err := e.meter.ListPendingBaselineCorrectionsByRoom(e.ctx, e.roomID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom: %v", err)
	}
	return len(list)
}

func (e pendingListEnv) resolve(t *testing.T, recID uuid.UUID, utility string) {
	t.Helper()
	if _, err := e.bill.UpdateMonthlyDraft(e.ctx, e.billID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Utility: utility, Decision: "ACCEPT", AdjustmentNote: "คืนยอดที่เก็บเกิน " + utility},
		},
	}, nil); err != nil {
		t.Fatalf("resolve %s: %v", utility, err)
	}
}

// Electricity-only over-record: resolving electricity drops the row (nothing
// else affected).
func TestPendingList_ElectricityOnly_ResolveDropsRow(t *testing.T) {
	e := setupPendingList(t)
	elecRec := 500
	corr, err := e.meter.CreateBaselineCorrection(e.ctx, meterreading.CreateBaselineCorrectionInput{
		RoomID: &e.roomID, ElectricityCurrent: 380, WaterCurrent: 65,
		ElectricityRecorded: &elecRec, // water NOT affected (nil)
		AnchorNote:          "elec-only over-record",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}
	if got := e.pendingCount(t); got != 1 {
		t.Fatalf("pending before resolve = %d, want 1", got)
	}
	e.resolve(t, corr.ID, "ELECTRICITY")
	if got := e.pendingCount(t); got != 0 {
		t.Fatalf("pending after resolving the only affected utility = %d, want 0 (row drops)", got)
	}
}

// Both utilities over-record: resolving ONLY electricity keeps the row visible
// (water still pending); resolving water too finally drops it.
func TestPendingList_BothAffected_PartialResolveKeepsRow(t *testing.T) {
	e := setupPendingList(t)
	elecRec, waterRec := 500, 100
	corr, err := e.meter.CreateBaselineCorrection(e.ctx, meterreading.CreateBaselineCorrectionInput{
		RoomID: &e.roomID, ElectricityCurrent: 380, WaterCurrent: 65,
		ElectricityRecorded: &elecRec, WaterRecorded: &waterRec, // both affected
		AnchorNote: "both over-record",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}
	if got := e.pendingCount(t); got != 1 {
		t.Fatalf("pending before resolve = %d, want 1", got)
	}

	// Resolve ELECTRICITY only → row MUST remain (water unresolved).
	e.resolve(t, corr.ID, "ELECTRICITY")
	if got := e.pendingCount(t); got != 1 {
		t.Fatalf("pending after resolving only electricity = %d, want 1 (water still pending → row stays visible)", got)
	}

	// Resolve BOTH in one save (UpdateMonthlyDraft replaces all MANUAL/ADJUSTMENT
	// lines per save, so the operator's final save carries every decision) → all
	// affected utilities resolved → row drops.
	if _, err := e.bill.UpdateMonthlyDraft(e.ctx, e.billID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: corr.ID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดไฟฟ้า"},
			{RecoveryReadingID: corr.ID.String(), Utility: "WATER", Decision: "ACCEPT", AdjustmentNote: "คืนยอดน้ำ"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve both: %v", err)
	}
	if got := e.pendingCount(t); got != 0 {
		t.Fatalf("pending after resolving both = %d, want 0", got)
	}
}

// Voiding the bill that carried the resolutions re-pends EVERY affected utility:
// applied state is derived from non-VOID lines, so a both-affected recovery
// reappears in the list after its bill is voided (via correction).
func TestPendingList_VoidRePendsAllAffectedUtilities(t *testing.T) {
	e := setupPendingList(t)
	elecRec, waterRec := 500, 100
	corr, err := e.meter.CreateBaselineCorrection(e.ctx, meterreading.CreateBaselineCorrectionInput{
		RoomID: &e.roomID, ElectricityCurrent: 380, WaterCurrent: 65,
		ElectricityRecorded: &elecRec, WaterRecorded: &waterRec,
		AnchorNote: "both over-record",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Resolve both in one save → row drops.
	if _, err := e.bill.UpdateMonthlyDraft(e.ctx, e.billID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: corr.ID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดไฟฟ้า"},
			{RecoveryReadingID: corr.ID.String(), Utility: "WATER", Decision: "ACCEPT", AdjustmentNote: "คืนยอดน้ำ"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve both: %v", err)
	}
	if got := e.pendingCount(t); got != 0 {
		t.Fatalf("pending after resolving both = %d, want 0", got)
	}

	// Finalize, then VOID the bill → the resolutions now sit on a VOID bill and
	// no longer count (applied state = non-VOID lines only).
	if _, err := e.bill.FinalizeBill(e.ctx, e.billID, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, err := e.bill.VoidBill(e.ctx, e.billID, billing.VoidBillRequest{Reason: "void re-pends over-records"}, nil); err != nil {
		t.Fatalf("VoidBill: %v", err)
	}

	// Both utilities re-pend → the row reappears exactly once.
	if got := e.pendingCount(t); got != 1 {
		t.Fatalf("pending after void = %d, want 1 (both affected utilities re-pend)", got)
	}
}
