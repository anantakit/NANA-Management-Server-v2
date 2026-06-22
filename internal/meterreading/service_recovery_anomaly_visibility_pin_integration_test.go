//go:build integration

// External test package — see service_replacement_lineage_integration_test.go
// for the import-cycle rationale.
package meterreading_test

import (
	"context"
	"testing"

	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_AnomalyFlagPersistsAcrossSubsequentReadings is a Phase 0
// regression pin for the Reading Recovery auditability guard.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//
// Phase 0 assertion (green today):
//
//	An anomaly flag set on a reading at create time is NOT cleared by the
//	creation of subsequent MONTHLY readings on the same room. The flag is
//	historical evidence — once set, it stays set.
//
// Phase 5 strengthening (added when recovery commit transaction lands):
//
//	Create a READING_RECOVERY row that references this reading as
//	recovery_source_reading_id, then re-fetch the source — anomaly flag
//	STILL true. The recovery commit must NOT clear the source's flag
//	("auditability over cleanup", doctrine guard #1).
//
// What this pin guards against:
//
//   - Phase 5 recovery commit logic clearing the source's anomaly flag
//     as part of "tidying up." Locked doctrine guard #1 explicitly
//     forbids this: "Reject any proposal to clear the source reading's
//     original anomaly flag. The goal is NOT eliminating the error — it
//     is keeping the error visible as evidence while preventing it from
//     poisoning future analytics."
//   - Any future write path that mutates a non-latest reading's anomaly
//     flag as a side effect. The flag is a one-shot signal that fires
//     at create-time and must remain stable thereafter.
func TestRecovery_AnomalyFlagPersistsAcrossSubsequentReadings(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	// Contract starts 6 months ago so all five readings below sit within
	// the current-tenant baseline window (getBaselinesByRoomIDs filters
	// readings whose month is not before the contract start month).
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-301")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txMgr)

	create := func(label, month string, elecCurrent, waterCurrent int) *meterreading.MeterReadingWithRoom {
		t.Helper()
		m, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
			RoomID:             rm.ID.String(),
			BillingMonth:       month,
			ElectricityCurrent: elecCurrent,
			WaterCurrent:       waterCurrent,
		})
		if err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
		return m
	}

	// ── Months 1–3: normal MONTHLY readings to seed the baseline pool ──
	// Each contributes electricity usage 50. After Month 3 (≥3 readings)
	// computeBaseline returns ElectricityHasEnoughData=true with median 50.
	_ = create("Month 1", "2026-01", 50, 10)
	_ = create("Month 2", "2026-02", 100, 20)
	_ = create("Month 3", "2026-03", 150, 30)

	// ── Month 4: HIGH usage → anomaly flag fires at create time ──
	// Baseline electricity = median(50,50,50) = 50.
	// Anomaly threshold: usage > 75 (baseline*3/2) AND usage > 50.
	// Month 4 electricity usage = 350 - 150 = 200 → clears both → flag set.
	m4 := create("Month 4 (anomalous)", "2026-04", 350, 40)
	if !m4.IsAnomalyElectricity {
		t.Fatalf("Month 4 IsAnomalyElectricity = false at create time, want true (usage 200 vs baseline 50 — pin premise broken)")
	}

	// ── Month 5: normal usage AFTER the anomalous reading ──
	// Anomaly is computed ONLY against the new row's usage at create time
	// (service.go: reading.ComputeAnomalies(bl) before persist). Creating
	// Month 5 must not mutate Month 4's flag.
	_ = create("Month 5", "2026-05", 400, 50)

	// ── Assert: source's anomaly flag persists ──
	// Re-fetch Month 4 by ID. Flag MUST still be true — the pin's
	// load-bearing assertion. Phase 5's recovery commit logic is the
	// primary threat surface, but the pin fires even earlier: any future
	// write path that mutates a non-latest reading's anomaly flag will
	// trip this.
	refetched, err := meterRepo.FindByIDSimple(ctx, m4.ID)
	if err != nil {
		t.Fatalf("re-fetch Month 4 after Month 5 create: %v", err)
	}
	if !refetched.IsAnomalyElectricity {
		t.Error("Month 4 IsAnomalyElectricity flipped to false after Month 5 was created — anomaly flag must persist as historical evidence")
	}
}
