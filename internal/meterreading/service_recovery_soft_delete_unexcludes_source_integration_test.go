//go:build integration

package meterreading_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"nana/internal/meterreading"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"
)

// TestRecovery_SoftDeletingRecoveryUnexcludesSource is the E3 anchor for
// Phase 2's emergent soft-delete behavior, now that Phase 5 ships the
// recovery write path.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md.
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md.
//	PLAN:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (E3).
//
// Phase 2's FindRecentByRoomIDs NOT EXISTS subquery has `src.deleted_at IS NULL`,
// which means: if a recovery row is soft-deleted, its source row is no
// longer "tainted" — the source un-excludes from the analytics pool.
//
// This is a meter-side-only test (no bill, no service path). The recovery
// row is hand-seeded so we can directly soft-delete it via db.Delete()
// and observe the analytics pool behavior. Phase 5 does NOT ship a
// service-level soft-delete-recovery operation; this test pins the
// structural guarantee that emerges from Phase 2's filter shape.
func TestRecovery_SoftDeletingRecoveryUnexcludesSource(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "E3-101")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6)

	// Seed 3 prior MONTHLY readings — Month 1, 2, 3 (source is Month 3).
	makeReading := func(month string, elecCurr int) *meterreading.MeterReading {
		m := &meterreading.MeterReading{
			RoomID:              rm.ID,
			ReadingType:         meterreading.ReadingTypeMonthly,
			BillingMonth:        &month,
			ElectricityPrevious: elecCurr - 50,
			ElectricityCurrent:  elecCurr,
			WaterPrevious:       elecCurr/2 - 5,
			WaterCurrent:        elecCurr / 2,
		}
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("seed %s: %v", month, err)
		}
		return m
	}
	m1 := makeReading(time.Now().AddDate(0, -3, 0).Format("2006-01"), 50)
	m2 := makeReading(time.Now().AddDate(0, -2, 0).Format("2006-01"), 100)
	m3 := makeReading(time.Now().AddDate(0, -1, 0).Format("2006-01"), 150)

	// Hand-seed a recovery row pointing at m3 (prev=curr per doctrine).
	recoveryReason := meterreading.AnchorReasonReadingRecovery
	note := "Phase 5 E3 — soft-delete un-exclusion test"
	recoveryMonth := time.Now().Format("2006-01")
	recovery := &meterreading.MeterReading{
		RoomID:                  rm.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &recoveryMonth,
		ElectricityPrevious:     200, // = current
		ElectricityCurrent:      200,
		WaterPrevious:           80,
		WaterCurrent:            80,
		AnchorReason:            &recoveryReason,
		AnchorNote:              &note,
		RecoverySourceReadingID: &m3.ID,
	}
	if err := db.Create(recovery).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}

	meterRepo := meterreading.NewMeterReadingRepository(db)

	// BEFORE soft-delete: m3 excluded from the analytics pool (Phase 2 B4).
	beforeMap, err := meterRepo.FindRecentByRoomIDs(ctx, []uuid.UUID{rm.ID}, 6)
	if err != nil {
		t.Fatalf("FindRecentByRoomIDs (before): %v", err)
	}
	beforeIDs := idSet(beforeMap[rm.ID])
	if beforeIDs[m3.ID] {
		t.Errorf("BEFORE soft-delete: m3=%v leaked into pool — Phase 2 B4 regression", m3.ID)
	}
	if beforeIDs[recovery.ID] {
		t.Errorf("BEFORE soft-delete: recovery row=%v leaked into pool — Phase 2 B3 regression", recovery.ID)
	}
	// m1 + m2 should be the only entries.
	if len(beforeMap[rm.ID]) != 2 {
		t.Errorf("BEFORE: pool size=%d, want 2 (m1, m2)", len(beforeMap[rm.ID]))
	}

	// Soft-delete the recovery row directly (no service surface in Phase 5).
	if err := db.Delete(recovery).Error; err != nil {
		t.Fatalf("soft-delete recovery: %v", err)
	}

	// AFTER soft-delete: m3 should re-enter the pool (un-excluded).
	afterMap, err := meterRepo.FindRecentByRoomIDs(ctx, []uuid.UUID{rm.ID}, 6)
	if err != nil {
		t.Fatalf("FindRecentByRoomIDs (after): %v", err)
	}
	afterIDs := idSet(afterMap[rm.ID])
	if !afterIDs[m3.ID] {
		t.Errorf("AFTER soft-delete: m3=%v still excluded — un-exclusion broke (Phase 2 src.deleted_at IS NULL regression)", m3.ID)
	}
	if afterIDs[recovery.ID] {
		t.Errorf("AFTER soft-delete: recovery row=%v leaked back in — soft-delete must remove it from the pool", recovery.ID)
	}
	if len(afterMap[rm.ID]) != 3 {
		t.Errorf("AFTER: pool size=%d, want 3 (m1, m2, m3)", len(afterMap[rm.ID]))
	}
	if !afterIDs[m1.ID] || !afterIDs[m2.ID] {
		t.Errorf("AFTER: m1 or m2 missing from pool (%v)", afterIDs)
	}
}

func idSet(rows []meterreading.MeterReading) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}
