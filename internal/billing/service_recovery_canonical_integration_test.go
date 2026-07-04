//go:build integration

// service_recovery_canonical_integration_test.go — P5 Layer A (monthly).
// Canonical Reading-Recovery regression, Q1.5 over-record model. Monthly cases:
// RR01 (pending blocks), RR02 refund-accept / RR03 refund-override / RR04 waive,
// RR07 (multi-recovery partial), RR08 (re-baseline usage→0), RR09 (source-less),
// RR10 (electricity-only, water bills normally), RR11 (deterministic override
// bound), RR12 (non-over-record does not engage), and the 3 finalize-gate cases.
// Settlement RR05/RR06 live in billing/settlement.
//
// These lock BUSINESS INVARIANTS against real Postgres: refund-only sign,
// per-utility resolution, re-baseline, the per-(recovery,utility) finalize gate,
// and the over-record precondition — so the doctrine survives any UI rewrite.
package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type recoveryMonthlyEnv struct {
	db        *gorm.DB
	svc       BillingService
	ctx       context.Context
	billID    uuid.UUID
	roomID    uuid.UUID
	elecRate  int64
	waterRate int64
}

func intPtr(v int) *int { return &v }

// setupRecoveryMonthly wires a real billing service + one DRAFT MONTHLY bill
// with AUTO electricity + water lines (so re-baseline has something to zero and
// the bill can finalize).
func setupRecoveryMonthly(t *testing.T) recoveryMonthlyEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "RR-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	svc := NewBillingService(
		NewBillingRepository(db),
		NewBillAuditRepository(db),
		&integrationContractStub{c: c},
		meterreading.NewMeterReadingRepository(db),
		&integrationConfigStub{},
		nil,
		database.NewTxManager(db),
	)

	bill := &Bill{ContractID: c.ID, BillingMonth: "2026-05", BillType: BillTypeMonthly, Status: BillStatusDraft}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed draft bill: %v", err)
	}
	for _, li := range []BillLineItem{
		{BillID: bill.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Description: "ค่าไฟฟ้า", Quantity: 120, UnitPrice: c.ElectricityRatePerUnit, Amount: 120 * c.ElectricityRatePerUnit, SortOrder: 1},
		{BillID: bill.ID, LineType: LineItemWater, Source: LineItemSourceAuto, Description: "ค่าน้ำ", Quantity: 20, UnitPrice: c.WaterRatePerUnit, Amount: 20 * c.WaterRatePerUnit, SortOrder: 2},
	} {
		if err := db.Create(&li).Error; err != nil {
			t.Fatalf("seed auto line: %v", err)
		}
	}

	return recoveryMonthlyEnv{
		db: db, svc: svc, ctx: context.Background(), billID: bill.ID, roomID: rm.ID,
		elecRate: c.ElectricityRatePerUnit, waterRate: c.WaterRatePerUnit,
	}
}

// seedRecovery inserts a READING_RECOVERY meter row. prev=curr=physical (Lock A);
// recorded (nullable per utility) is the previously-recorded wrong value. Pass
// nil to leave a utility uncorrected. withSource exercises the source path.
func (e recoveryMonthlyEnv) seedRecovery(t *testing.T, month string, withSource bool, elecRecorded, waterRecorded *int) uuid.UUID {
	t.Helper()
	var sourceID *uuid.UUID
	if withSource {
		sm := "2026-03"
		src := &meterreading.MeterReading{
			RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sm,
			ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 55,
		}
		if err := e.db.Create(src).Error; err != nil {
			t.Fatalf("seed source: %v", err)
		}
		sourceID = &src.ID
	}
	reason := meterreading.AnchorReasonReadingRecovery
	note := "จดมิเตอร์ผิด " + month
	rec := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
		ElectricityPrevious: 180, ElectricityCurrent: 180, WaterPrevious: 60, WaterCurrent: 60, // prev=curr (physical)
		ElectricityRecorded: elecRecorded, WaterRecorded: waterRecorded,
		AnchorReason: &reason, AnchorNote: &note, RecoverySourceReadingID: sourceID,
	}
	if err := e.db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	return rec.ID
}

