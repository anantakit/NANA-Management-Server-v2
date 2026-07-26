//go:build integration

package meterreading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// buildRecoveryServices wires the meter-reading service the way the Q1.6
// recovery flow uses it, plus the billing repo so tests can probe the
// freshness gate directly. Replaces the deleted Phase-7 buildPhase7Services
// helper (which returned the removed pending-list surface).
func buildRecoveryServices(t *testing.T, db *gorm.DB) (meterreading.MeterReadingService, billing.BillingRepository) {
	t.Helper()
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)
	return meterSvc, billRepo
}

// seedSourceReading inserts the prior MONTHLY reading whose CURRENT is the
// wrong-high value the operator later corrects — Q1.6 derives the over-record
// from source.current, so this row is the evidence of how much was mis-recorded.
func seedSourceReading(t *testing.T, db *gorm.DB, contractID, roomID uuid.UUID, elecCur, waterCur int) *meterreading.MeterReading {
	t.Helper()
	srcMonth := time.Now().AddDate(0, -1, 0).Format("2006-01")
	src := &meterreading.MeterReading{
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: elecCur - 200,
		ElectricityCurrent:  elecCur,
		WaterPrevious:       waterCur - 10,
		WaterCurrent:        waterCur,
	}
	if err := db.Create(src).Error; err != nil {
		t.Fatalf("seed source reading: %v", err)
	}
	// The source month was billed (ontology lock 2026-07-08): a FINALIZED bill so
	// the over-record maps to real money → the forward credit + freshness gate
	// engage. Electricity charged at 800/unit (the historical rate).
	sb := &billing.Bill{ContractID: contractID, BillingMonth: srcMonth, BillType: billing.BillTypeMonthly, Status: billing.BillStatusFinalized}
	if err := db.Create(sb).Error; err != nil {
		t.Fatalf("seed source bill: %v", err)
	}
	sbLines := []billing.BillLineItem{
		{BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 200 * 800, Quantity: 200, UnitPrice: 800, SortOrder: 2},
		{BillID: sb.ID, LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 0, Quantity: 0, UnitPrice: 1800, SortOrder: 3},
	}
	if err := db.Create(&sbLines).Error; err != nil {
		t.Fatalf("seed source bill lines: %v", err)
	}
	return src
}

