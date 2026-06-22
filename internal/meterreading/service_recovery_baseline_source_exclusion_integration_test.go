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

// TestRecovery_SourceRowExcludedFromAnalyticsBaseline is the B4 regression
// anchor for the Reading Recovery analytics-pool exclusion (source-row half).
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//	DESIGN:   /Users/anantakit/.claude/plans/lexical-popping-canyon.md (B4)
//
// What this asserts (Phase 2 ship state):
//
//	A row that is referenced by another row's recovery_source_reading_id —
//	the "suspect source" — is excluded from FindRecentByRoomIDs even though
//	the source row itself has anchor_reason = NULL. The NOT EXISTS subquery
//	in the query is responsible; the anchor_reason filter would not catch
//	this case alone.
//
//	The source row remains a regular MONTHLY row from the schema's POV
//	(no anchor metadata). The exclusion is structural: any live recovery
//	pointing at it taints it for analytics purposes. Once the recovery is
//	soft-deleted, the taint clears (handled by src.deleted_at IS NULL in
//	the NOT EXISTS subquery — see the implementation comment in
//	repository.go FindRecentByRoomIDs).
//
// Edge-case note — soft-delete-recovery un-exclusion:
//
//	If a future maintainer soft-deletes the recovery row, the source row
//	(Month 3 here) would naturally re-enter the baseline pool. This is the
//	intended doctrine extension (see lexical-popping-canyon.md item 3):
//	emergent from the query's src.deleted_at IS NULL filter, no separate
//	code path. We don't write a dedicated test for this case because
//	flipping the filter sense would ALSO flip B4's "live recovery excludes
//	source" assertion — one direction protects both.
//
// What this guards against:
//
//   - A future PR that removes the NOT EXISTS subquery from
//     FindRecentByRoomIDs — Month 3 (source) would re-enter the pool;
//     assertion 1 fails (3 rows instead of 2).
//   - A future PR that applies the NOT EXISTS subquery to lineage queries
//     (FindLatestByRoomID) — caught by A1, NOT by this test. B4 protects
//     the analytics half; A1 protects the lineage half.
//   - A future PR that drops the src.deleted_at IS NULL filter — would
//     leave soft-deleted recoveries (retracted) still tainting their
//     sources; B4 doesn't directly assert this, but B4's "live recovery
//     excludes source" structure means flipping the filter to ignore
//     deleted_at would change this test's pass/fail in a related way.
func TestRecovery_SourceRowExcludedFromAnalyticsBaseline(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-402")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txMgr)

	// ── Months 1–2: normal MONTHLY readings ──
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

	// ── Month 3: the SUSPECT SOURCE — a regular MONTHLY (no anchor_reason),
	//              about to be referenced by Month 4's recovery row ──
	// Doctrine: the source does NOT need to be anomalous at create time.
	// What flags it for exclusion is that a recovery exists pointing at it.
	m3 := create("Month 3 (suspect source)", "2026-03", 150, 30)

	// ── Month 4: READING_RECOVERY anchor referencing Month 3 ──
	// Recovery semantics per doctrine: previous = current (usage=0),
	// anchor_reason = READING_RECOVERY, anchor_note required, source FK set.
	recoveryMonth := "2026-04"
	recoveryNote := "ค้นพบว่าเดือนมีนาคมจดมิเตอร์ผิด — re-anchor"
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

	// ── Assert 1 (LOAD-BEARING): FindRecentByRoomIDs returns 2 rows ──
	// Months 1 and 2 survive. Month 3 excluded by the NOT EXISTS subquery
	// (recovery row references it). Month 4 excluded by the anchor_reason
	// IS NULL filter (it's the recovery itself).
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
		t.Fatalf("FindRecentByRoomIDs returned %d rows, want 2 (source + recovery both excluded): got IDs %v", len(rows), ids)
	}
	for _, r := range rows {
		if r.ID == m3.ID {
			t.Errorf("source row Month 3 (%v) leaked into analytics pool — NOT EXISTS subquery regressed", m3.ID)
		}
		if r.ID == recovery.ID {
			t.Errorf("recovery row Month 4 (%v) leaked into analytics pool — anchor_reason filter regressed", recovery.ID)
		}
	}

	// ── Assert 2: GetBaselines reports HasEnoughData=false ──
	// 2 surviving rows < 3-row threshold. Sanity check that the trimmed
	// pool reaches the service surface — if a future refactor adds a
	// fallback (e.g. "include source if recovery has the same month"),
	// HasEnoughData would flip to true and this fails.
	baselines, err := meterSvc.GetBaselines(ctx, apt.ID)
	if err != nil {
		t.Fatalf("GetBaselines: %v", err)
	}
	bl, ok := baselines[rm.ID]
	if !ok {
		t.Fatalf("GetBaselines returned no entry for room %v", rm.ID)
	}
	if bl.ElectricityHasEnoughData {
		t.Errorf("ElectricityHasEnoughData=true, want false (only 2 rows survive exclusion, below 3-row threshold)")
	}

	// ── Assert 3 (sanity): lineage truth — recovery is still latest ──
	// FindLatestByRoomID MUST still return the Month 4 recovery row.
	// Mirrors A1's locked-guard assertion in B4's domain.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != recovery.ID {
		t.Errorf("FindLatestByRoomID=%v, want recovery %v — lineage MUST see recovery rows", latest.ID, recovery.ID)
	}
}
