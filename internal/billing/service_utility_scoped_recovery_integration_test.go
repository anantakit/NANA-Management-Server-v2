//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// Utility-scoped recovery overlay regression suite (owner lock 2026-07-18).
//
// Defect: when a READING_RECOVERY anchor coexists with a real (non-anchor)
// MONTHLY consumption row for the same month, monthly billing used to pick the
// anchor as the WHOLE-ROW winner → the utility NOT over-recorded billed 0 usage
// and its real consumption was permanently lost. The fix makes recovery a
// utility-scoped overlay: the affected utility keeps the anchor's 0-usage +
// refund; every other utility bills its real usage from the consumption row.
//
// All four cases generate through CreateMonthlyBill with MeterReadingID = the
// recovery anchor (the reconciliation-adapter/batch selector's winner), so they
// exercise the projection inside buildMonthlyDraftBill AFTER the ID re-fetch.

// lineByType returns the first line item of the given type, or nil.
func lineByType(lines []BillLineItem, lt LineItemType) *BillLineItem {
	for i := range lines {
		if lines[i].LineType == lt {
			return &lines[i]
		}
	}
	return nil
}

// A — ELECTRICITY recovery + real WATER usage: elec 0 + refund once, water 8 / ฿144.
func TestUtilityScopedRecovery_A_ElecRecovery_RealWater(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "US-A01")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6) // elec 800, water 1800

	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500, WaterPrevious: 200, WaterCurrent: 220,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1800) // source elec rate 700 → refund rate

	curMonth := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	note := "จดไฟฟ้าเกิน — physical 1240 recorded 1500"
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &note, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 220, WaterCurrent: 220,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	// The coexisting REAL consumption reading: water actually advanced 220→228.
	consumption := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, WaterPrevious: 220, WaterCurrent: 228,
	}
	if err := db.Create(consumption).Error; err != nil {
		t.Fatalf("seed consumption: %v", err)
	}

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: curMonth, MeterReadingID: rec.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	bill, err := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	water := lineByType(bill.LineItems, LineItemWater)
	if water == nil || water.Quantity != 8 || water.Amount != 8*1800 {
		t.Errorf("water = %+v, want qty 8 / ฿%d (real usage, not suppressed)", water, 8*1800)
	}
	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec == nil || elec.Quantity != 0 || elec.Amount != 0 {
		t.Errorf("elec = %+v, want qty 0 / ฿0 (recovery-anchored)", elec)
	}
	assertSingleRefund(t, bill.LineItems, AdjustmentUtilityElectricity, -(260 * 700), rec.ID)
}

// B — WATER recovery + real ELECTRICITY usage: mirror of A.
func TestUtilityScopedRecovery_B_WaterRecovery_RealElec(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "US-B01")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 400, ElectricityCurrent: 500, WaterPrevious: 200, WaterCurrent: 300,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1500) // source water rate 1500 → refund rate

	curMonth := "2026-07"
	waterRecorded := 300
	anchor := meterreading.AnchorReasonReadingRecovery
	note := "จดน้ำเกิน — physical 240 recorded 300"
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &note, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 500, ElectricityCurrent: 500,
		WaterPrevious: 240, WaterCurrent: 240, WaterRecorded: &waterRecorded,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	consumption := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		ElectricityPrevious: 500, ElectricityCurrent: 515, WaterPrevious: 240, WaterCurrent: 240,
	}
	if err := db.Create(consumption).Error; err != nil {
		t.Fatalf("seed consumption: %v", err)
	}

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: curMonth, MeterReadingID: rec.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	bill, err := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec == nil || elec.Quantity != 15 || elec.Amount != 15*800 {
		t.Errorf("elec = %+v, want qty 15 / ฿%d (real usage)", elec, 15*800)
	}
	water := lineByType(bill.LineItems, LineItemWater)
	if water == nil || water.Quantity != 0 || water.Amount != 0 {
		t.Errorf("water = %+v, want qty 0 / ฿0 (recovery-anchored)", water)
	}
	assertSingleRefund(t, bill.LineItems, AdjustmentUtilityWater, -(60 * 1500), rec.ID)
}

