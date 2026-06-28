//go:build integration

// External test package — see service_replacement_lineage_integration_test.go
// for the import-cycle rationale.
package meterreading_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_LineageVsAnalyticsSymmetry is the B5 anchor — THE MOST
// LOAD-BEARING TEST OF THE READING RECOVERY PROJECT. It encodes the locked
// guard's asymmetry as one test in one room, asserting both halves
// side-by-side: lineage MUST see recovery + source rows; analytics MUST NOT.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//	DESIGN:   /Users/anantakit/.claude/plans/lexical-popping-canyon.md (B5)
//
// Why this test is load-bearing:
//
//	B3 protects the anchor_reason exclusion. B4 protects the
//	recovery_source_reading_id exclusion. Both could pass while a future
//	developer accidentally normalizes the lineage and analytics queries
//	together (e.g. "DRY them up by reusing a query builder") — that
//	regression would silently fail next-month inheritance for every room
//	that ever had a recovery. B5 catches the normalization at the source:
//	one room, one recovery + source pair, both query surfaces probed in
//	the same test. A merge-the-queries refactor fails both assertion
//	clusters together; there is no path through B5 that lets lineage and
//	analytics filter the same way.
//
// Scenario:
//
//	Months 1–3 (2026-01 … 2026-03): 3 normal MONTHLY readings via
//	    meterSvc.Create, elec usage 50 each. These exist purely to keep the
//	    baseline pool non-trivial AFTER the exclusion. Without them, B5's
//	    HasEnoughData=false assertion would be indistinguishable from B4's.
//	Month 3 doubles as the SUSPECT SOURCE.
//	Month 4 (2026-04): READING_RECOVERY hand-seeded via meterRepo.Create,
//	    referencing Month 3 as source; prev=curr per doctrine.
//
//	Surviving analytics pool after exclusion: Months 1 + 2 only (2 rows).
//	Surviving lineage chain: all 4 rows. Month 4 is the latest.
//
// What this guards against:
//
//   - Normalizing FindLatestByRoomID and FindRecentByRoomIDs to share a
//     filter set — assertion 1 (lineage sees recovery) fails simultaneously
//     with assertion 3 (analytics excludes both). Both halves of the
//     asymmetry explode in the same test run.
//   - Re-using the analytics filter inside populatePrevious — the next
//     write to the room would inherit from Month 2 instead of Month 4;
//     not directly asserted here, but A1 and B2 (future Phase 5) anchor
//     the chain; B5 makes the asymmetry visible at the query layer.
//   - A baseline pool that "smartly" includes the source row when the
//     recovery's note explains it well — explicitly out of doctrine
//     ("exclude from baseline pool, keep visible"). Assertion 3 fails.
func TestRecovery_LineageVsAnalyticsSymmetry(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-500")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingApplicationChecker{}, txMgr)

	// ── Months 1–3: normal MONTHLY readings, elec usage 50 each ──
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
	_ = create("Month 1", "2026-01", 50, 10)
	_ = create("Month 2", "2026-02", 100, 20)
	m3 := create("Month 3 (suspect source)", "2026-03", 150, 30)

	// ── Month 4: READING_RECOVERY anchor pointing at Month 3 ──
	recoveryMonth := "2026-04"
	recoveryNote := "ค้นพบว่าเดือนมีนาคมจดมิเตอร์ผิด — re-anchor (B5 symmetry)"
	recoveryReason := meterreading.AnchorReasonReadingRecovery
	recovery := &meterreading.MeterReading{
		RoomID:                  rm.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &recoveryMonth,
		ElectricityPrevious:     200, // = current; recovery has usage=0
		ElectricityCurrent:      200,
		WaterPrevious:           45,
		WaterCurrent:            45,
		AnchorReason:            &recoveryReason,
		AnchorNote:              &recoveryNote,
		RecoverySourceReadingID: &m3.ID,
	}
	if err := meterRepo.Create(ctx, recovery); err != nil {
		t.Fatalf("hand-seed Month 4 recovery: %v", err)
	}

	// ──────────────────────────────────────────────────────────────────
	// LINEAGE TRUTH — recovery + source are part of the lineage chain
	// ──────────────────────────────────────────────────────────────────

	// ── Assert 1: FindLatestByRoomID returns the recovery row ──
	// Lineage MUST see recovery rows. If a refactor applied the analytics
	// filter to this query, this assertion fails first.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != recovery.ID {
		t.Errorf("FindLatestByRoomID=%v, want recovery %v — LINEAGE MUST SEE RECOVERY ROWS",
			latest.ID, recovery.ID)
	}

	// ── Assert 2: recovery row's source FK roundtrips to Month 3 ──
	// Lineage truth cross-row: re-read Month 3 via FindByIDSimple and
	// confirm the recovery's FK still points at it. Matches A1's roundtrip
	// pattern from Phase 1.
	persistedRecovery, err := meterRepo.FindByIDSimple(ctx, recovery.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple(recovery): %v", err)
	}
	if persistedRecovery.RecoverySourceReadingID == nil {
		t.Fatalf("persisted recovery.RecoverySourceReadingID = nil, want %v", m3.ID)
	}
	if *persistedRecovery.RecoverySourceReadingID != m3.ID {
		t.Errorf("persisted recovery.RecoverySourceReadingID = %v, want %v (Month 3)",
			*persistedRecovery.RecoverySourceReadingID, m3.ID)
	}
	// Also confirm Month 3 itself still exists and is reachable via lineage.
	persistedSource, err := meterRepo.FindByIDSimple(ctx, m3.ID)
	if err != nil {
		t.Fatalf("FindByIDSimple(source Month 3): %v", err)
	}
	if persistedSource.ID != m3.ID {
		t.Errorf("source Month 3 roundtrip mismatch: got %v, want %v", persistedSource.ID, m3.ID)
	}

	// ──────────────────────────────────────────────────────────────────
	// ANALYTICS TRUTH — recovery + source are BOTH excluded from baseline
	// ──────────────────────────────────────────────────────────────────

	// ── Assert 3 (THE ASYMMETRY, repo layer): FindRecentByRoomIDs returns
	//              exactly 2 readings (Months 1 + 2). Neither the source
	//              (Month 3) nor the recovery (Month 4) appears. ──
	recentMap, err := meterRepo.FindRecentByRoomIDs(ctx, []uuid.UUID{rm.ID}, 6)
	if err != nil {
		t.Fatalf("FindRecentByRoomIDs: %v", err)
	}
	rows := recentMap[rm.ID]
	if len(rows) != 2 {
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Fatalf("FindRecentByRoomIDs returned %d rows, want 2 (Months 1+2 only — Month 3 source AND Month 4 recovery must both be excluded): got IDs %v",
			len(rows), ids)
	}
	for _, r := range rows {
		switch r.ID {
		case m3.ID:
			t.Errorf("source row Month 3 leaked into analytics pool — asymmetry broken at NOT EXISTS subquery")
		case recovery.ID:
			t.Errorf("recovery row Month 4 leaked into analytics pool — asymmetry broken at anchor_reason filter")
		}
	}

	// ── Assert 4 (THE ASYMMETRY, service layer): GetBaselines surfaces
	//              the trimmed pool at the service boundary too. ──
	// 2 surviving rows < 3-row threshold → HasEnoughData=false.
	baselines, err := meterSvc.GetBaselines(ctx, apt.ID)
	if err != nil {
		t.Fatalf("GetBaselines: %v", err)
	}
	bl, ok := baselines[rm.ID]
	if !ok {
		t.Fatalf("GetBaselines returned no entry for room %v", rm.ID)
	}
	if bl.ElectricityHasEnoughData {
		t.Errorf("ElectricityHasEnoughData=true at service layer, want false (analytics pool excluded source+recovery, only 2 rows survive)")
	}
}
