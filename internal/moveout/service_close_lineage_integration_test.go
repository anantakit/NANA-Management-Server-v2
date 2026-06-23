//go:build integration

// External test package — see service_cancel_lineage_integration_test.go
// for the import-cycle rationale (settlement imports moveout for the
// compile-time BillingCommander check).
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

// TestTenantTurnover_ExitToNextMonthly_InheritsExitCurrent is the symmetric
// pair to TestMoveOutCancel_RevertsExitSettlement_ThenMonthlyBillUsesCorrectPrevious.
//
//	Cancel → EXIT must NOT be visible (cancel test)
//	Close  → EXIT MUST carry forward to the next tenant (this test)
//
// Together they pin the meter-lineage invariant:
//
//	Cancelled artifacts disappear from lineage; completed artifacts persist.
//
// Scenario:
//
//	N-1 (2026-03)   : Tenant A baseline MONTHLY
//	N   (2026-04-15): Tenant A EXIT reading  ← lineage anchor for Tenant B
//	                  Notice progresses to READY_TO_CLOSE
//	CloseMoveOut    : contract ENDED, room VACANT, EXIT preserved
//	(new tenant)    : Tenant B contract created on same room
//	N+1 (2026-05)   : Tenant B records first MONTHLY
//	                  previous MUST = EXIT.current (NOT baseline.current)
//
// If a future refactor accidentally soft-deletes the EXIT during close —
// or filters reading_type when looking up the latest — Tenant B's first
// monthly bill would over-bill (anchored on stale baseline instead of EXIT).
// This test fails before that ships.
func TestTenantTurnover_ExitToNextMonthly_InheritsExitCurrent(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Tenant A scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "Y-202")
	tnA := fixtures.SeedTenant(t, db)
	contractA := fixtures.SeedContract(t, db, tnA.ID.String(), rm.ID.String(), 6)

	// Month N-1: baseline MONTHLY (Tenant A). This is what the post-turnover
	// MONTHLY would anchor on IF EXIT lineage broke — deliberately distinct
	// from EXIT.current so a leak would surface in the quantity asserts.
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

	// Month N: EXIT reading. This is the lineage row that MUST carry forward
	// to Tenant B's first MONTHLY (1200 elec / 120 water as previous).
	moveOutDate := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
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

	// Minimal FINALIZED settlement bill — CanClose requires settlement_bill_id
	// to be set on the notice. Bill contents are irrelevant to this test;
	// what matters is the FK linkage so the close guard passes.
	settlementBill := &billing.Bill{
		ContractID:   contractA.ID,
		BillingMonth: "2026-04",
		BillType:     billing.BillTypeSettlement,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  0,
	}
	if err := db.Create(settlementBill).Error; err != nil {
		t.Fatalf("seed settlement bill: %v", err)
	}

	// Notice in READY_TO_CLOSE with settlement bill linked + ZERO_BALANCE outcome.
	// CanClose requires: status=READY_TO_CLOSE AND settlement_bill_id != nil.
	zb := moveout.PaymentOutcomeZeroBalance
	notice := &moveout.MoveOutNotice{
		ContractID:           contractA.ID,
		NoticeDate:           moveOutDate.AddDate(0, 0, -7),
		ScheduledMoveOutDate: moveOutDate,
		ActualMoveOutDate:    &moveOutDate,
		Status:               moveout.MoveOutStatusReadyToClose,
		PaymentOutcome:       &zb,
		SettlementBillID:     &settlementBill.ID,
	}
	if err := db.Create(notice).Error; err != nil {
		t.Fatalf("seed notice READY_TO_CLOSE: %v", err)
	}

	// ── Wire real services (parity with main.go) ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	bcRepo := billingconfig.NewBillingConfigRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)

	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingAdjustmentCommander{}, txMgr)
	settleAdapter := billing.NewSettlementAdapter(billRepo, billAuditRepo)
	settleSvc := settlement.NewService(settleAdapter, settleAdapter, contractRepo, meterRepo, bcRepo, moveOutRepo, nil, txMgr)
	svc := moveout.NewMoveOutService(moveOutRepo, contractRepo, contractRepo, roomRepo, meterSvc, settleSvc, settleSvc, txMgr)

	// ── Act 1: close the move-out ──
	if _, err := svc.CloseMoveOut(ctx, notice.ID); err != nil {
		t.Fatalf("CloseMoveOut: %v", err)
	}

	// ── Assert 1: notice COMPLETED ──
	var noticeAfter moveout.MoveOutNotice
	if err := db.Where("id = ?", notice.ID).First(&noticeAfter).Error; err != nil {
		t.Fatalf("reload notice: %v", err)
	}
	if !noticeAfter.IsCompleted() {
		t.Errorf("notice status = %s, want COMPLETED", noticeAfter.Status)
	}

	// ── Assert 2: EXIT reading STILL ACTIVE — invariant opposite of Cancel ──
	// If a future refactor adds a DeleteExitByRoomID call to CloseMoveOut
	// (mirroring Cancel by mistake), this assert fires and the lineage check
	// below would have silently broken.
	var exitAfter meterreading.MeterReading
	if err := db.Unscoped().Where("id = ?", exit.ID).First(&exitAfter).Error; err != nil {
		t.Fatalf("reload EXIT: %v", err)
	}
	if exitAfter.DeletedAt.Valid {
		t.Errorf("EXIT must remain active after CloseMoveOut — got DeletedAt.Valid=true (close should NEVER soft-delete EXIT, only Cancel does)")
	}

	// ── Assert 3: contract ENDED, room VACANT ──
	var contractAAfter contract.Contract
	if err := db.Where("id = ?", contractA.ID).First(&contractAAfter).Error; err != nil {
		t.Fatalf("reload contract A: %v", err)
	}
	if contractAAfter.Status != contract.ContractStatusEnded {
		t.Errorf("contract A status = %s, want ENDED", contractAAfter.Status)
	}
	var roomAfter room.Room
	if err := db.Where("id = ?", rm.ID).First(&roomAfter).Error; err != nil {
		t.Fatalf("reload room: %v", err)
	}
	if roomAfter.Status != room.RoomStatusVacant {
		t.Errorf("room status = %s, want VACANT", roomAfter.Status)
	}

	// ── Assert 4: lineage query returns the EXIT (load-bearing) ──
	// This is what populatePrevious will see when Tenant B records a MONTHLY.
	// If FindLatestByRoomID returns baseline instead, the bug is here.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after close: %v", err)
	}
	if latest.ID != exit.ID {
		t.Errorf("latest after close = %v, want EXIT %v (close must NOT hide EXIT from lineage)", latest.ID, exit.ID)
	}
	if !latest.IsExit() {
		t.Errorf("latest reading_type = %s, want EXIT", latest.ReadingType)
	}

	// ── Act 2: Tenant B moves into the same room ──
	// Direct insert (not via fixture) so StartDate is anchored to moveOutDate,
	// not time.Now(); test stays deterministic across calendar drift.
	tnB := fixtures.SeedTenant(t, db)
	contractB := &contract.Contract{
		TenantID:               tnB.ID,
		RoomID:                 rm.ID,
		StartDate:              moveOutDate.AddDate(0, 0, 5), // 2026-04-20
		MinMonths:              6,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(contractB).Error; err != nil {
		t.Fatalf("seed Tenant B contract: %v", err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		t.Fatalf("re-occupy room: %v", err)
	}

	// ── Act 3: Tenant B records their first MONTHLY for Month N+1 ──
	monthNext := "2026-05"
	newReading, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       monthNext,
		ElectricityCurrent: 1250, // EXIT.current (1200) + 50 used
		WaterCurrent:       130,  // EXIT.current (120) + 10 used
	})
	if err != nil {
		t.Fatalf("create Tenant B first MONTHLY: %v", err)
	}

	// ── Assert 5: Tenant B's MONTHLY previous = EXIT.current ──
	// This is the carryover proof. If EXIT was soft-deleted or filtered out
	// of FindLatestByRoomID, previous would fall back to baseline (1100/110)
	// and over-bill Tenant B by 100 elec + 10 water units.
	if newReading.ElectricityPrevious != exit.ElectricityCurrent {
		t.Errorf("Tenant B MONTHLY elec previous = %d, want %d (EXIT.current — turnover MUST inherit EXIT, not skip back to baseline)",
			newReading.ElectricityPrevious, exit.ElectricityCurrent)
	}
	if newReading.WaterPrevious != exit.WaterCurrent {
		t.Errorf("Tenant B MONTHLY water previous = %d, want %d (EXIT.current — turnover MUST inherit EXIT, not skip back to baseline)",
			newReading.WaterPrevious, exit.WaterCurrent)
	}

	// ── Assert 6: monthly bill snapshot quantities reflect EXIT→MONTHLY usage ──
	// elec usage = 1250 - 1200 = 50  (would be 150 if EXIT were skipped → baseline 1100)
	// water usage =  130 -  120 = 10 (would be  20 if EXIT were skipped → baseline 110)
	snapshot := billing.ComputeMonthlyBillSnapshot(
		monthNext,
		contractB.MonthlyRent,
		contractB.ElectricityRatePerUnit,
		contractB.WaterRatePerUnit,
		&newReading.MeterReading,
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
		t.Errorf("Tenant B bill electricity quantity = %d, want 50 (anchor on EXIT.current=1200 — if it were 150, EXIT was skipped and lineage fell back to baseline 1100)", elecQty)
	}
	if waterQty != 10 {
		t.Errorf("Tenant B bill water quantity = %d, want 10 (anchor on EXIT.current=120 — if it were 20, EXIT was skipped and lineage fell back to baseline 110)", waterQty)
	}
}
