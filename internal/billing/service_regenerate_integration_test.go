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

// TestRegenerateDraft_RefreshesStaleDraftWithRecoveryRefund is the Monthly Draft
// Refresh end-to-end (Epic A): a DRAFT that predates a Reading Recovery is stale
// (freshness gate blocks). RegenerateDraft atomically voids it and rebuilds via
// the SHARED generation path, so the new draft carries the S0-gated recovery
// forward credit (priced at the SOURCE bill rate) and the gate clears.
func TestRegenerateDraft_RefreshesStaleDraftWithRecoveryRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "RG-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6) // current rate ฿8 (800)

	billRepo := NewBillingRepository(db)
	auditRepo := NewBillAuditRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, auditRepo, &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))

	// Source month billed at 700 (distinct from current 800 → proves source rate).
	sourceMonth := "2026-06"
	src := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 1000, ElectricityCurrent: 1500, WaterPrevious: 80, WaterCurrent: 100,
	}
	if err := db.Create(src).Error; err != nil {
		t.Fatalf("seed source reading: %v", err)
	}
	seedFinalizedMonthlyBill(t, db, c.ID, sourceMonth, 700, 1800)

	// Current month: recovery row (physical 1200, recorded 1500, source=June).
	curMonth := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	note := "จดเกิน — physical 1200"
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &curMonth,
		AnchorReason: &anchor, AnchorNote: &note, RecoverySourceReadingID: &src.ID,
		ElectricityPrevious: 1200, ElectricityCurrent: 1200, ElectricityRecorded: &elecRecorded,
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}

	// The stale DRAFT (generated before the recovery — no refund line).
	stale := &Bill{ContractID: c.ID, BillingMonth: curMonth, BillType: BillTypeMonthly, Status: BillStatusDraft}
	if err := db.Create(stale).Error; err != nil {
		t.Fatalf("seed stale draft: %v", err)
	}
	if err := db.Create(&[]BillLineItem{
		{BillID: stale.ID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1},
		{BillID: stale.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 0, Quantity: 0, UnitPrice: 800, SortOrder: 2},
		{BillID: stale.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 0, Quantity: 0, UnitPrice: 1800, SortOrder: 3},
	}).Error; err != nil {
		t.Fatalf("seed stale draft lines: %v", err)
	}

	// Precondition: the contract is stale (recovery unreflected on the draft).
	if pending, _ := billRepo.HasUnreflectedOverRecordByContractID(ctx, c.ID); !pending {
		t.Fatal("expected stale (unreflected recovery) before regenerate")
	}

	fresh, err := svc.RegenerateDraft(ctx, stale.ID, nil)
	if err != nil {
		t.Fatalf("RegenerateDraft: %v", err)
	}

	// Old draft is VOID (reason REGENERATED).
	var oldReloaded Bill
	if err := db.First(&oldReloaded, "id = ?", stale.ID).Error; err != nil {
		t.Fatalf("reload old bill: %v", err)
	}
	if oldReloaded.Status != BillStatusVoid {
		t.Errorf("old status = %s, want VOID", oldReloaded.Status)
	}
	if oldReloaded.VoidReason == nil || *oldReloaded.VoidReason != voidReasonRegenerated {
		t.Errorf("old void_reason = %v, want %q", oldReloaded.VoidReason, voidReasonRegenerated)
	}

	// New draft carries the refund at the SOURCE rate (700), not current (800).
	if fresh.Status != BillStatusDraft {
		t.Errorf("new status = %s, want DRAFT", fresh.Status)
	}
	var refund *BillLineItem
	for i := range fresh.LineItems {
		if fresh.LineItems[i].LineType == LineItemAdjustment {
			refund = &fresh.LineItems[i]
		}
	}
	if refund == nil {
		t.Fatal("regenerated draft missing the recovery refund line")
	}
	if refund.Amount != -(300 * 700) {
		t.Errorf("refund = %d, want %d (source rate 700, not current 800)", refund.Amount, -(300*700))
	}

	// The gate now clears — the recovery is reflected on the fresh draft.
	if pending, _ := billRepo.HasUnreflectedOverRecordByContractID(ctx, c.ID); pending {
		t.Error("expected gate cleared after regenerate (recovery now reflected on the fresh draft)")
	}
}

// TestRegenerateDraft_RejectsSettlement locks the out-of-scope guard: a SETTLEMENT
// draft must NOT go through Monthly Draft Refresh (Epic B / S4 is a different
// reconciliation).
func TestRegenerateDraft_RejectsSettlement(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "RG-102")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	billRepo := NewBillingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c},
		meterreading.NewMeterReadingRepository(db), &integrationConfigStub{}, nil, database.NewTxManager(db))

	settle := &Bill{ContractID: c.ID, BillingMonth: "2026-07", BillType: BillTypeSettlement, Status: BillStatusDraft}
	if err := db.Create(settle).Error; err != nil {
		t.Fatalf("seed settlement draft: %v", err)
	}

	if _, err := svc.RegenerateDraft(ctx, settle.ID, nil); err == nil {
		t.Fatal("expected SETTLEMENT draft to be rejected by RegenerateDraft, got nil")
	}
}
