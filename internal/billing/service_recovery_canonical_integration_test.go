//go:build integration

// service_recovery_canonical_integration_test.go — P5 Layer A (monthly + both).
// The canonical Reading-Recovery regression: RR01-RR04 (monthly destination),
// RR07 (multi-recovery partial resolve), RR09 (source-less). Settlement cases
// RR05/RR06 live in billing/settlement (that package owns the move-out harness).
// RR08 (void → re-pending) is already locked by
// meterreading/service_baseline_correction_bill_void_returns_to_pending.
//
// These lock BUSINESS INVARIANTS against real Postgres, not UI behavior:
// money sign (+/−/0), METER_RECOVERY_WAIVED semantics, the finalize gate
// (pending blocks / resolved passes), and source-optional resolve — so the
// doctrine survives any future UI rewrite.
package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type recoveryMonthlyEnv struct {
	db     *gorm.DB
	svc    BillingService
	ctx    context.Context
	billID uuid.UUID
	roomID uuid.UUID
}

// setupRecoveryMonthly wires a real billing service + one DRAFT MONTHLY bill
// (with an AUTO line so it can finalize) for the given contract's room.
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
	if err := db.Create(&BillLineItem{
		BillID: bill.ID, LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 96000, SortOrder: 1,
	}).Error; err != nil {
		t.Fatalf("seed auto line: %v", err)
	}

	return recoveryMonthlyEnv{db: db, svc: svc, ctx: context.Background(), billID: bill.ID, roomID: rm.ID}
}

// seedRecovery inserts a READING_RECOVERY meter row for the env's room.
// withSource=false exercises the source-optional path (RR09).
func (e recoveryMonthlyEnv) seedRecovery(t *testing.T, month string, withSource bool) uuid.UUID {
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
		ElectricityPrevious: 180, ElectricityCurrent: 180, WaterPrevious: 60, WaterCurrent: 60, // prev=curr (migration 00040)
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

func (e recoveryMonthlyEnv) billStatus(t *testing.T) BillStatus {
	t.Helper()
	var b Bill
	if err := e.db.First(&b, "id = ?", e.billID).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	return b.Status
}

// RR01 — monthly recovery pending blocks finalize.
func TestRR01_MonthlyPendingBlocksFinalize(t *testing.T) {
	e := setupRecoveryMonthly(t)
	e.seedRecovery(t, "2026-04", true)

	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
		t.Fatal("expected finalize blocked by pending recovery")
	}
	if got := e.billStatus(t); got != BillStatusDraft {
		t.Fatalf("bill status = %s, want DRAFT (finalize must not have happened)", got)
	}
}

// RR02/RR03/RR04 — resolve charge/refund/waive → correct ADJUSTMENT → finalize passes.
func TestRR02_03_04_MonthlyResolveThenFinalize(t *testing.T) {
	cases := []struct {
		name       string
		input      AppliedCorrectionInput
		wantAmount int64
		wantReason AdjustmentReasonCode
	}{
		{"RR02 charge", AppliedCorrectionInput{Amount: 500, AdjustmentNote: "เก็บยอดเพิ่มจากมิเตอร์ผิด"}, 50000, AdjustmentReasonMeterRecovery},
		{"RR03 refund", AppliedCorrectionInput{Amount: -500, AdjustmentNote: "คืนยอดเก็บเกินจากมิเตอร์ผิด"}, -50000, AdjustmentReasonMeterRecovery},
		{"RR04 waive", AppliedCorrectionInput{Waive: true, AdjustmentNote: "ตรวจแล้วไม่คิดเงินเพิ่ม"}, 0, AdjustmentReasonMeterRecoveryWaived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setupRecoveryMonthly(t)
			recID := e.seedRecovery(t, "2026-04", true)
			in := tc.input
			in.RecoveryReadingID = recID.String()

			if _, err := e.svc.UpdateMonthlyDraft(e.ctx, e.billID, UpdateMonthlyDraftRequest{
				AppliedCorrections: []AppliedCorrectionInput{in},
			}, nil); err != nil {
				t.Fatalf("resolve: %v", err)
			}

			adj := e.adjustmentLines(t)
			if len(adj) != 1 {
				t.Fatalf("adjustment lines = %d, want 1", len(adj))
			}
			if adj[0].Amount != tc.wantAmount {
				t.Errorf("amount = %d, want %d", adj[0].Amount, tc.wantAmount)
			}
			if adj[0].AdjustmentReasonCode == nil || *adj[0].AdjustmentReasonCode != tc.wantReason {
				t.Errorf("reason = %v, want %v", adj[0].AdjustmentReasonCode, tc.wantReason)
			}

			// Resolved → finalize passes.
			if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err != nil {
				t.Fatalf("finalize after resolve: %v", err)
			}
			if got := e.billStatus(t); got != BillStatusFinalized {
				t.Fatalf("bill status = %s, want FINALIZED", got)
			}
		})
	}
}

