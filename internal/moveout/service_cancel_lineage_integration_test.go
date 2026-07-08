//go:build integration

// External test package — settlement imports moveout for the compile-time
// BillingCommander check (adapter_check.go), so wiring a real settlement
// service inside package `moveout` would create an import cycle. Living
// in `moveout_test` keeps both imports legal.
package moveout_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/billing/settlement"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestMoveOutCancel_RevertsExitSettlement_ThenMonthlyBillUsesCorrectPrevious
// is the P0 lineage regression for the "tenant changes their mind after
// settlement preparation" flow:
//
//   N-1: normal MONTHLY reading (baseline.current = lineage anchor)
//   N  : move-out prepared → EXIT recorded + DRAFT settlement attached
//   N  : tenant stays → Cancel
//   N  : normal MONTHLY reading recorded instead
//
// Lineage invariant: the post-cancel MONTHLY reading must inherit
// baseline.current as its previous — NOT the cancelled EXIT's current, and
// no value from the cancelled settlement bill may leak into the resulting
// monthly bill snapshot. If any cancelled artifact stays visible to
// FindLatestByRoomID or ComputeMonthlyBillSnapshot, the tenant gets an
// incorrect monthly bill. Locks Cancel's atomic revert (EXIT soft-delete
// + settlement VOID) against silent regression from a future refactor.
func TestMoveOutCancel_RevertsExitSettlement_ThenMonthlyBillUsesCorrectPrevious(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 4)
	// Settlement service may not be called by Cancel, but its constructor
	// expects config repo wiring; cleaning + prorate seeded for parity with
	// the production wiring path.
	fixtures.SeedCleaningFeeConfig(t, db, apt.ID.String(), 30000)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000)

	// Month N-1: baseline MONTHLY reading. Post-cancel MONTHLY for Month N
	// MUST anchor on this row's CURRENT values (1100 elec / 110 water).
	monthN1 := "2026-03"
	baseline := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &monthN1,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1100,
		WaterPrevious:       100,
		WaterCurrent:        110,
	}
	if err := db.Create(baseline).Error; err != nil {
		t.Fatalf("seed baseline MONTHLY: %v", err)
	}

	// Move-out notice in PENDING_SETTLEMENT.
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	notice := fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	// EXIT reading for Month N. Currents deliberately != baseline.current so a
	// lineage leak (post-cancel MONTHLY picks EXIT.current instead of
	// baseline.current) would surface as a wrong "previous" in the assertion.
	exit := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeExit,
		ReadingDateActual:   &moveOutDate,
		ElectricityPrevious: 1100,
		ElectricityCurrent:  1200,
		WaterPrevious:       110,
		WaterCurrent:        120,
	}
	if err := db.Create(exit).Error; err != nil {
		t.Fatalf("seed EXIT: %v", err)
	}

	// DRAFT settlement bill with deliberately-distinct quantities. If the
	// post-cancel monthly bill snapshot picked these up, the quantity asserts
	// (50 elec / 5 water) would fail with the 100 / 10 values from here.
	settlementBill := &billing.Bill{
		ContractID:   c.ID,
		BillingMonth: "2026-04",
		BillType:     billing.BillTypeSettlement,
		Status:       billing.BillStatusDraft,
		TotalAmount:  98000,
	}
	if err := db.Create(settlementBill).Error; err != nil {
		t.Fatalf("seed settlement bill: %v", err)
	}
	settlementLines := []billing.BillLineItem{
		{BillID: settlementBill.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 100 หน่วย", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 1},
		{BillID: settlementBill.ID, LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 10 หน่วย", Amount: 18000, Quantity: 10, UnitPrice: 1800, SortOrder: 2},
	}
	if err := db.Create(&settlementLines).Error; err != nil {
		t.Fatalf("seed settlement line items: %v", err)
	}
	if err := db.Model(notice).
		Updates(map[string]any{"settlement_bill_id": settlementBill.ID}).Error; err != nil {
		t.Fatalf("attach settlement to notice: %v", err)
	}

	// ── Wire real services (parity with main.go DI) ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	bcRepo := billingconfig.NewBillingConfigRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)

	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingApplicationChecker{}, txMgr)
	settleAdapter := billing.NewSettlementAdapter(billRepo, billAuditRepo)
	settleSvc := settlement.NewService(settleAdapter, settleAdapter, contractRepo, meterRepo, bcRepo, moveOutRepo, nil, txMgr)
	svc := moveout.NewMoveOutService(moveOutRepo, contractRepo, contractRepo, roomRepo, meterSvc, settleSvc, settleSvc, txMgr)

	// ── Act: cancel ──
	if _, err := svc.Cancel(ctx, notice.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// ── Assert 1: EXIT reading soft-deleted ──
	var exitAfter meterreading.MeterReading
	if err := db.Unscoped().Where("id = ?", exit.ID).First(&exitAfter).Error; err != nil {
		t.Fatalf("reload EXIT: %v", err)
	}
	if !exitAfter.DeletedAt.Valid {
		t.Error("EXIT must be soft-deleted after Cancel — DeletedAt.Valid=false")
	}

	// ── Assert 2: settlement bill VOID with CANCELLED_MOVE_OUT reason ──
	var settleAfter billing.Bill
	if err := db.Where("id = ?", settlementBill.ID).First(&settleAfter).Error; err != nil {
		t.Fatalf("reload settlement bill: %v", err)
	}
	if !settleAfter.IsVoid() {
		t.Errorf("settlement status = %s, want VOID", settleAfter.Status)
	}
	if settleAfter.VoidReason == nil || *settleAfter.VoidReason != "CANCELLED_MOVE_OUT" {
		t.Errorf("settlement void_reason = %v, want CANCELLED_MOVE_OUT", settleAfter.VoidReason)
	}

	// ── Assert 3: lineage query skips the cancelled EXIT ──
	// This is the load-bearing check: if a future refactor stops
	// soft-deleting EXIT (or FindLatestByRoomID stops honoring deleted_at),
	// this assertion fires — and Assert 4/5 would have produced a wrong bill.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after cancel: %v", err)
	}
	if latest.ID != baseline.ID {
		t.Errorf("latest after cancel = %v, want baseline %v (cancelled EXIT must not be visible)", latest.ID, baseline.ID)
	}
	if !latest.IsMonthly() {
		t.Errorf("latest reading_type = %s, want MONTHLY", latest.ReadingType)
	}

	// ── Act: tenant stays — record normal MONTHLY for Month N ──
	monthN := "2026-04"
	newReading, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       monthN,
		ElectricityCurrent: 1150, // baseline.elec.current (1100) + 50 used
		WaterCurrent:       115,  // baseline.water.current (110) + 5 used
	})
	if err != nil {
		t.Fatalf("create post-cancel MONTHLY: %v", err)
	}

	// ── Assert 4: post-cancel MONTHLY previous = baseline.current ──
	if newReading.ElectricityPrevious != baseline.ElectricityCurrent {
		t.Errorf("post-cancel MONTHLY elec previous = %d, want %d (baseline.current — cancelled EXIT must not leak as latest)",
			newReading.ElectricityPrevious, baseline.ElectricityCurrent)
	}
	if newReading.WaterPrevious != baseline.WaterCurrent {
		t.Errorf("post-cancel MONTHLY water previous = %d, want %d (baseline.current — cancelled EXIT must not leak as latest)",
			newReading.WaterPrevious, baseline.WaterCurrent)
	}

	// ── Assert 5: monthly bill line item quantity matches MONTHLY usage ──
	// ElectricityUsed = 1150 - 1100 = 50  (NOT the cancelled EXIT/settlement's 100)
	// WaterUsed       =  115 - 110 =  5   (NOT the cancelled EXIT/settlement's 10)
	snapshot := billing.ComputeMonthlyBillSnapshot(
		monthN,
		c.MonthlyRent,
		c.ElectricityRatePerUnit,
		c.WaterRatePerUnit,
		&newReading.MeterReading,
		nil,
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
	if elecQty != 50 {
		t.Errorf("monthly bill electricity quantity = %d, want 50 (cancelled artifact's 100 must not leak)", elecQty)
	}
	if waterQty != 5 {
		t.Errorf("monthly bill water quantity = %d, want 5 (cancelled artifact's 10 must not leak)", waterQty)
	}
}
