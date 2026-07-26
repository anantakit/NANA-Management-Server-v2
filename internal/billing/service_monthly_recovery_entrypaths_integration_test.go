//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MONTHLY recovery capability-family closure (owner 2026-07-18): EVERY
// production-reachable monthly create/rebuild path must obey the same
// utility-scoped recovery semantics — electricity recovery (0 + refund once)
// must never suppress the real water usage, on ANY entry path. This suite
// exercises the entry paths bea5196 did not: single/manual from the consumption
// row ID, regenerate-draft, and MONTHLY correction (the second reachable defect).

type recoveryFamilyEnv struct {
	svc         BillingService
	billRepo    BillingRepository
	c           *contract.Contract
	anchor      *meterreading.MeterReading
	consumption *meterreading.MeterReading
	curMonth    string
}

// seedElecRecoveryWithRealWater seeds the canonical mixed-utility state:
// electricity over-recorded (recovery), water with real 8-unit usage, plus a
// billed source month so the forward credit fires at the source rate (700).
func seedElecRecoveryWithRealWater(t *testing.T, db *gorm.DB, roomNum string) *recoveryFamilyEnv {
	t.Helper()
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), roomNum)
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
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1800)

	curMonth := "2026-07"
	elecRecorded := 1500
	anchorReason := meterreading.AnchorReasonReadingRecovery
	note := "จดไฟฟ้าเกิน — physical 1240 recorded 1500"
	anchor := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchorReason, AnchorNote: &note, RecoverySourceReadingID: &source.ID,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 220, WaterCurrent: 220,
	}
	if err := db.Create(anchor).Error; err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	consumption := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, WaterPrevious: 220, WaterCurrent: 228,
	}
	if err := db.Create(consumption).Error; err != nil {
		t.Fatalf("seed consumption: %v", err)
	}
	return &recoveryFamilyEnv{svc: svc, billRepo: billRepo, c: c, anchor: anchor, consumption: consumption, curMonth: curMonth}
}

// assertCanonicalMonthly verifies the invariant every path must satisfy:
// water 8 units / ฿144, electricity 0, exactly one electricity refund.
func assertCanonicalMonthly(t *testing.T, lines []BillLineItem) {
	t.Helper()
	water := lineByType(lines, LineItemWater)
	if water == nil || water.Quantity != 8 || water.Amount != 8*1800 {
		t.Errorf("water = %+v, want qty 8 / ฿%d (real usage, not suppressed)", water, 8*1800)
	}
	elec := lineByType(lines, LineItemElectricity)
	if elec == nil || elec.Quantity != 0 || elec.Amount != 0 {
		t.Errorf("elec = %+v, want qty 0 / ฿0 (recovery-anchored)", elec)
	}
	var refunds []BillLineItem
	for _, li := range lines {
		if li.LineType == LineItemAdjustment {
			refunds = append(refunds, li)
		}
	}
	if len(refunds) != 1 || refunds[0].AdjustmentUtility == nil || *refunds[0].AdjustmentUtility != AdjustmentUtilityElectricity || refunds[0].Amount != -(260*700) {
		t.Errorf("refunds = %+v, want exactly one ELECTRICITY refund of %d", refunds, -(260 * 700))
	}
}

// B — single/manual monthly STARTING FROM THE CONSUMPTION ROW ID (the known
// edge): the canonical resolver must still apply the recovery from the
// coexisting anchor, so water is billed and the refund fires.
func TestMonthlyFamily_B_SingleFromConsumptionID(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()
	env := seedElecRecoveryWithRealWater(t, db, "FAM-B01")

	created, err := env.svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: env.c.ID.String(), BillingMonth: env.curMonth,
		MeterReadingID: env.consumption.ID.String(), // <-- consumption row, not the anchor
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	bill, _ := env.billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	assertCanonicalMonthly(t, bill.LineItems)
}

// C — regenerate-draft rebuild keeps the utility-scoped result + refund once.
func TestMonthlyFamily_C_RegenerateDraft(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()
	env := seedElecRecoveryWithRealWater(t, db, "FAM-C01")

	created, err := env.svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: env.c.ID.String(), BillingMonth: env.curMonth, MeterReadingID: env.anchor.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	regen, err := env.svc.RegenerateDraft(ctx, created.Bill.ID, nil)
	if err != nil {
		t.Fatalf("RegenerateDraft: %v", err)
	}
	bill, _ := env.billRepo.FindByIDWithRelations(ctx, regen.Bill.ID)
	assertCanonicalMonthly(t, bill.LineItems) // H: still exactly one refund after rebuild
}

// D — MONTHLY correction rebuild (POST /bills/:id/correct, UI-reachable): the
// second reachable defect — correction inlined the compute with no projection.
func TestMonthlyFamily_D_Correct(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()
	env := seedElecRecoveryWithRealWater(t, db, "FAM-D01")

	created, err := env.svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: env.c.ID.String(), BillingMonth: env.curMonth, MeterReadingID: env.anchor.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	if _, err := env.svc.FinalizeBill(ctx, created.Bill.ID, nil); err != nil {
		t.Fatalf("FinalizeBill: %v", err)
	}
	corrected, err := env.svc.CorrectBill(ctx, created.Bill.ID, CorrectBillRequest{
		CorrectionReason: "แก้ไขบิลรอบนี้ (regression)",
	}, nil)
	if err != nil {
		t.Fatalf("CorrectBill: %v", err)
	}
	bill, _ := env.billRepo.FindByIDWithRelations(ctx, corrected.Bill.ID)
	assertCanonicalMonthly(t, bill.LineItems)
}

// G — ordinary monthly (NO recovery anchor): plain usage, no refund, unchanged.
func TestMonthlyFamily_G_NoRecovery_Unchanged(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "FAM-G01")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)
	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	month := "2026-07"
	reading := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
		ElectricityPrevious: 100, ElectricityCurrent: 130, WaterPrevious: 40, WaterCurrent: 52,
	}
	if err := db.Create(reading).Error; err != nil {
		t.Fatalf("seed reading: %v", err)
	}
	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: c.ID.String(), BillingMonth: month, MeterReadingID: reading.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("CreateMonthlyBill: %v", err)
	}
	bill, _ := billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if e := lineByType(bill.LineItems, LineItemElectricity); e == nil || e.Quantity != 30 {
		t.Errorf("elec = %+v, want qty 30 (ordinary usage)", e)
	}
	if w := lineByType(bill.LineItems, LineItemWater); w == nil || w.Quantity != 12 {
		t.Errorf("water = %+v, want qty 12 (ordinary usage)", w)
	}
	for _, li := range bill.LineItems {
		if li.LineType == LineItemAdjustment {
			t.Errorf("ordinary monthly must carry NO refund, got %+v", li)
		}
	}
	_ = uuid.Nil
}