// TestRecovery_SoftDeleteClearsFreshnessGate locks the Q1.6 soft-delete +
// applied-guard behavior against the freshness gate, AND the per-utility
// derivation: the source's electricity current (600) exceeds the physical (450)
// → an over-record on electricity only; water (source 80, physical 80) is NOT
// over-recorded → its recorded stays NULL and no refund is owed on water.
//
//  1. An over-record recovery not yet reflected on any live bill can be
//     soft-deleted; the gate then clears (the row is gone).
//  2. A recovery whose refund IS reflected on a live bill CANNOT be
//     soft-deleted — ErrCorrectionAlreadyApplied; reverse via VOID + GENERATE.
func TestRecovery_SoftDeleteClearsFreshnessGate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P3-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billRepo := buildRecoveryServices(t, db)
	meterRepo := meterreading.NewMeterReadingRepository(db)

	// Source: last month recorded electricity 600 (wrong) and water 80. The
	// operator's physical reading now is elec 450 (over-record 150) / water 80
	// (not an over-record).
	src := seedSourceReading(t, db, con.ID, rm.ID, 600, 80)
	rec, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    &src.ID,
		ElectricityCurrent: 450,
		WaterCurrent:       80,
		AnchorNote:         "จดไฟฟ้าเกิน — น้ำถูกต้อง",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Per-utility derivation: electricity recorded set to source.current (600);
	// water recorded stays NULL (not over-recorded).
	got, err := meterRepo.FindByIDSimple(ctx, rec.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple: %v", err)
	}
	if got.ElectricityRecorded == nil || *got.ElectricityRecorded != 600 {
		t.Errorf("ElectricityRecorded = %v, want 600 (derived from source)", got.ElectricityRecorded)
	}
	if got.WaterRecorded != nil {
		t.Errorf("WaterRecorded = %v, want nil (water not over-recorded)", got.WaterRecorded)
	}

	// Before any bill reflects it, the contract's bills are stale.
	stale, err := billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe (before delete): %v", err)
	}
	if !stale {
		t.Fatal("gate = false before any bill reflects the recovery, want true (stale)")
	}

	// Soft-delete the unreflected recovery — allowed (Edit-via-Delete).
	if err := meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, rec.ID, nil); err != nil {
		t.Fatalf("SoftDelete on unreflected recovery: %v", err)
	}

	// Gate clears: no live over-record recovery remains for the contract.
	stale, err = billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe (after delete): %v", err)
	}
	if stale {
		t.Fatal("gate = true after soft-deleting the recovery, want false (nothing to reflect)")
	}

	// --- Applied-guard: a recovery reflected on a live bill is immutable. ---
	rec2, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    &src.ID,
		ElectricityCurrent: 450,
		WaterCurrent:       80,
		AnchorNote:         "จดเกินอีกครั้ง — จะออกบิล",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (second): %v", err)
	}

	// Hand-seed a live DRAFT bill carrying the refund ADJUSTMENT that would be
	// auto-emitted at generation (P2). This makes the recovery "reflected".
	recoveryMonth := time.Now().Format("2006-01")
	bill := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: recoveryMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	reason := billing.AdjustmentReasonMeterRecovery
	elecUtil := billing.AdjustmentUtilityElectricity
	if err := db.Create(&billing.BillLineItem{
		BillID:                      bill.ID,
		LineType:                    billing.LineItemAdjustment,
		Source:                      billing.LineItemSourceManual,
		Description:                 "คืนค่าไฟฟ้า",
		Amount:                      -(600 - 450) * 800,
		SortOrder:                   99,
		AdjustmentRecoveryReadingID: &rec2.ID,
		AdjustmentUtility:           &elecUtil,
		AdjustmentReasonCode:        &reason,
	}).Error; err != nil {
		t.Fatalf("seed adjustment line: %v", err)
	}

	// Now soft-deleting the reflected recovery must be rejected.
	err = meterSvc.SoftDeletePendingBaselineCorrection(ctx, apt.ID, rm.ID, rec2.ID, nil)
	if !errors.Is(err, meterreading.ErrCorrectionAlreadyApplied) {
		t.Fatalf("SoftDelete on reflected recovery = %v, want ErrCorrectionAlreadyApplied", err)
	}
}

// TestRecovery_NilSourceReAnchorsWithoutRefund locks the revised Q1.6
// source-optional doctrine: a source-less recovery is a PURE re-anchor. With no
// source there is no evidence of a mis-recorded value, so recorded stays NULL —
// it does NOT engage the freshness gate and produces no refund. It still
// persists, sits in lineage, and re-baselines the next month.
func TestRecovery_NilSourceReAnchorsWithoutRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P3-102")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, billRepo := buildRecoveryServices(t, db)
	meterRepo := meterreading.NewMeterReadingRepository(db)

	// Source-less recovery — only the room anchor carries identity.
	rec, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    nil, // no source → re-anchor only
		RoomID:             &rm.ID,
		ElectricityCurrent: 450,
		WaterCurrent:       80,
		AnchorNote:         "รีเซ็ตฐาน — ไม่มี source",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection (nil source): %v", err)
	}

	got, err := meterRepo.FindByIDSimple(ctx, rec.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple: %v", err)
	}
	// Persisted with NULL source FK + NULL recorded (no over-record).
	if got.RecoverySourceReadingID != nil {
		t.Errorf("RecoverySourceReadingID = %v, want nil (NULL FK)", got.RecoverySourceReadingID)
	}
	if got.ElectricityRecorded != nil || got.WaterRecorded != nil {
		t.Errorf("recorded = elec %v / water %v, want both nil (source-less = no over-record)",
			got.ElectricityRecorded, got.WaterRecorded)
	}
	if got.AnchorReason == nil || *got.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("AnchorReason = %v, want READING_RECOVERY", got.AnchorReason)
	}

	// Lineage sees the nil-source recovery like any other anchor.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != rec.ID {
		t.Fatalf("FindLatestByRoomID = %v, want recovery %v", latest.ID, rec.ID)
	}

	// The freshness gate does NOT engage — a source-less recovery owes no refund.
	stale, err := billRepo.HasUnreflectedOverRecordByContractID(ctx, con.ID)
	if err != nil {
		t.Fatalf("gate probe: %v", err)
	}
	if stale {
		t.Fatal("source-less recovery gate = true, want false (re-anchor only, no refund)")
	}

	// Next month inherits the recovery's current as its previous (re-baseline).
	nextMonth := time.Now().AddDate(0, 1, 0).Format("2006-01")
	next := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &nextMonth,
		ElectricityPrevious: got.ElectricityCurrent,
		ElectricityCurrent:  480,
		WaterPrevious:       got.WaterCurrent,
		WaterCurrent:        92,
	}
	if err := meterRepo.Create(ctx, next); err != nil {
		t.Fatalf("seed next month: %v", err)
	}
	if next.ElectricityPrevious != got.ElectricityCurrent {
		t.Errorf("next.ElectricityPrevious = %d, want recovery.ElectricityCurrent = %d",
			next.ElectricityPrevious, got.ElectricityCurrent)
	}
}