// C — recovery on BOTH utilities: both dimensions use recovery (0 + refund each),
// no real usage adopted for either.
func TestUtilityScopedRecovery_C_BothUtilities(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "US-C01")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500, WaterPrevious: 200, WaterCurrent: 300,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1500)

	curMonth := "2026-07"
	er, wr := 1500, 300
	anchor := meterreading.AnchorReasonReadingRecovery
	note := "จดเกินทั้งไฟและน้ำ"
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &note, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &er,
		WaterPrevious: 240, WaterCurrent: 240, WaterRecorded: &wr,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	// A consumption row exists but BOTH utilities are recovery-affected → neither
	// real usage is adopted.
	consumption := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		ElectricityPrevious: 1240, ElectricityCurrent: 1299, WaterPrevious: 240, WaterCurrent: 299,
	}
	if err := db.Create(consumption).Error; err != nil {
		t.Fatalf("seed consumption: %v", err)
	}

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: curMonth, MeterReadingID: rec.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	bill, err := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if e := lineByType(bill.LineItems, LineItemElectricity); e == nil || e.Quantity != 0 {
		t.Errorf("elec = %+v, want qty 0", e)
	}
	if w := lineByType(bill.LineItems, LineItemWater); w == nil || w.Quantity != 0 {
		t.Errorf("water = %+v, want qty 0", w)
	}
	var refunds []BillLineItem
	for _, li := range bill.LineItems {
		if li.LineType == LineItemAdjustment {
			refunds = append(refunds, li)
		}
	}
	if len(refunds) != 2 {
		t.Fatalf("want 2 refunds (elec+water), got %d", len(refunds))
	}
	elecR := adjustmentByUtility(refunds, AdjustmentUtilityElectricity)
	waterR := adjustmentByUtility(refunds, AdjustmentUtilityWater)
	if elecR == nil || elecR.Amount != -(260*700) {
		t.Errorf("elec refund = %+v, want %d", elecR, -(260 * 700))
	}
	if waterR == nil || waterR.Amount != -(60*1500) {
		t.Errorf("water refund = %+v, want %d", waterR, -(60 * 1500))
	}
}

// D — cancel → monthly → following monthly: the over-charged utility is refunded,
// the real usage is billed once (not lost), and the FOLLOWING month bills from
// the real observation baseline (228, not the anchor's 220) — no loss, no dup.
func TestUtilityScopedRecovery_D_FollowingMonthNoLossNoDup(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "US-D01")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500, WaterPrevious: 200, WaterCurrent: 220,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1800)

	// Month M (post-cancel state): recovery anchor + real consumption (water 220→228).
	mMonth := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	note := "post-cancel over-record"
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &mMonth,
		AnchorReason: &anchor, AnchorNote: &note, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 220, WaterCurrent: 220,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	mConsumption := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &mMonth,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, WaterPrevious: 220, WaterCurrent: 228,
	}
	if err := db.Create(mConsumption).Error; err != nil {
		t.Fatalf("seed M consumption: %v", err)
	}

	mBillR, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: mMonth, MeterReadingID: rec.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill M: %v", err)
	}
	mBill, _ := billRepo.FindByIDWithRelations(ctx, mBillR.Bill.ID)
	if w := lineByType(mBill.LineItems, LineItemWater); w == nil || w.Quantity != 8 {
		t.Fatalf("month M water = %+v, want 8 units (not lost)", w)
	}

	// Following month M+1: real reading starts from the REAL observation (228),
	// not the anchor's 220. Bills the next 8 units. No recovery, no refund.
	nMonth := "2026-08"
	nReading := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &nMonth,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, WaterPrevious: 228, WaterCurrent: 236,
	}
	if err := db.Create(nReading).Error; err != nil {
		t.Fatalf("seed M+1 reading: %v", err)
	}
	nBillR, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: nMonth, MeterReadingID: nReading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill M+1: %v", err)
	}
	nBill, _ := billRepo.FindByIDWithRelations(ctx, nBillR.Bill.ID)
	nWater := lineByType(nBill.LineItems, LineItemWater)
	if nWater == nil || nWater.Quantity != 8 {
		t.Errorf("month M+1 water = %+v, want 8 units (from real baseline 228)", nWater)
	}
	if nWater != nil && (nWater.MeterPrevious == nil || *nWater.MeterPrevious != 228) {
		t.Errorf("month M+1 water previous = %v, want 228 (follows real observation, not anchor 220)", nWater.MeterPrevious)
	}
	// No duplicate refund on M+1 (the refund lives once, on M).
	for _, li := range nBill.LineItems {
		if li.LineType == LineItemAdjustment {
			t.Errorf("month M+1 must carry NO refund (already reflected on M), got %+v", li)
		}
	}
}

func adjustmentByUtility(lines []BillLineItem, u AdjustmentUtility) *BillLineItem {
	for i := range lines {
		if lines[i].LineType == LineItemAdjustment && lines[i].AdjustmentUtility != nil && *lines[i].AdjustmentUtility == u {
			return &lines[i]
		}
	}
	return nil
}

func assertSingleRefund(t *testing.T, lines []BillLineItem, u AdjustmentUtility, wantAmount int64, recID interface{ String() string }) {
	t.Helper()
	var refunds []BillLineItem
	for _, li := range lines {
		if li.LineType == LineItemAdjustment {
			refunds = append(refunds, li)
		}
	}
	if len(refunds) != 1 {
		t.Fatalf("want exactly 1 refund, got %d", len(refunds))
	}
	r := refunds[0]
	if r.AdjustmentUtility == nil || *r.AdjustmentUtility != u {
		t.Errorf("refund utility = %v, want %v", r.AdjustmentUtility, u)
	}
	if r.Amount != wantAmount {
		t.Errorf("refund amount = %d, want %d", r.Amount, wantAmount)
	}
	if r.AdjustmentRecoveryReadingID == nil || r.AdjustmentRecoveryReadingID.String() != recID.String() {
		t.Errorf("refund FK = %v, want %v", r.AdjustmentRecoveryReadingID, recID)
	}
}