func (e recoveryMonthlyEnv) adjustmentLines(t *testing.T) []BillLineItem {
	t.Helper()
	var lines []BillLineItem
	if err := e.db.Where("bill_id = ? AND line_type = ?", e.billID, LineItemAdjustment).Find(&lines).Error; err != nil {
		t.Fatalf("load adjustment lines: %v", err)
	}
	return lines
}

func (e recoveryMonthlyEnv) autoLine(t *testing.T, lt LineItemType) BillLineItem {
	t.Helper()
	var li BillLineItem
	if err := e.db.First(&li, "bill_id = ? AND line_type = ? AND source = ?", e.billID, lt, LineItemSourceAuto).Error; err != nil {
		t.Fatalf("load auto %s line: %v", lt, err)
	}
	return li
}

func (e recoveryMonthlyEnv) billStatus(t *testing.T) BillStatus {
	t.Helper()
	var b Bill
	if err := e.db.First(&b, "id = ?", e.billID).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	return b.Status
}

func (e recoveryMonthlyEnv) resolve(t *testing.T, inputs ...AppliedCorrectionInput) error {
	t.Helper()
	_, err := e.svc.UpdateMonthlyDraft(e.ctx, e.billID, UpdateMonthlyDraftRequest{AppliedCorrections: inputs}, nil)
	return err
}

// RR01 — monthly over-record pending blocks finalize.
func TestRR01_MonthlyPendingBlocksFinalize(t *testing.T) {
	e := setupRecoveryMonthly(t)
	e.seedRecovery(t, "2026-04", true, intPtr(280), nil) // elec over-record

	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
		t.Fatal("expected finalize blocked by pending over-record")
	}
	if got := e.billStatus(t); got != BillStatusDraft {
		t.Fatalf("bill status = %s, want DRAFT", got)
	}
}

// RR02/RR03/RR04 — resolve refund-accept / refund-override / waive → correct
// ADJUSTMENT → finalize passes. Charge is gone (refund-only).
func TestRR02_03_04_MonthlyResolveThenFinalize(t *testing.T) {
	t.Run("RR02 refund-accept", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		recID := e.seedRecovery(t, "2026-04", true, intPtr(280), nil) // diff 100
		wantAmount := -(100 * e.elecRate)
		if err := e.resolve(t, AppliedCorrectionInput{
			RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT",
			AdjustmentNote: "คืนยอดที่เก็บเกินจากมิเตอร์ผิด",
		}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		adj := e.adjustmentLines(t)
		if len(adj) != 1 || adj[0].Amount != wantAmount {
			t.Fatalf("adjustment = %+v, want one line amount %d", adj, wantAmount)
		}
		if *adj[0].AdjustmentReasonCode != AdjustmentReasonMeterRecovery || *adj[0].AdjustmentUtility != AdjustmentUtilityElectricity {
			t.Errorf("reason/utility = %v/%v", *adj[0].AdjustmentReasonCode, *adj[0].AdjustmentUtility)
		}
		mustFinalize(t, e)
	})

	t.Run("RR03 refund-override (partial)", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		recID := e.seedRecovery(t, "2026-04", true, intPtr(680), nil) // diff 500 → recommended big
		override := 1.0                                               // 1 baht = 100 satang, well under recommended
		if err := e.resolve(t, AppliedCorrectionInput{
			RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "OVERRIDE",
			OverrideRefundBaht: &override, AdjustmentNote: "คืนบางส่วนตามที่ตกลง",
		}); err != nil {
			t.Fatalf("resolve override: %v", err)
		}
		adj := e.adjustmentLines(t)
		if len(adj) != 1 || adj[0].Amount != -100 {
			t.Fatalf("override adjustment = %+v, want amount -100", adj)
		}
		mustFinalize(t, e)
	})

	t.Run("RR04 waive", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		recID := e.seedRecovery(t, "2026-04", true, intPtr(280), nil)
		if err := e.resolve(t, AppliedCorrectionInput{
			RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "WAIVE",
			AdjustmentNote: "ตรวจแล้วไม่คืนเงิน",
		}); err != nil {
			t.Fatalf("resolve waive: %v", err)
		}
		adj := e.adjustmentLines(t)
		if len(adj) != 1 || adj[0].Amount != 0 || *adj[0].AdjustmentReasonCode != AdjustmentReasonMeterRecoveryWaived {
			t.Fatalf("waive adjustment = %+v, want zero METER_RECOVERY_WAIVED", adj)
		}
		mustFinalize(t, e)
	})
}

