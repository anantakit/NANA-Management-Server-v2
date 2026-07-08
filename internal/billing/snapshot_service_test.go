package billing

import (
	"testing"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

func i64ptr(v int64) *int64 { return &v }

// Q1.6 P2 — the refund is emitted AT GENERATION from the meter-truth fact, with
// no decision. A READING_RECOVERY reading with recorded > current auto-produces a
// refund ADJUSTMENT line; the affected AUTO line is already 0 (usage 0). Ontology
// lock 2026-07-08: the refund RATE comes from `recon` (the SOURCE bill's line
// unit_price), NOT the current contract rate passed as elecRate — here elecRate
// is deliberately 1000 while recon carries 800, and the refund resolves at 800.
func TestComputeMonthlyBillSnapshot_AutoRefundOnOverRecord(t *testing.T) {
	recorded := 1500
	recID := uuid.New()
	anchor := meterreading.AnchorReasonReadingRecovery
	reading := &meterreading.MeterReading{
		ID:                  recID,
		AnchorReason:        &anchor,
		ElectricityPrevious: 1200,
		ElectricityCurrent:  1200, // usage 0 (re-baselined); recorded 1500 → over-record 300
		ElectricityRecorded: &recorded,
		WaterPrevious:       200,
		WaterCurrent:        220, // normal water usage 20, not a recovery
	}
	recon := &RecoveryReconciliation{Electricity: i64ptr(800)} // source-bill rate
	snap := ComputeMonthlyBillSnapshot("2026-07", 250000, 1000, 1800, reading, recon)

	if len(snap.LineItems) != 4 {
		t.Fatalf("line count = %d, want 4 (rent, elec, water, refund)", len(snap.LineItems))
	}
	var elec, refund *ComputedLineItem
	for i := range snap.LineItems {
		li := &snap.LineItems[i]
		switch li.Type {
		case LineItemElectricity:
			elec = li
		case LineItemAdjustment:
			refund = li
		}
	}
	if elec == nil || elec.Amount != 0 {
		t.Errorf("electricity AUTO = %+v, want amount 0 (usage 0)", elec)
	}
	if refund == nil {
		t.Fatal("no refund ADJUSTMENT line emitted")
	}
	// Priced at the SOURCE rate (800), not the current rate (1000).
	if refund.Amount != -(300*800) || refund.Quantity != 300 || refund.UnitPrice != 800 {
		t.Errorf("refund = %+v, want amount %d / qty 300 / unit_price 800 (source rate)", refund, -(300 * 800))
	}
	if refund.Description != "จดไว้ 1500 → จริง 1200" {
		t.Errorf("refund description = %q", refund.Description)
	}
	if refund.AdjustmentUtility == nil || *refund.AdjustmentUtility != AdjustmentUtilityElectricity {
		t.Errorf("refund utility = %v, want ELECTRICITY", refund.AdjustmentUtility)
	}
	if refund.AdjustmentRecoveryReadingID == nil || *refund.AdjustmentRecoveryReadingID != recID {
		t.Errorf("refund FK = %v, want %v", refund.AdjustmentRecoveryReadingID, recID)
	}
	// total = rent 250000 + elec 0 + water 20*1800 + refund -(300*800)
	if want := int64(250000 + 20*1800 - 300*800); snap.TotalAmount != want {
		t.Errorf("total = %d, want %d", snap.TotalAmount, want)
	}

	// ToLineItems materializes the ADJUSTMENT as a valid MANUAL/METER_RECOVERY row.
	billID := uuid.New()
	var adj *BillLineItem
	for _, li := range snap.ToLineItems(billID) {
		if li.LineType == LineItemAdjustment {
			l := li
			adj = &l
		}
	}
	if adj == nil || adj.Source != LineItemSourceManual || adj.BillID != billID {
		t.Fatalf("materialized adjustment = %+v, want source MANUAL + billID %v", adj, billID)
	}
	if adj.AdjustmentReasonCode == nil || *adj.AdjustmentReasonCode != AdjustmentReasonMeterRecovery {
		t.Errorf("reason = %v, want METER_RECOVERY", adj.AdjustmentReasonCode)
	}
	if err := adj.ValidateAdjustment(); err != nil {
		t.Errorf("materialized adjustment invalid: %v", err)
	}
}

// S0 gate (ontology lock 2026-07-08): an over-record whose SOURCE month was never
// billed produces a nil recon → NO refund line, even though recorded > current.
// This is the phantom-refund fix at the pure-function boundary.
func TestComputeMonthlyBillSnapshot_S0_NilRecon_NoRefund(t *testing.T) {
	recorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	reading := &meterreading.MeterReading{
		ID:                  uuid.New(),
		AnchorReason:        &anchor,
		ElectricityPrevious: 1200,
		ElectricityCurrent:  1200,
		ElectricityRecorded: &recorded, // a real over-record...
	}
	// ...but recon is nil (source never billed) → no forward credit.
	snap := ComputeMonthlyBillSnapshot("2026-07", 250000, 800, 1800, reading, nil)
	for _, li := range snap.LineItems {
		if li.Type == LineItemAdjustment {
			t.Errorf("S0 (unbilled source) must emit NO refund line, got %+v", li)
		}
	}
}

// A zero-rate over-record was never charged → nothing to refund → NO line, even
// with F applicable (recon present at rate 0).
func TestComputeMonthlyBillSnapshot_ZeroRateOverRecord_NoLine(t *testing.T) {
	recorded := 1500
	anchor := meterreading.AnchorReasonReadingRecovery
	reading := &meterreading.MeterReading{
		ID:                  uuid.New(),
		AnchorReason:        &anchor,
		ElectricityPrevious: 1200,
		ElectricityCurrent:  1200,
		ElectricityRecorded: &recorded,
	}
	recon := &RecoveryReconciliation{Electricity: i64ptr(0)} // source billed at rate 0
	snap := ComputeMonthlyBillSnapshot("2026-07", 250000, 800, 1800, reading, recon)
	for _, li := range snap.LineItems {
		if li.Type == LineItemAdjustment {
			t.Errorf("zero-rate over-record must emit NO refund line, got %+v", li)
		}
	}
}

// A normal (non-recovery) reading emits no refund line — the common path.
func TestComputeMonthlyBillSnapshot_NormalReading_NoRefund(t *testing.T) {
	reading := &meterreading.MeterReading{
		ID:                  uuid.New(),
		ElectricityPrevious: 1200,
		ElectricityCurrent:  1300, // normal usage 100, no recorded value
		WaterPrevious:       200,
		WaterCurrent:        220,
	}
	snap := ComputeMonthlyBillSnapshot("2026-07", 250000, 800, 1800, reading, nil)
	if len(snap.LineItems) != 3 {
		t.Errorf("normal reading line count = %d, want 3 (rent+elec+water, no refund)", len(snap.LineItems))
	}
}
