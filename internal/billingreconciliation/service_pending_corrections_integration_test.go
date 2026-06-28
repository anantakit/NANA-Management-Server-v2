//go:build integration

// External test package — billing imports billingreconciliation for the
// adapter, so a package-internal test importing billing would form a cycle.
// External package + interface-only access keeps the import direction
// one-way while exercising the full DI stack against real Postgres.
package billingreconciliation_test

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/billingreconciliation"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
)

// TestReconcile_PendingBaselineCorrectionsCount_FlipsOnApply locks the
// recon-row signal that drives Item 10 (Phase 7 reconcile→bill bridge):
//
//   - After CreateBaselineCorrection: the room's row reports
//     `PendingBaselineCorrectionsCount = 1`. This is the per-row signal
//     the workspace renders as "ใส่ยอดปรับ N รายการ" so the operator can
//     jump into the bill edit drawer's PendingCorrectionsSection.
//   - After UpdateMonthlyDraft applies the correction: the same room
//     reports `PendingBaselineCorrectionsCount = 0`. Definition mirrors
//     billing.HasNonVoidAdjustmentLineByRecoveryID (the per-bill list's
//     applied-state derivation), batched per room.
//
// Sequence:
//  1. Seed source MONTHLY meter row in a past month.
//  2. Commit baseline correction via meterSvc.CreateBaselineCorrection
//     (Phase 7 meter-only — no bill effect yet).
//  3. Seed a current-month DRAFT bill (the surface the operator will edit).
//  4. Call Reconcile → assert count = 1 on the seeded room.
//  5. Apply via billSvc.UpdateMonthlyDraft.applied_corrections.
//  6. Call Reconcile → assert count = 0 on the same row.
func TestReconcile_PendingBaselineCorrectionsCount_FlipsOnApply(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "REC-101")
	tn := fixtures.SeedTenant(t, db)
	con := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billConfigRepo := billingconfig.NewBillingConfigRepository(db)
	billAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billAppliedChecker, txMgr)
	billSvc := billing.NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, billConfigRepo, nil, txMgr)
	reconAdapter := billing.NewReconciliationAdapter(billRepo, contractRepo, meterRepo, billSvc)
	reconRepo := billingreconciliation.NewRepository(db)
	reconSvc := billingreconciliation.NewService(reconRepo, meterRepo, moveOutRepo, reconAdapter, reconAdapter)

	currentMonth := time.Now().UTC().Format("2006-01")
	srcMonth := time.Now().UTC().AddDate(0, -3, 0).Format("2006-01")

	// Step 1 — seed source MONTHLY meter for the past month + current
	// month (so the room sits in bucket=READY and would normally land a
	// DRAFT bill via Generate; we seed the DRAFT directly to skip that).
	source := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &srcMonth,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1395,
		WaterPrevious:       100,
		WaterCurrent:        130,
	}
	if err := db.Create(source).Error; err != nil {
		t.Fatalf("seed source meter: %v", err)
	}
	current := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &currentMonth,
		ElectricityPrevious: 1395,
		ElectricityCurrent:  1300,
		WaterPrevious:       130,
		WaterCurrent:        135,
	}
	if err := db.Create(current).Error; err != nil {
		t.Fatalf("seed current meter: %v", err)
	}

	// Step 2 — commit baseline correction (Phase 7 meter-only).
	correction, err := meterSvc.CreateBaselineCorrection(ctx, meterreading.CreateBaselineCorrectionInput{
		SourceReadingID:    source.ID,
		ElectricityCurrent: 1300,
		WaterCurrent:       125,
		AnchorNote:         "Item 10 anchor — recon signal flip",
	})
	if err != nil {
		t.Fatalf("CreateBaselineCorrection: %v", err)
	}

	// Step 3 — seed current-month DRAFT bill (proxy for monthly batch).
	draft := &billing.Bill{
		ContractID:   con.ID,
		BillingMonth: currentMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed DRAFT bill: %v", err)
	}

	// Step 4 — Reconcile → assert count = 1 on the seeded room.
	report, err := reconSvc.Reconcile(ctx, billingreconciliation.ReconciliationQuery{
		ApartmentID:  apt.ID.String(),
		BillingMonth: currentMonth,
	})
	if err != nil {
		t.Fatalf("Reconcile (pre-apply): %v", err)
	}
	got := findPendingCount(t, report, rm.ID)
	if got != 1 {
		t.Fatalf("PendingBaselineCorrectionsCount (pre-apply) = %d, want 1", got)
	}

	// Step 5 — apply via UpdateMonthlyDraft.
	if _, err := billSvc.UpdateMonthlyDraft(ctx, draft.ID, billing.UpdateMonthlyDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{{
			RecoveryReadingID: correction.ID.String(),
			Amount:            -81000, // -810 baht refund
			AdjustmentNote:    "Item 10 anchor — apply at bill edit",
		}},
	}, nil); err != nil {
		t.Fatalf("UpdateMonthlyDraft: %v", err)
	}

	// Step 6 — Reconcile again → assert count = 0 on the same row.
	reportAfter, err := reconSvc.Reconcile(ctx, billingreconciliation.ReconciliationQuery{
		ApartmentID:  apt.ID.String(),
		BillingMonth: currentMonth,
	})
	if err != nil {
		t.Fatalf("Reconcile (post-apply): %v", err)
	}
	gotAfter := findPendingCount(t, reportAfter, rm.ID)
	if gotAfter != 0 {
		t.Errorf("PendingBaselineCorrectionsCount (post-apply) = %d, want 0", gotAfter)
	}
}

func findPendingCount(t *testing.T, report *billingreconciliation.Report, roomID uuid.UUID) int {
	t.Helper()
	for _, r := range report.Rooms {
		if r.Room.RoomID == roomID {
			return r.PendingBaselineCorrectionsCount
		}
	}
	t.Fatalf("room %s not present in recon report", roomID)
	return -1
}
