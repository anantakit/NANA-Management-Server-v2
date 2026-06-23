//go:build integration

package meterreading_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_EmitsAuditWithFullPayload is the E2 audit anchor for Phase 5.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md.
//	PLAN:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (E2 + Lock B).
//
// Asserts the audit payload is operator-authoritative (Lock B) — no
// SuggestedAmount or Deviation fields. Forensic capture of system math
// is deferred to post-Phase-5; Phase 5 stores the operator's committed
// Amount as-is alongside Note + RecoveryReadingID + ReasonCode.
func TestRecovery_EmitsAuditWithFullPayload(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "E2-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billRecoveryAdapter := billing.NewRecoveryAdapter(billRepo, billAuditRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billRecoveryAdapter, txMgr)

	srcMonth := time.Now().AddDate(0, -2, 0).Format("2006-01")
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 100,
		ElectricityCurrent:  500,
		WaterPrevious:       40,
		WaterCurrent:        80,
	}
	if err := meterRepo.Create(ctx, source); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	recoveryMonth := time.Now().Format("2006-01")
	draft := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: recoveryMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	operatorNote := "คืนยอดเก็บเกินเดือนเมษายน — มิเตอร์อ่านผิด"
	operatorAmount := int64(-2700)

	recovery, err := meterSvc.CreateRecovery(ctx, meterreading.CreateRecoveryInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 400,
		WaterCurrent:       70,
		Amount:             operatorAmount,
		ReasonCode:         "METER_RECOVERY",
		AnchorNote:         "พบจดเกินจริง",
		AdjustmentNote:     operatorNote,
	})
	if err != nil {
		t.Fatalf("CreateRecovery: %v", err)
	}

	// Assert ADJUSTMENT line on the DRAFT bill.
	var lines []billing.BillLineItem
	if err := db.Where("bill_id = ? AND line_type = ?", draft.ID, billing.LineItemAdjustment).Find(&lines).Error; err != nil {
		t.Fatalf("query line items: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("ADJUSTMENT lines on DRAFT = %d, want 1", len(lines))
	}
	li := lines[0]
	if li.Amount != operatorAmount {
		t.Errorf("line.Amount=%d, want %d (operator-authoritative)", li.Amount, operatorAmount)
	}
	if li.Source != billing.LineItemSourceManual {
		t.Errorf("line.Source=%v, want MANUAL", li.Source)
	}
	if li.AdjustmentRecoveryReadingID == nil || *li.AdjustmentRecoveryReadingID != recovery.ID {
		t.Errorf("line.AdjustmentRecoveryReadingID=%v, want %v (FK provenance)", li.AdjustmentRecoveryReadingID, recovery.ID)
	}
	if li.AdjustmentReasonCode == nil || *li.AdjustmentReasonCode != billing.AdjustmentReasonMeterRecovery {
		t.Errorf("line.AdjustmentReasonCode=%v, want METER_RECOVERY", li.AdjustmentReasonCode)
	}
	if !strings.Contains(li.Description, "จดมิเตอร์ผิด") {
		t.Errorf("Description=%q, want to contain 'จดมิเตอร์ผิด'", li.Description)
	}
	if !strings.Contains(li.Description, srcMonth) {
		t.Errorf("Description=%q, want to contain source month %s", li.Description, srcMonth)
	}

	// Assert bill_audit_log row.
	var logs []billing.BillAuditLog
	if err := db.Where("bill_id = ? AND action = ?", draft.ID, billing.AuditApplyRecoveryAdjustment).Find(&logs).Error; err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(logs))
	}

	// Decode payload to inspect operator-authoritative shape (Lock B).
	var payload map[string]interface{}
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if amt, ok := payload["amount"].(float64); !ok || int64(amt) != operatorAmount {
		t.Errorf("payload.amount=%v, want %d", payload["amount"], operatorAmount)
	}
	if rrid, _ := payload["recovery_reading_id"].(string); rrid != recovery.ID.String() {
		t.Errorf("payload.recovery_reading_id=%v, want %v", payload["recovery_reading_id"], recovery.ID)
	}
	if rc, _ := payload["reason_code"].(string); rc != "METER_RECOVERY" {
		t.Errorf("payload.reason_code=%v, want METER_RECOVERY", payload["reason_code"])
	}
	if note, _ := payload["note"].(string); note != operatorNote {
		t.Errorf("payload.note=%v, want %q", payload["note"], operatorNote)
	}

	// Lock B doctrine assertion: NO SuggestedAmount or Deviation keys.
	if _, present := payload["suggested_amount"]; present {
		t.Errorf("payload.suggested_amount present; Phase 5 ships operator-authoritative only (Lock B)")
	}
	if _, present := payload["deviation"]; present {
		t.Errorf("payload.deviation present; Phase 5 ships operator-authoritative only (Lock B)")
	}
}
