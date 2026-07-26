//go:build integration

package monthly

import (
	"context"
	"testing"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestBatchMonthly_RecoveryOverlay_BillsRealWater (owner family-closure matrix A):
// the BATCH generate→commit path must obey the same utility-scoped recovery
// semantics as the single-bill path — electricity recovery (0 usage + refund
// once) must NOT suppress the real water usage that coexists in a consumption row.
func TestBatchMonthly_RecoveryOverlay_BillsRealWater(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "BATCH-OV1")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := billing.NewBillingRepository(db)
	auditRepo := billing.NewBillAuditRepository(db)
	adapter := billing.NewMonthlyAdapter(billRepo, auditRepo)
	batchRepo := NewBatchRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewService(adapter, adapter, batchRepo, meters, &replanMoveOutStub{}, database.NewTxManager(db))

	// Billed source month (electricity charged at 700 → the forward-credit rate).
	sourceMonth := "2026-06"
	source := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500, WaterPrevious: 200, WaterCurrent: 220,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	seedFinalizedSourceBill(t, db, c.ID, sourceMonth, 700, 1800)

	// Recovery month: electricity over-record anchor + coexisting real water usage.
	curMonth := "2026-07"
	elecRecorded := 1500
	anchorReason := meterreading.AnchorReasonReadingRecovery
	note := "จดไฟฟ้าเกิน"
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

	batch, err := svc.BatchCreateMonthlyBills(ctx,
		BatchCreateMonthlyBillsRequest{ApartmentID: apt.ID.String(), BillingMonth: curMonth}, nil)
	if err != nil {
		t.Fatalf("BatchCreateMonthlyBills: %v", err)
	}
	if _, err := svc.CommitBatch(ctx, batch.ID); err != nil {
		t.Fatalf("CommitBatch: %v", err)
	}

	bill, err := billRepo.FindDraftBillForContractAndMonth(ctx, c.ID, curMonth, billing.BillTypeMonthly)
	if err != nil {
		t.Fatalf("find committed DRAFT bill: %v", err)
	}
	full, err := billRepo.FindByIDWithRelations(ctx, bill.ID)
	if err != nil {
		t.Fatalf("reload bill: %v", err)
	}

	var water, elec *billing.BillLineItem
	var refunds []billing.BillLineItem
	for i := range full.LineItems {
		li := &full.LineItems[i]
		switch li.LineType {
		case billing.LineItemWater:
			water = li
		case billing.LineItemElectricity:
			elec = li
		case billing.LineItemAdjustment:
			refunds = append(refunds, *li)
		}
	}
	if water == nil || water.Quantity != 8 || water.Amount != 8*1800 {
		t.Errorf("batch water = %+v, want qty 8 / ฿%d (real usage)", water, 8*1800)
	}
	if elec == nil || elec.Quantity != 0 || elec.Amount != 0 {
		t.Errorf("batch elec = %+v, want qty 0 (recovery-anchored)", elec)
	}
	if len(refunds) != 1 || refunds[0].AdjustmentUtility == nil || *refunds[0].AdjustmentUtility != billing.AdjustmentUtilityElectricity || refunds[0].Amount != -(260*700) {
		t.Errorf("batch refunds = %+v, want exactly one ELECTRICITY refund of %d", refunds, -(260 * 700))
	}
}

// seedFinalizedSourceBill inserts a FINALIZED MONTHLY bill for (contract, month)
// with electricity/water AUTO lines at the given rates — the historical charge a
// forward credit refunds against. Local copy of the billing-package helper
// (unexported across packages).
func seedFinalizedSourceBill(t *testing.T, db *gorm.DB, contractID uuid.UUID, month string, elecRate, waterRate int64) {
	t.Helper()
	b := &billing.Bill{ContractID: contractID, BillingMonth: month, BillType: billing.BillTypeMonthly, Status: billing.BillStatusFinalized}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("seed source bill: %v", err)
	}
	lines := []billing.BillLineItem{
		{BillID: b.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 500 * elecRate, Quantity: 500, UnitPrice: elecRate, SortOrder: 2},
		{BillID: b.ID, LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 20 * waterRate, Quantity: 20, UnitPrice: waterRate, SortOrder: 3},
	}
	if err := db.Create(&lines).Error; err != nil {
		t.Fatalf("seed source bill lines: %v", err)
	}
}
