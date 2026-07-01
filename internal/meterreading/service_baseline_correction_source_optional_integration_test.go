//go:build integration

package meterreading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_NilSource_CommitsPersistsInheritsAndListsPending is the primary
// integration anchor for the source-optional relaxation (locked 2026-07-01):
//
//	nil-source correction commits → recovery row persists with NULL FK →
//	FindLatestByRoomID returns it → next MONTHLY inherits its current →
//	the correction APPEARS in ListPending (it no longer vanishes).
//
// The room has an active contract, so nil-path settlement safety (§1.4)
// passes. No source is supplied — absence is a valid, complete recovery.
func TestRecovery_NilSource_CommitsPersistsInheritsAndListsPending(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "SO-101")
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

	// Commit a recovery with NO source — only the room anchor carries identity.
	recovery, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    nil, // no source supplied
		RoomID:             &rm.ID,
		ElectricityCurrent: 450,
		WaterCurrent:       60,
		AnchorNote:         "รีเซ็ตฐานมิเตอร์ — ไม่ทราบเดือนต้นทาง",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (nil source): %v", err)
	}

	// Persisted with NULL FK — no source was invented.
	got, err := meterRepo.FindByIDSimple(ctx, recovery.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple: %v", err)
	}
	if got.RecoverySourceReadingID != nil {
		t.Errorf("RecoverySourceReadingID=%v, want nil — nil source must persist as NULL FK", got.RecoverySourceReadingID)
	}
	if got.AnchorReason == nil || *got.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("AnchorReason=%v, want READING_RECOVERY", got.AnchorReason)
	}

	// Lineage sees the nil-source recovery like any other anchor.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != recovery.ID {
		t.Fatalf("FindLatestByRoomID=%v, want recovery %v — nil-source recovery MUST be in lineage", latest.ID, recovery.ID)
	}

	// Next month inherits recovery.Current as previous (structural).
	nextMonth := time.Now().AddDate(0, 1, 0).Format("2006-01")
	next := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &nextMonth,
		ElectricityPrevious: latest.ElectricityCurrent,
		ElectricityCurrent:  latest.ElectricityCurrent + 30,
		WaterPrevious:       latest.WaterCurrent,
		WaterCurrent:        latest.WaterCurrent + 5,
	}
	if err := meterRepo.Create(ctx, next); err != nil {
		t.Fatalf("hand-seed next month: %v", err)
	}
	if next.ElectricityPrevious != recovery.ElectricityCurrent {
		t.Errorf("next.ElectricityPrevious=%d, want recovery.ElectricityCurrent=%d", next.ElectricityPrevious, recovery.ElectricityCurrent)
	}

	// The nil-source correction APPEARS in ListPending with an empty source
	// block (not skipped, not crashed).
	pending, err := meterSvc.ListPendingBaselineCorrectionsByRoom(ctx, rm.ID)
	if err != nil {
		t.Fatalf("ListPendingBaselineCorrectionsByRoom: %v", err)
	}
	var found *meterreading.PendingBaselineCorrection
	for i := range pending {
		if pending[i].RecoveryID == recovery.ID {
			found = &pending[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nil-source recovery %v missing from ListPending — it must appear, not vanish", recovery.ID)
	}
	if found.SourceReadingID != uuid.Nil {
		t.Errorf("SourceReadingID=%v, want uuid.Nil for nil-source row", found.SourceReadingID)
	}
	if found.SourceBillingMonth != "" {
		t.Errorf("SourceBillingMonth=%q, want empty for nil-source row", found.SourceBillingMonth)
	}
	if found.SourceElectricity != 0 || found.SourceWater != 0 {
		t.Errorf("Source readings=(%d,%d), want (0,0) for nil-source row", found.SourceElectricity, found.SourceWater)
	}
	if found.RecoveryElectricity != 450 || found.RecoveryWater != 60 {
		t.Errorf("Recovery readings=(%d,%d), want (450,60)", found.RecoveryElectricity, found.RecoveryWater)
	}
}

// TestRecovery_NilSource_NoActiveContract_Rejected locks nil-path settlement
// safety (§1.4): a nil-source correction on a room with NO active contract is
// rejected. There is no live billing relationship to carry the correction
// forward, so the recovery must not commit.
func TestRecovery_NilSource_NoActiveContract_Rejected(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "SO-201")
	// Deliberately NO contract seeded → FindActiveContractIDByRoomID is empty.

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)

	_, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    nil,
		RoomID:             &rm.ID,
		ElectricityCurrent: 100,
		WaterCurrent:       20,
		AnchorNote:         "ไม่มีสัญญา — ต้องถูกปฏิเสธ",
	})
	if err == nil {
		t.Fatal("CreateBaselineCorrection succeeded on a room with no active contract, want rejection")
	}
	if !errors.Is(err, meterreading.ErrBaselineCorrectionSettlementBoundaryCrossed) {
		t.Errorf("err=%v, want ErrBaselineCorrectionSettlementBoundaryCrossed", err)
	}

	// Nothing persisted.
	if _, err := meterRepo.FindLatestByRoomID(ctx, rm.ID); !database.IsNotFound(err) {
		t.Errorf("FindLatestByRoomID err=%v, want not-found — nothing should have committed", err)
	}
}
