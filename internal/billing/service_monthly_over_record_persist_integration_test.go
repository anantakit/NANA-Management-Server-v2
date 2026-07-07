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

// TestCreateMonthlyBill_OverRecord_PersistsRefund is the Q1.6 end-to-end money
// guard: generating a monthly bill from an over-record recovery reading must
// PERSIST an auto-emitted refund ADJUSTMENT line to Postgres. The unit tests
// prove ComputeMonthlyBillSnapshot emits the line; this proves it actually
// lands in the DB — which the pre-00048 `adjustment_note_required` constraint
// silently prevented (a Q1.6 refund carries no operator note). The affected
// AUTO line is re-baselined to 0 (usage 0).
func TestCreateMonthlyBill_OverRecord_PersistsRefund(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "OR-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6) // rates ฿8 elec / ฿18 water

	// Over-record recovery MONTHLY reading: a re-anchor event, so BOTH utilities
	// are usage 0 (prev==curr). Electricity was over-recorded — physically 1200
	// but 1500 was recorded before → 300 units over-charged → refund. Water is a
	// clean re-anchor (no over-record).
	month := "2026-07"
	elecRecorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	anchorNote := "จดเกิน — physical 1200 recorded 1500"
	reading := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &month,
		AnchorReason:        &anchor,
		AnchorNote:          &anchorNote,
		ElectricityPrevious: 1200,
		ElectricityCurrent:  1200,
		ElectricityRecorded: &elecRecorded,
		WaterPrevious:       100,
		WaterCurrent:        100,
	}
	if err := db.Create(reading).Error; err != nil {
		t.Fatalf("seed recovery reading: %v", err)
	}

	billRepo := NewBillingRepository(db)
	auditRepo := NewBillAuditRepository(db)
	contracts := &integrationContractStub{c: c}
	meters := meterreading.NewMeterReadingRepository(db)
	configs := &integrationConfigStub{}
	txMgr := database.NewTxManager(db)
	svc := NewBillingService(billRepo, auditRepo, contracts, meters, configs, nil, txMgr)

	created, err := svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID:     c.ID.String(),
		BillingMonth:   month,
		MeterReadingID: reading.ID.String(),
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
	if refund.Amount != -(300 * 800) {
		t.Errorf("refund amount = %d, want %d", refund.Amount, -(300 * 800))
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
