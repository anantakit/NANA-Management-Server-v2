//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_NilSource_NextMonthInheritsThroughRealPath is the ontology
// anchor the source-optional migration hinges on:
//
//	lineage is determined by the RECOVERY ROW itself, never by the existence
//	of a source. A nil-source recovery must still be the lineage parent of
//	the following month.
//
// Scenario (all through the PRODUCTION path — no hand-assigned previous):
//
//	Month 1: normal MONTHLY reading (elec 700).
//	Month 2: RECOVERY(source = nil), elec current 1000.
//	Month 3: normal MONTHLY reading via meterSvc.Create.
//	Expect:  Month 3.previous == 1000  (recovery.current, not Month 1).
//
// Month 3 goes through meterSvc.Create → findLatest → NewReading →
// populatePrevious(latest=recovery). If a regression ever hid nil-source
// recovery rows from FindLatestByRoomID, Month 3 would inherit Month 1's 700
// and this test fails immediately.
func TestRecovery_NilSource_NextMonthInheritsThroughRealPath(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "NSL-101")
	tn := fixtures.SeedTenant(t, db)
	fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)

	// Month 1: normal reading, one month before the recovery month.
	month1 := time.Now().AddDate(0, -1, 0).Format("2006-01")
	m1 := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &month1,
		ElectricityPrevious: 500,
		ElectricityCurrent:  700,
		WaterPrevious:       30,
		WaterCurrent:        40,
	}
	if err := meterRepo.Create(ctx, m1); err != nil {
		t.Fatalf("seed month 1: %v", err)
	}

	// Month 2: RECOVERY with NO source, elec current 1000 / water current 60.
	recovery, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    nil,
		RoomID:             &rm.ID,
		ElectricityCurrent: 1000,
		WaterCurrent:       60,
		AnchorNote:         "รีเซ็ตฐาน — ไม่ทราบเดือนต้นทาง",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (nil source): %v", err)
	}
	if recovery.RecoverySourceReadingID != nil {
		t.Fatalf("recovery source = %v, want nil", recovery.RecoverySourceReadingID)
	}

	// Month 3: normal reading via the REAL service path (findLatest → NewReading).
	month3 := time.Now().AddDate(0, 1, 0).Format("2006-01")
	created, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       month3,
		ElectricityCurrent: 1050,
		WaterCurrent:       65,
	})
	if err != nil {
		t.Fatalf("create month 3: %v", err)
	}

	// The ontology invariant: Month 3 inherits the RECOVERY row's current,
	// NOT Month 1's — proving the nil-source recovery is the lineage parent.
	if created.ElectricityPrevious != recovery.ElectricityCurrent {
		t.Errorf("month3.electricity_previous = %d, want %d (recovery.current); Month1 was 700",
			created.ElectricityPrevious, recovery.ElectricityCurrent)
	}
	if created.WaterPrevious != recovery.WaterCurrent {
		t.Errorf("month3.water_previous = %d, want %d (recovery.current)",
			created.WaterPrevious, recovery.WaterCurrent)
	}

	// Roundtrip persistence: re-read Month 3 confirms the inherited previous.
	got, err := meterRepo.FindByIDSimple(ctx, created.ID)
	if err != nil {
		t.Fatalf("re-read month 3: %v", err)
	}
	if got.ElectricityPrevious != 1000 || got.WaterPrevious != 60 {
		t.Errorf("persisted month3 previous = (%d, %d), want (1000, 60)",
			got.ElectricityPrevious, got.WaterPrevious)
	}
}
