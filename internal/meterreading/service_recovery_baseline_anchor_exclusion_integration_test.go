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

// TestRecovery_AnchorRowExcludedFromAnalyticsBaseline is the B3 regression
// anchor for the Reading Recovery analytics-pool exclusion.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//	DESIGN:   /Users/anantakit/.claude/plans/lexical-popping-canyon.md (B3)
//
// What this asserts (Phase 2 ship state):
//
//	A row with anchor_reason set (PHYSICAL_REPLACEMENT in this test, but the
//	filter is anchor_reason IS NOT NULL so READING_RECOVERY would behave
//	identically) is excluded from FindRecentByRoomIDs. The downstream
//	analytics surface (GetBaselines / computeBaseline) sees the trimmed pool
//	and produces a clean median over the surviving consumption months.
//
//	Load-bearing assertion: row-count from FindRecentByRoomIDs. Median is a
//	sanity check, NOT the doctrine assertion — median's even-length lower-
//	middle behavior can hide exclusion bugs (e.g. [50,50,50,100] still
//	yields median 50). The unambiguous signal is "the anchor row's ID is
//	not in the slice."
//
// Why PHYSICAL_REPLACEMENT (not READING_RECOVERY):
//
//	B3's job is to lock the anchor_reason IS NOT NULL filter — the broader
//	of the two doctrine exclusions. REPLACEMENT does not require a source
//	FK (migration 00038 CHECK constraint), so B3 stays narrow on that one
//	filter without entangling the source-pointer mechanic. B4 carries the
//	source-row exclusion; B5 carries the lineage-vs-analytics asymmetry.
//
// Why usage 100 (not 999):
//
//	B3 doesn't test anomaly; it tests exclusion. Dramatic usage values
//	would become reader-noise if the baseline algorithm ever migrates from
//	median to trimmed-mean or weighted-median. 50 baseline / 100 anchor is
//	enough to mark the anchor row as distinct without implying anomaly-
//	test intent.
//
// What this guards against:
//
//   - A future PR that removes the anchor_reason IS NULL filter from
//     FindRecentByRoomIDs — Month 4's anchor row would re-enter the pool;
//     row-count assertion fails.
//   - A future PR that applies the anchor_reason filter to lineage queries
//     (FindLatestByRoomID) — caught by A1, NOT by this test. B3 protects
//     the analytics half; A1 protects the lineage half. The two pair must
//     hold together.
//   - A future PR that flips the predicate sense (IS NOT NULL instead of
//     IS NULL) — every consumption month would be excluded, baseline
//     would never have enough data; assertion 2's HasEnoughData=true fails.
func TestRecovery_AnchorRowExcludedFromAnalyticsBaseline(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	// 6-month contract so all four readings (2026-01 … 2026-04) sit inside
	// the tenant-baseline window (getBaselinesByRoomIDs filters readings
	// whose month is before the contract start month).
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-401")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, fixtures.NoopBillingAdjustmentCommander{}, txMgr)

	// ── Months 1–3: normal MONTHLY readings, elec usage 50 each ──
	// elec currents: 50, 100, 150 → usages 50, 50, 50 → median 50, HasEnoughData=true
	create := func(label, month string, elecCurrent, waterCurrent int) {
		t.Helper()
		if _, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
			RoomID:             rm.ID.String(),
			BillingMonth:       month,
			ElectricityCurrent: elecCurrent,
			WaterCurrent:       waterCurrent,
		}); err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
	}
	create("Month 1", "2026-01", 50, 10)
	create("Month 2", "2026-02", 100, 20)
	create("Month 3", "2026-03", 150, 30)

	// ── Month 4: PHYSICAL_REPLACEMENT anchor, hand-seeded ──
	// Phase 5 will land a service-level anchor commit. Phase 2 hand-seeds
	// via the repository to keep the test narrow on the doctrine filter.
	// elec curr = 250; if the row were ever to enter the pool, its usage
	// (250-150=100) would only nudge median to (50+50)/2=50 anyway — the
	// load-bearing signal is row-count, not median. This number choice
	// (100 not 999) prevents future readers from misinterpreting the test
	// as an anomaly-threshold probe.
	anchorMonth := "2026-04"
	anchorNote := "เปลี่ยนมิเตอร์ไฟฟ้าหลังพังเมื่อ 2026-04-15"
	anchorReason := meterreading.AnchorReasonPhysicalReplacement
	anchor := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &anchorMonth,
		ElectricityPrevious: 150,
		ElectricityCurrent:  250,
		WaterPrevious:       30,
		WaterCurrent:        40,
		AnchorReason:        &anchorReason,
		AnchorNote:          &anchorNote,
	}
	if err := meterRepo.Create(ctx, anchor); err != nil {
		t.Fatalf("hand-seed Month 4 anchor: %v", err)
	}

	// ── Assert 1 (LOAD-BEARING): FindRecentByRoomIDs returns 3 rows, not 4 ──
	// This is the direct doctrine assertion: anchor rows do not enter the
	// analytics pool. The downstream median computation depends on it.
	recentMap, err := meterRepo.FindRecentByRoomIDs(ctx, []uuid.UUID{rm.ID}, 6)
	if err != nil {
		t.Fatalf("FindRecentByRoomIDs: %v", err)
	}
	rows := recentMap[rm.ID]
	if len(rows) != 3 {
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		t.Fatalf("FindRecentByRoomIDs returned %d rows, want 3 (anchor row must NOT enter analytics pool): got IDs %v", len(rows), ids)
	}
	for _, r := range rows {
		if r.ID == anchor.ID {
			t.Errorf("anchor row %v leaked into analytics pool — anchor_reason IS NULL filter regressed", anchor.ID)
		}
	}

	// ── Assert 2: GetBaselines still produces a clean baseline ──
	// 3 rows hits the threshold exactly (computeBaseline requires ≥3).
	// Median of [50,50,50] = 50. HasEnoughData=true.
	baselines, err := meterSvc.GetBaselines(ctx, apt.ID)
	if err != nil {
		t.Fatalf("GetBaselines: %v", err)
	}
	bl, ok := baselines[rm.ID]
	if !ok {
		t.Fatalf("GetBaselines returned no entry for room %v", rm.ID)
	}
	if !bl.ElectricityHasEnoughData {
		t.Errorf("ElectricityHasEnoughData=false, want true (3 surviving rows ≥ threshold)")
	}
	if bl.ElectricityBaseline != 50 {
		t.Errorf("ElectricityBaseline=%d, want 50 (median of [50,50,50])", bl.ElectricityBaseline)
	}

	// ── Assert 3 (sanity): lineage truth unchanged ──
	// FindLatestByRoomID MUST still return the Month 4 anchor — A1's
	// Phase 1 strengthened assertion in this test's domain. If a future
	// PR ever applies the analytics filter to FindLatestByRoomID by
	// reflex, A1 fails first, but B3's sanity catches it too.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != anchor.ID {
		t.Errorf("FindLatestByRoomID=%v, want anchor %v — lineage MUST see anchor rows", latest.ID, anchor.ID)
	}
}