// RR07 — multiple recoveries: resolve one, still blocked; resolve all, passes.
func TestRR07_MultiRecoveryPartialResolveStillBlocks(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recA := e.seedRecovery(t, "2026-03", false, intPtr(280), nil)
	recB := e.seedRecovery(t, "2026-04", false, intPtr(280), nil)

	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recA.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนเงิน recA",
	}); err != nil {
		t.Fatalf("resolve recA: %v", err)
	}
	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
		t.Fatal("expected still blocked with recB unresolved")
	}

	if err := e.resolve(t,
		AppliedCorrectionInput{RecoveryReadingID: recA.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนเงิน recA"},
		AppliedCorrectionInput{RecoveryReadingID: recB.String(), Utility: "ELECTRICITY", Decision: "WAIVE", AdjustmentNote: "recB ไม่คืนเงิน"},
	); err != nil {
		t.Fatalf("resolve both: %v", err)
	}
	mustFinalize(t, e)
}

// RR08 — re-baseline: resolving an affected utility zeroes its AUTO line
// (usage 0, amount 0); the recovery row is untouched; the refund is a separate
// line. (Next-cycle previous=physical is inherent in the meter chain and locked
// by meterreading's next_month_inheritance test.)
func TestRR08_ReBaselineZeroesAffectedAutoLine(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recID := e.seedRecovery(t, "2026-04", false, intPtr(280), nil) // elec affected, water not

	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดที่เก็บเกิน",
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	elec := e.autoLine(t, LineItemElectricity)
	if elec.Amount != 0 || elec.Quantity != 0 {
		t.Errorf("electricity AUTO line = amount %d / qty %d, want 0/0 (re-baselined)", elec.Amount, elec.Quantity)
	}
	// Water is untouched — different utility, not affected.
	if water := e.autoLine(t, LineItemWater); water.Amount != 20*e.waterRate {
		t.Errorf("water AUTO line = %d, want unchanged %d", water.Amount, 20*e.waterRate)
	}
	// Recovery row is untouched (still prev=curr=physical).
	var rec meterreading.MeterReading
	if err := e.db.First(&rec, "id = ?", recID).Error; err != nil {
		t.Fatalf("reload recovery: %v", err)
	}
	if rec.ElectricityCurrent != 180 || rec.ElectricityPrevious != 180 {
		t.Errorf("recovery row mutated: prev/curr = %d/%d, want 180/180", rec.ElectricityPrevious, rec.ElectricityCurrent)
	}
	mustFinalize(t, e)
}

// RR09 — source-less over-record resolves; FK provenance intact.
func TestRR09_SourceLessRecoveryResolves(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recID := e.seedRecovery(t, "2026-04", false, intPtr(280), nil)

	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอด (ไม่ทราบต้นทาง)",
	}); err != nil {
		t.Fatalf("resolve source-less: %v", err)
	}
	adj := e.adjustmentLines(t)
	if len(adj) != 1 || adj[0].Amount != -(100*e.elecRate) {
		t.Fatalf("source-less adjustment = %+v", adj)
	}
	if adj[0].AdjustmentRecoveryReadingID == nil || *adj[0].AdjustmentRecoveryReadingID != recID {
		t.Errorf("recovery FK = %v, want %v", adj[0].AdjustmentRecoveryReadingID, recID)
	}
	mustFinalize(t, e)
}

