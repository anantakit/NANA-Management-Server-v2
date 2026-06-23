//go:build integration

package billing

import (
	"strings"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_AdjustmentLineContract locks the Phase 4 contract: persistence
// shape + cross-feature provenance + DB CHECK constraints — all in one
// integration test, all against real Postgres.
//
// DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22).
// DESIGN:   /Users/anantakit/.claude/plans/hidden-waddling-marble.md.
//
// Five assertions:
//
//	A1 (positive roundtrip)        — ADJUSTMENT line with all 4 new fields
//	                                  persists + reads back identically.
//	A2 (cross-feature provenance)  — FK target meter row carries
//	                                  anchor_reason = READING_RECOVERY.
//	A3 (negative DB CHECK)         — ADJUSTMENT + Source=AUTO rejected by
//	                                  bill_line_items_adjustment_source_manual.
//	A4 (negative DB CHECK)         — ADJUSTMENT + FK=NULL rejected by
//	                                  bill_line_items_adjustment_fk_required.
//	A5 (negative DB CHECK)         — ELECTRICITY + FK populated rejected by
//	                                  bill_line_items_adjustment_fk_only_for_type.
//
// Seeding is hand-rolled (no SeedBill / SeedAdjustmentLine fixtures exist —
// C1 is the first caller, fixture extraction deferred to 3rd-caller threshold
// per project pattern). Source + recovery meter rows are also hand-seeded via
// raw db.Create because Phase 5's recovery commit service surface doesn't
// exist yet (matches Phase 1's A1 strengthened test pattern,
// mutable-swimming-firefly.md §Item 3).
func TestRecovery_AdjustmentLineContract(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "ADJ-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	ctxNote := "ค้นพบเดือน 2026-04 จดมิเตอร์ผิด — re-anchor 2026-05"

	// ── Source meter reading (Month 1, suspect month) ──
	sourceMonth := "2026-04"
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &sourceMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  200,
		WaterPrevious:       50,
		WaterCurrent:        55,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source meter row: %v", err)
	}

	// ── Recovery meter reading (Month 2, anchor) ──
	// Doctrine: prev=current (usage=0), anchor_reason=READING_RECOVERY,
	// recovery_source_reading_id points at the suspect source.
	recoveryMonth := "2026-05"
	recoveryReason := meterreading.AnchorReasonReadingRecovery
	recoveryNote := "ค้นพบเดือน 2026-04 จดมิเตอร์ผิด"
	recovery := &meterreading.MeterReading{
		RoomID:                  rm.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &recoveryMonth,
		ElectricityPrevious:     220, // = current; recovery has usage=0
		ElectricityCurrent:      220,
		WaterPrevious:           60,
		WaterCurrent:            60,
		AnchorReason:            &recoveryReason,
		AnchorNote:              &recoveryNote,
		RecoverySourceReadingID: &source.ID,
	}
	if err := db.Create(recovery).Error; err != nil {
		t.Fatalf("seed recovery meter row: %v", err)
	}

	// ── DRAFT MONTHLY bill (the current month carrying the adjustment) ──
	bill := &Bill{
		ContractID:   c.ID,
		BillingMonth: recoveryMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	// Inline helper — first caller scenario, no shared extraction yet.
	assertCheckViolation := func(t *testing.T, err error, constraintName string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expected DB rejection (%s), got nil", constraintName)
		}
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "check constraint") {
			t.Fatalf("expected CHECK violation, got: %v", err)
		}
		if !strings.Contains(lower, strings.ToLower(constraintName)) {
			t.Fatalf("expected constraint %q in error, got: %v", constraintName, err)
		}
	}

	// ── A1: positive roundtrip ──
	reason := AdjustmentReasonMeterRecovery
	adjLine := &BillLineItem{
		BillID:                      bill.ID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		Description:                 "คืนยอดที่เก็บเกินจากเดือน 2026-04",
		Amount:                      12345,
		SortOrder:                   99,
		AdjustmentRecoveryReadingID: &recovery.ID,
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &ctxNote,
	}
	if err := db.Create(adjLine).Error; err != nil {
		t.Fatalf("A1: insert ADJUSTMENT line: %v", err)
	}

	var got BillLineItem
	if err := db.First(&got, "id = ?", adjLine.ID).Error; err != nil {
		t.Fatalf("A1: re-read ADJUSTMENT line: %v", err)
	}
	if got.LineType != LineItemAdjustment {
		t.Errorf("A1: LineType = %q, want ADJUSTMENT", got.LineType)
	}
	if got.Source != LineItemSourceManual {
		t.Errorf("A1: Source = %q, want MANUAL", got.Source)
	}
	if got.Amount != 12345 {
		t.Errorf("A1: Amount = %d, want 12345", got.Amount)
	}
	if got.AdjustmentRecoveryReadingID == nil || *got.AdjustmentRecoveryReadingID != recovery.ID {
		t.Errorf("A1: AdjustmentRecoveryReadingID = %v, want %v", got.AdjustmentRecoveryReadingID, recovery.ID)
	}
	if got.AdjustmentReasonCode == nil || *got.AdjustmentReasonCode != AdjustmentReasonMeterRecovery {
		t.Errorf("A1: AdjustmentReasonCode = %v, want METER_RECOVERY", got.AdjustmentReasonCode)
	}
	if got.AdjustmentNote == nil || *got.AdjustmentNote != ctxNote {
		t.Errorf("A1: AdjustmentNote = %v, want %q", got.AdjustmentNote, ctxNote)
	}

	// ── A2: cross-feature provenance — FK target is a READING_RECOVERY anchor ──
	var meterRow meterreading.MeterReading
	if err := db.First(&meterRow, "id = ?", recovery.ID).Error; err != nil {
		t.Fatalf("A2: re-read FK target meter row: %v", err)
	}
	if meterRow.AnchorReason == nil {
		t.Fatalf("A2: FK target's AnchorReason is nil — provenance broken")
	}
	if *meterRow.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("A2: FK target's AnchorReason = %q, want READING_RECOVERY", *meterRow.AnchorReason)
	}

	// ── A3: ADJUSTMENT + Source=AUTO rejected ──
	badSource := &BillLineItem{
		BillID:                      bill.ID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceAuto, // violation
		Description:                 "bad: source=AUTO",
		Amount:                      100,
		AdjustmentRecoveryReadingID: &recovery.ID,
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &ctxNote,
	}
	err := db.Create(badSource).Error
	assertCheckViolation(t, err, "bill_line_items_adjustment_source_manual")

	// ── A4: ADJUSTMENT + FK=NULL rejected ──
	badFK := &BillLineItem{
		BillID:                      bill.ID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		Description:                 "bad: fk=null",
		Amount:                      100,
		AdjustmentRecoveryReadingID: nil, // violation
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &ctxNote,
	}
	err = db.Create(badFK).Error
	assertCheckViolation(t, err, "bill_line_items_adjustment_fk_required")

	// ── A5: ELECTRICITY + FK populated rejected ──
	// Non-ADJUSTMENT rows must keep adjustment_recovery_reading_id NULL.
	badType := &BillLineItem{
		BillID:                      bill.ID,
		LineType:                    LineItemElectricity,
		Source:                      LineItemSourceAuto,
		Description:                 "bad: ELECTRICITY with recovery FK",
		Amount:                      100,
		AdjustmentRecoveryReadingID: &recovery.ID, // violation
	}
	err = db.Create(badType).Error
	assertCheckViolation(t, err, "bill_line_items_adjustment_fk_only_for_type")
}
