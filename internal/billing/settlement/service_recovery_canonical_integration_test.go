//go:build integration

// service_recovery_canonical_integration_test.go — P5 Layer A (settlement).
// RR05 (pending blocks settlement finalize) + RR06 (resolve → ADJUSTMENT on the
// settlement bill → finalize passes). The settlement half of the canonical
// Reading-Recovery regression; the monthly half lives in package billing.
// Because move-out closure drives FinalizeSettlement via port, gating it here
// also covers the move-out-complete path.
package settlement

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupRecoverySettlement(t *testing.T) (*gorm.DB, *Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "RRS-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000)

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -5)
	fixtures.SeedExitMeter(t, db, rm.ID.String(), moveOutDate, 100, 20)
	fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	svc := buildRealSettlementService(t, db)
	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOutDate, "")
	if err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	return db, svc, result.BillID, rm.ID
}

func seedSettlementRecovery(t *testing.T, db *gorm.DB, roomID uuid.UUID, month string) uuid.UUID {
	t.Helper()
	reason := meterreading.AnchorReasonReadingRecovery
	note := "จดมิเตอร์ผิดตอนย้ายออก " + month
	elecRecorded := 280 // over-record: recorded 280 > physical 180 (diff 100)
	rec := &meterreading.MeterReading{
		RoomID: roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
		ElectricityPrevious: 180, ElectricityCurrent: 180, WaterPrevious: 60, WaterCurrent: 60, // prev=curr
		ElectricityRecorded: &elecRecorded,
		AnchorReason:        &reason, AnchorNote: &note,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed settlement recovery: %v", err)
	}
	return rec.ID
}

func settlementBillStatus(t *testing.T, db *gorm.DB, billID uuid.UUID) billing.BillStatus {
	t.Helper()
	var b billing.Bill
	if err := db.First(&b, "id = ?", billID).Error; err != nil {
		t.Fatalf("reload settlement bill: %v", err)
	}
	return b.Status
}

// RR05 — a pending recovery blocks settlement finalize (and thus move-out close).
func TestRR05_SettlementPendingBlocksFinalize(t *testing.T) {
	db, svc, billID, roomID := setupRecoverySettlement(t)
	seedSettlementRecovery(t, db, roomID, "2026-04")

	if err := svc.FinalizeSettlement(context.Background(), billID); err == nil {
		t.Fatal("expected settlement finalize blocked by pending recovery")
	}
	if got := settlementBillStatus(t, db, billID); got != billing.BillStatusDraft {
		t.Fatalf("bill status = %s, want DRAFT (finalize must not have happened)", got)
	}
}

// RR06 — resolve the recovery into the settlement draft (charge) → ADJUSTMENT on
// the SETTLEMENT bill → finalize passes.
func TestRR06_SettlementResolveThenFinalize(t *testing.T) {
	db, svc, billID, roomID := setupRecoverySettlement(t)
	recID := seedSettlementRecovery(t, db, roomID, "2026-04")

	// ACCEPT the deterministic refund: (280-180) × 800 satang = -80000.
	if _, err := svc.UpdateSettlementDraft(context.Background(), billID, UpdateSettlementDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดที่เก็บเกินจากมิเตอร์ผิดตอนย้ายออก"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve settlement recovery: %v", err)
	}

	var adj []billing.BillLineItem
	if err := db.Where("bill_id = ? AND line_type = ?", billID, billing.LineItemAdjustment).Find(&adj).Error; err != nil {
		t.Fatalf("load adjustment lines: %v", err)
	}
	if len(adj) != 1 {
		t.Fatalf("adjustment lines on settlement = %d, want 1", len(adj))
	}
	if adj[0].Amount != -80000 {
		t.Errorf("amount = %d satang, want -80000 (refund)", adj[0].Amount)
	}
	if adj[0].AdjustmentUtility == nil || *adj[0].AdjustmentUtility != billing.AdjustmentUtilityElectricity {
		t.Errorf("utility = %v, want ELECTRICITY", adj[0].AdjustmentUtility)
	}
	if adj[0].AdjustmentReasonCode == nil || *adj[0].AdjustmentReasonCode != billing.AdjustmentReasonMeterRecovery {
		t.Errorf("reason = %v, want METER_RECOVERY", adj[0].AdjustmentReasonCode)
	}

	if err := svc.FinalizeSettlement(context.Background(), billID); err != nil {
		t.Fatalf("finalize settlement after resolve: %v", err)
	}
	if got := settlementBillStatus(t, db, billID); got != billing.BillStatusFinalized {
		t.Fatalf("bill status = %s, want FINALIZED", got)
	}
}