// RR10 — electricity-only recovery: water is untouched and bills normally; only
// electricity is resolvable, and only the electricity AUTO line re-baselines.
func TestRR10_ElectricityOnlyWaterBillsNormally(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recID := e.seedRecovery(t, "2026-04", false, intPtr(280), nil) // water recorded nil

	// Resolving WATER (not affected) is rejected.
	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recID.String(), Utility: "WATER", Decision: "ACCEPT", AdjustmentNote: "ไม่ควรทำได้ (น้ำไม่ over-record)",
	}); err == nil {
		t.Fatal("expected resolving unaffected water to be rejected")
	}

	// Resolving electricity succeeds; water AUTO line stays, one adjustment only.
	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดไฟฟ้า",
	}); err != nil {
		t.Fatalf("resolve elec: %v", err)
	}
	if adj := e.adjustmentLines(t); len(adj) != 1 || *adj[0].AdjustmentUtility != AdjustmentUtilityElectricity {
		t.Fatalf("want exactly one ELECTRICITY adjustment, got %+v", adj)
	}
	if water := e.autoLine(t, LineItemWater); water.Amount != 20*e.waterRate {
		t.Errorf("water billed abnormally: %d, want %d", water.Amount, 20*e.waterRate)
	}
	mustFinalize(t, e)
}

// RR11 — deterministic bound: an override larger than the recommended refund
// (or a charge) is rejected; the recommendation is the ceiling.
func TestRR11_OverrideCannotExceedRecommended(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recID := e.seedRecovery(t, "2026-04", false, intPtr(280), nil) // diff 100 → recommended 100*rate satang
	tooBig := float64(100*e.elecRate)/100 + 1                      // 1 baht over the recommended magnitude

	if err := e.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "OVERRIDE",
		OverrideRefundBaht: &tooBig, AdjustmentNote: "พยายามคืนเกินยอดแนะนำ",
	}); err == nil {
		t.Fatal("expected over-refund override to be rejected")
	}
	if adj := e.adjustmentLines(t); len(adj) != 0 {
		t.Errorf("no adjustment should be created on rejected override, got %d", len(adj))
	}
}

// RR12 — a non-over-record recovery (recorded absent / <= physical) does not
// engage: it never blocks finalize, and resolving it is rejected.
func TestRR12_NonOverRecordDoesNotEngage(t *testing.T) {
	e := setupRecoveryMonthly(t)
	e.seedRecovery(t, "2026-04", false, nil, nil) // no recorded → not affected

	// Not affected → gate does not block → finalize passes without resolution.
	mustFinalize(t, e)

	// And attempting to resolve it is rejected (nothing to refund).
	e2 := setupRecoveryMonthly(t)
	rec2 := e2.seedRecovery(t, "2026-04", false, nil, nil)
	if err := e2.resolve(t, AppliedCorrectionInput{
		RecoveryReadingID: rec2.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "ไม่ควรทำได้",
	}); err == nil {
		t.Fatal("expected resolving a non-over-record utility to be rejected")
	}
}

// The three mandated finalize-gate cases (owner focus #6).
func TestRRGate_PerUtilityFinalizeGate(t *testing.T) {
	t.Run("elec affected unresolved → blocks", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		e.seedRecovery(t, "2026-04", false, intPtr(280), nil)
		if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
			t.Fatal("want blocked")
		}
	})
	t.Run("elec resolved + water unaffected → allows", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		recID := e.seedRecovery(t, "2026-04", false, intPtr(280), nil) // water nil
		if err := e.resolve(t, AppliedCorrectionInput{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดไฟฟ้า"}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		mustFinalize(t, e)
	})
	t.Run("elec resolved + water affected unresolved → blocks", func(t *testing.T) {
		e := setupRecoveryMonthly(t)
		recID := e.seedRecovery(t, "2026-04", false, intPtr(280), intPtr(160)) // both affected
		if err := e.resolve(t, AppliedCorrectionInput{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดไฟฟ้าเท่านั้น"}); err != nil {
			t.Fatalf("resolve elec: %v", err)
		}
		if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
			t.Fatal("want blocked while water still affected+unresolved")
		}
	})
}

func mustFinalize(t *testing.T, e recoveryMonthlyEnv) {
	t.Helper()
	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err != nil {
		t.Fatalf("finalize after resolve: %v", err)
	}
	if got := e.billStatus(t); got != BillStatusFinalized {
		t.Fatalf("bill status = %s, want FINALIZED", got)
	}
}
