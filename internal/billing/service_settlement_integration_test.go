//go:build integration

package billing

import (
	"context"
	"testing"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestGenerateSettlement_PersistsOutstandingBillLineItems is the end-to-end
// regression test for the OUTSTANDING_BILL bug.
//
// Bug history:
//   - LineItemOutstandingBill existed in Go and was used by prepareSettlementPlan
//   - Unit tests (mocks) verified it was appended to line_items
//   - But migration 00014's CHECK constraint omitted 'OUTSTANDING_BILL'
//   - ⇒ any settlement that tried to absorb outstanding bills 500'd in prod
//
// This test exercises the write path end-to-end, so a future regression
// (constraint drift vs. Go constants) fails here before reaching prod.
func TestGenerateSettlement_PersistsOutstandingBillLineItems(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "A-101")
	tn := fixtures.SeedTenant(t, db)
	// Contract started 8 months ago — comfortably past minMonths=6.
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedCleaningFeeConfig(t, db, apt.ID.String(), 30000)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000) // ฿100/day — matches seed default

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -5)

	// EXIT meter reading — required by prepareSettlementPlan.
	fixtures.SeedExitMeter(t, db, rm.ID.String(), moveOutDate, 100, 20)

	// Active move-out notice with actual_move_out_date set.
	fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	// One FINALIZED monthly bill from 3 months ago — this is the absorb target.
	// (Must be BEFORE M-1 to trigger full-absorption path that creates
	// OUTSTANDING_BILL line items.)
	oldMonth := moveOutDate.AddDate(0, -3, 0).Format("2006-01")
	oldBill := Bill{
		ContractID:   c.ID,
		BillingMonth: oldMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  416000,
	}
	if err := db.Create(&oldBill).Error; err != nil {
		t.Fatalf("create old monthly bill: %v", err)
	}
	oldItems := []BillLineItem{
		{BillID: oldBill.ID, LineType: LineItemRoomRent, Source: LineItemSourceAuto, Description: "ค่าเช่า", Amount: 300000, SortOrder: 1},
		{BillID: oldBill.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ 20 หน่วย", Amount: 36000, Quantity: 20, UnitPrice: 1800, SortOrder: 2},
		{BillID: oldBill.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า 100 หน่วย", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 3},
	}
	if err := db.Create(&oldItems).Error; err != nil {
		t.Fatalf("create old bill line items: %v", err)
	}

	// ── Wire the real service graph ──
	svc := buildRealBillingService(t, db)

	// ── Act ──
	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOutDate, "")
	if err != nil {
		t.Fatalf("GenerateSettlement failed: %v", err)
	}
	if result == nil || result.BillID == (uuid.UUID{}) {
		t.Fatalf("nil or zero result: %+v", result)
	}

	// ── Load the persisted settlement bill (with line items) ──
	var settlement Bill
	if err := db.Preload("LineItems").Where("id = ?", result.BillID).First(&settlement).Error; err != nil {
		t.Fatalf("reload settlement bill: %v", err)
	}
	if settlement.BillType != BillTypeSettlement {
		t.Errorf("bill_type = %s, want SETTLEMENT", settlement.BillType)
	}
	if settlement.Status != BillStatusDraft {
		t.Errorf("status = %s, want DRAFT", settlement.Status)
	}

	// ── Assert: line types the business flow must produce ──
	// Always-present on a settlement with utility + cleaning fee + absorbed bills:
	//   prorate rent, water, electricity, cleaning fee, OUTSTANDING_BILL
	//
	// Note: ROOM_RENT vs PRORATE_RENT depends on whether advance rent was
	// already paid; for this scenario (no PAID M-1 bill) it's PRORATE_RENT.
	byType := make(map[LineItemType][]BillLineItem, len(settlement.LineItems))
	for _, li := range settlement.LineItems {
		byType[li.LineType] = append(byType[li.LineType], li)
	}
	requiredTypes := []LineItemType{
		LineItemProrateRent,
		LineItemWater,
		LineItemElectricity,
		LineItemCleaningFee,
		LineItemOutstandingBill,
	}
	for _, lt := range requiredTypes {
		if len(byType[lt]) == 0 {
			t.Errorf("settlement missing required line_type %q", lt)
		}
	}

	// ── Assert: OUTSTANDING_BILL amount equals absorbed bill's full total ──
	// Because oldBill is from M-3 (strictly before M-1), absorption is FULL —
	// the OUTSTANDING_BILL line amount must equal oldBill.TotalAmount (416000).
	// If this drifts, either absorption logic changed or M-1 special-case leaked
	// into pre-M-1 months.
	if got := byType[LineItemOutstandingBill]; len(got) != 1 {
		t.Errorf("expected exactly 1 OUTSTANDING_BILL line, got %d", len(got))
	} else if got[0].Amount != oldBill.TotalAmount {
		t.Errorf("OUTSTANDING_BILL amount = %d, want %d (full old bill total)", got[0].Amount, oldBill.TotalAmount)
	}

	// ── Assert: bill.total_amount == sum(line_items.amount) ──
	// Catches drift between Bill.CalculateTotal() and what's persisted.
	var sum int64
	for _, li := range settlement.LineItems {
		sum += li.Amount
	}
	if sum != settlement.TotalAmount {
		t.Errorf("total_amount = %d, sum(line_items) = %d (must match)", settlement.TotalAmount, sum)
	}

	// ── Assert: all AUTO-source line items are populated (no MANUAL on fresh draft) ──
	for _, li := range settlement.LineItems {
		if li.Source != LineItemSourceAuto {
			t.Errorf("fresh draft has non-AUTO line item: type=%s source=%s", li.LineType, li.Source)
		}
	}

	// ── Assert: result.NetAmount reflects a real settlement (non-zero) ──
	// Zero would mean deposit exactly covered charges — in this scenario
	// (charges include absorbed 4160 baht bill), deposit (3000) < charges,
	// so tenant must owe a positive amount.
	if result.NetAmount == 0 {
		t.Error("result.NetAmount = 0, expected non-zero (charges exceed deposit in this scenario)")
	}

	// ── Assert: absorbed bill transitioned to VOID with reason ABSORBED_BY_SETTLEMENT ──
	var absorbed Bill
	if err := db.Where("id = ?", oldBill.ID).First(&absorbed).Error; err != nil {
		t.Fatalf("reload absorbed bill: %v", err)
	}
	if absorbed.Status != BillStatusVoid {
		t.Errorf("absorbed bill status = %s, want VOID", absorbed.Status)
	}
	if absorbed.VoidReason == nil || *absorbed.VoidReason != "ABSORBED_BY_SETTLEMENT" {
		t.Errorf("absorbed bill void_reason = %v, want ABSORBED_BY_SETTLEMENT", absorbed.VoidReason)
	}
}

// buildRealBillingService wires the billing service with real repositories
// from each feature package, backed by the test DB.
func buildRealBillingService(t *testing.T, db *gorm.DB) BillingService {
	t.Helper()
	txMgr := database.NewTxManager(db)
	billRepo := NewBillingRepository(db)
	billAuditRepo := NewBillAuditRepository(db)
	contractRepo := contract.NewContractRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	bcRepo := billingconfig.NewBillingConfigRepository(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	return NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, bcRepo, moveOutRepo, nil, txMgr)
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
