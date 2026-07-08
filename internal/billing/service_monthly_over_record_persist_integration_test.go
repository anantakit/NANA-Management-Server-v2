//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestCreateMonthlyBill_OverRecord_PersistsRefund is the Q1.6 end-to-end money
// guard (ontology lock 2026-07-08): generating a monthly bill from an over-record
// recovery whose SOURCE month was actually billed must PERSIST an auto refund
// ADJUSTMENT line — and price it at the SOURCE bill's rate, not the current
// contract rate. The source month here is billed at 700 while the contract's
// current rate is 800, so the amount proves the historical rate is used.
func TestCreateMonthlyBill_OverRecord_PersistsRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "OR-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6) // current rate ฿8 (800) elec

	billRepo := NewBillingRepository(db)
	auditRepo := NewBillAuditRepository(db)
	contracts := &integrationContractStub{c: c}
	meters := meterreading.NewMeterReadingRepository(db)
	configs := &integrationConfigStub{}
	txMgr := database.NewTxManager(db)
	svc := NewBillingService(billRepo, auditRepo, contracts, meters, configs, nil, txMgr)

	// Source month (2026-06): the wrong-high 1500 was recorded AND billed. The
	// source bill's electricity line was charged at 700 — deliberately distinct
	// from the contract's current 800 to prove the refund uses the SOURCE rate.
	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500,
		WaterPrevious: 80, WaterCurrent: 100,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source reading: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1800)

	// Recovery month (2026-07): physical 1200, recorded 1500 (derived from the
	// source), sourced back to the June reading → 300 units over-charged.
	curMonth := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	anchorNote := "จดเกิน — physical 1200 recorded 1500"
	reading := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &anchorNote, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1200, ElectricityCurrent: 1200, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(reading).Error; err != nil {
		t.Fatalf("seed recovery reading: %v", err)
	}

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: curMonth, MeterReadingID: reading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill (over-record): %v", err)
	}

	// Reload from DB — assert the persisted rows, not the in-memory snapshot.
	persisted, err := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if err != nil {
		t.Fatalf("reload persisted bill: %v", err)
	}
	var refund, elec *BillLineItem
	for i := range persisted.LineItems {
		li := &persisted.LineItems[i]
		switch li.LineType {
		case LineItemAdjustment:
			refund = li
		case LineItemElectricity:
			elec = li
		}
	}

	if refund == nil {
		t.Fatal("no refund ADJUSTMENT line persisted for the over-record")
	}
	// Priced at the SOURCE bill rate (700), NOT the current contract rate (800).
	if refund.Amount != -(300 * 700) {
		t.Errorf("refund amount = %d, want %d (source rate 700, not current 800)", refund.Amount, -(300 * 700))
	}
	if refund.UnitPrice != 700 {
		t.Errorf("refund unit_price = %d, want 700 (source-bill rate)", refund.UnitPrice)
	}
	if refund.Source != LineItemSourceManual {
		t.Errorf("refund source = %s, want MANUAL", refund.Source)
	}
	if refund.AdjustmentNote != nil {
		t.Errorf("refund note = %v, want nil (Q1.6 refunds carry no operator note)", *refund.AdjustmentNote)
	}
	if refund.AdjustmentReasonCode == nil || *refund.AdjustmentReasonCode != AdjustmentReasonMeterRecovery {
		t.Errorf("refund reason = %v, want METER_RECOVERY", refund.AdjustmentReasonCode)
	}
	if refund.AdjustmentRecoveryReadingID == nil || *refund.AdjustmentRecoveryReadingID != reading.ID {
		t.Errorf("refund FK = %v, want %v", refund.AdjustmentRecoveryReadingID, reading.ID)
	}
	if refund.AdjustmentUtility == nil || *refund.AdjustmentUtility != AdjustmentUtilityElectricity {
		t.Errorf("refund utility = %v, want ELECTRICITY", refund.AdjustmentUtility)
	}
	// The electricity AUTO line is re-baselined to 0 (usage 0) — the refund is
	// a separate line, not an offset against consumption.
	if elec == nil || elec.Amount != 0 {
		t.Errorf("electricity AUTO = %+v, want amount 0 (usage 0)", elec)
	}
}

// TestCreateMonthlyBill_OverRecord_UnbilledSource_NoRefund is the S0 gate guard
// (ontology lock 2026-07-08): an over-record whose source month was NEVER billed
// must persist NO refund (money never moved → no phantom credit) AND must not
// leave the contract permanently stale-blocked (a P-only re-anchor has nothing
// to reflect).
func TestCreateMonthlyBill_OverRecord_UnbilledSource_NoRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "OR-102")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := NewBillingRepository(db)
	auditRepo := NewBillAuditRepository(db)
	contracts := &integrationContractStub{c: c}
	meters := meterreading.NewMeterReadingRepository(db)
	configs := &integrationConfigStub{}
	txMgr := database.NewTxManager(db)
	svc := NewBillingService(billRepo, auditRepo, contracts, meters, configs, nil, txMgr)

	// Source reading exists, but its month was NEVER billed (no seedFinalizedMonthlyBill).
	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500,
		WaterPrevious: 80, WaterCurrent: 100,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source reading: %v", err)
	}

	curMonth := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	anchorNote := "จดเกิน — แต่เดือนต้นทางยังไม่ออกบิล"
	reading := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &anchorNote, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1200, ElectricityCurrent: 1200, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(reading).Error; err != nil {
		t.Fatalf("seed recovery reading: %v", err)
	}

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: curMonth, MeterReadingID: reading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill (unbilled source): %v", err)
	}

	persisted, err := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if err != nil {
		t.Fatalf("reload persisted bill: %v", err)
	}
	for i := range persisted.LineItems {
		if persisted.LineItems[i].LineType == LineItemAdjustment {
			t.Errorf("unbilled source (S0) must persist NO refund, got %+v", persisted.LineItems[i])
		}
	}

	// The freshness gate must NOT flag the contract stale: an unbilled-source
	// over-record is P-only, so there is nothing to reflect and nothing to block.
	stale, err := billRepo.HasUnreflectedOverRecordByContractID(ctx, c.ID)
	if err != nil {
		t.Fatalf("HasUnreflectedOverRecordByContractID: %v", err)
	}
	if stale {
		t.Error("unbilled-source recovery must not leave the contract stale-blocked (S0)")
	}
}

// seedFinalizedMonthlyBill inserts a FINALIZED MONTHLY bill for (contract, month)
// with electricity/water AUTO lines at the given unit rates — the historical
// charge a forward credit refunds against.
func seedFinalizedMonthlyBill(t *testing.T, db *gorm.DB, contractID uuid.UUID, month string, elecRate, waterRate int64) {
	t.Helper()
	b := &Bill{ContractID: contractID, BillingMonth: month, BillType: BillTypeMonthly, Status: BillStatusFinalized}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("seed source bill: %v", err)
	}
	lines := []BillLineItem{
		{BillID: b.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 500 * elecRate, Quantity: 500, UnitPrice: elecRate, SortOrder: 2},
		{BillID: b.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 20 * waterRate, Quantity: 20, UnitPrice: waterRate, SortOrder: 3},
	}
	if err := db.Create(&lines).Error; err != nil {
		t.Fatalf("seed source bill lines: %v", err)
	}
}