// TestRecovery_SourcedCorrectionProducesRefund is the P5A chain proof: a
// sourced correction created through the real service (source.current 600 >
// physical 450 → derived recorded 600) yields a recovery reading that
// ComputeMonthlyBillSnapshot turns into an auto-refund of (600−450)×rate, with
// the affected electricity AUTO line re-baselined to 0. This is the link that
// was broken before P5A (the UI-driven correction never set recorded, so the
// refund was unreachable).
func TestRecovery_SourcedCorrectionProducesRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P3-103")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	meterSvc, _ := buildRecoveryServices(t, db)
	meterRepo := meterreading.NewMeterReadingRepository(db)

	src := seedSourceReading(t, db, con.ID, rm.ID, 600, 80) // recorded electricity 600
	rec, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    &src.ID,
		ElectricityCurrent: 450, // physical → over-record 150
		WaterCurrent:       80,  // not over-recorded
		AnchorNote:         "จดไฟฟ้าเกิน 150 หน่วย",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}
	got, err := meterRepo.FindByIDSimple(ctx, rec.ID)
	if err != nil {
		t.Fatalf("reload recovery: %v", err)
	}

	// Generation-side: the recovery reading emits a refund per the derived recorded,
	// priced at the source-bill rate the caller resolves (ontology lock 2026-07-08).
	// Here F is applicable at rate 800 (the source month was billed).
	month := time.Now().Format("2006-01")
	srcRate := int64(800)
	snap := billing.ComputeMonthlyBillSnapshot(month, 300000, 800, 1800, got, nil,
		&billing.RecoveryReconciliation{Electricity: &srcRate})

	var refund, elec *billing.ComputedLineItem
	for i := range snap.LineItems {
		li := &snap.LineItems[i]
		switch li.Type {
		case billing.LineItemAdjustment:
			refund = li
		case billing.LineItemElectricity:
			elec = li
		}
	}
	if refund == nil {
		t.Fatal("no refund ADJUSTMENT emitted from the sourced correction")
	}
	if refund.Amount != -(600-450)*800 {
		t.Errorf("refund amount = %d, want %d", refund.Amount, -(600-450)*800)
	}
	if refund.AdjustmentUtility == nil || *refund.AdjustmentUtility != billing.AdjustmentUtilityElectricity {
		t.Errorf("refund utility = %v, want ELECTRICITY", refund.AdjustmentUtility)
	}
	if elec == nil || elec.Amount != 0 {
		t.Errorf("electricity AUTO = %+v, want amount 0 (re-baselined)", elec)
	}
	// Water was not over-recorded → no second refund line.
	count := 0
	for i := range snap.LineItems {
		if snap.LineItems[i].Type == billing.LineItemAdjustment {
			count++
		}
	}
	if count != 1 {
		t.Errorf("adjustment line count = %d, want 1 (electricity only, water not over-recorded)", count)
	}
}