// RR07 — multiple recoveries: resolve one, still blocked; resolve all, passes.
func TestRR07_MultiRecoveryPartialResolveStillBlocks(t *testing.T) {
	e := setupRecoveryMonthly(t)
	// Source-less to keep two recoveries independent (sources would collide on
	// the room+month unique index); this case tests the multi-recovery gate.
	recA := e.seedRecovery(t, "2026-03", false)
	recB := e.seedRecovery(t, "2026-04", false)

	// Partial resolve (only recA in this save) → recB stays pending → blocked.
	// UpdateMonthlyDraft is a replace-per-save op, so the UI resolves every
	// pending recovery in one save; here we prove a save that omits recB blocks.
	if _, err := e.svc.UpdateMonthlyDraft(e.ctx, e.billID, UpdateMonthlyDraftRequest{
		AppliedCorrections: []AppliedCorrectionInput{
			{RecoveryReadingID: recA.String(), Amount: 300, AdjustmentNote: "เก็บเพิ่ม recA จากมิเตอร์ผิด"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve recA: %v", err)
	}
	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err == nil {
		t.Fatal("expected still blocked with recB unresolved")
	}

	// One save resolving BOTH → all resolved → finalize passes.
	if _, err := e.svc.UpdateMonthlyDraft(e.ctx, e.billID, UpdateMonthlyDraftRequest{
		AppliedCorrections: []AppliedCorrectionInput{
			{RecoveryReadingID: recA.String(), Amount: 300, AdjustmentNote: "เก็บเพิ่ม recA จากมิเตอร์ผิด"},
			{RecoveryReadingID: recB.String(), Waive: true, AdjustmentNote: "recB ไม่คิดเงินเพิ่ม"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve both: %v", err)
	}
	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err != nil {
		t.Fatalf("finalize after resolving all: %v", err)
	}
	if got := e.billStatus(t); got != BillStatusFinalized {
		t.Fatalf("bill status = %s, want FINALIZED", got)
	}
}

// RR09 — source-less recovery resolves without breaking provenance.
func TestRR09_SourceLessRecoveryResolves(t *testing.T) {
	e := setupRecoveryMonthly(t)
	recID := e.seedRecovery(t, "2026-04", false) // no source

	if _, err := e.svc.UpdateMonthlyDraft(e.ctx, e.billID, UpdateMonthlyDraftRequest{
		AppliedCorrections: []AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Amount: -200, AdjustmentNote: "คืนยอดเก็บเกิน (ไม่ทราบต้นทาง)"},
		},
	}, nil); err != nil {
		t.Fatalf("resolve source-less: %v", err)
	}
	adj := e.adjustmentLines(t)
	if len(adj) != 1 || adj[0].Amount != -20000 {
		t.Fatalf("source-less adjustment = %+v, want one line amount -20000", adj)
	}
	// FK provenance still points at the recovery row.
	if adj[0].AdjustmentRecoveryReadingID == nil || *adj[0].AdjustmentRecoveryReadingID != recID {
		t.Errorf("recovery FK = %v, want %v", adj[0].AdjustmentRecoveryReadingID, recID)
	}
	if _, err := e.svc.FinalizeBill(e.ctx, e.billID, nil); err != nil {
		t.Fatalf("finalize source-less: %v", err)
	}
}

// money.ToSatang guard — keeps the ×100 assumption honest for the amounts above.
func TestRecoveryAmountSatangAssumption(t *testing.T) {
	if money.ToSatang(500) != 50000 || money.ToSatang(-500) != -50000 {
		t.Fatal("baht→satang conversion changed; canonical expected amounts need updating")
	}
}
