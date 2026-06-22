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

// TestRecovery_FindLatestByRoomIDReturnsMostRecentByDate is a Phase 0
// regression pin for the Reading Recovery lineage-vs-analytics-split guard.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//
// Phase 0 assertion (green today):
//
//	Two consecutive MONTHLY readings on the same room. FindLatestByRoomID
//	returns the row with the later billing month, and the lineage chain
//	is consistent — Month N's previous equals Month N-1's current via
//	populatePrevious.
//
// Phase 2 strengthening (added when MeterReading.AnchorReason field lands):
//
//	A third reading hand-seeded directly with anchor_reason=READING_RECOVERY
//	(bypassing the service surface, which is Phase 5). Assert
//	FindLatestByRoomID STILL returns it. Encodes the locked guard
//	("lineage truth: FindLatestByRoomID MUST see recovery rows") in test
//	form.
//
// What this pin guards against:
//
//   - Phase 2 introducing the analytics-pool exclusion. The locked guard
//     restricts the anchor_reason / recovery_source_reading_id exclusion
//     to the analytics baseline pool query (FindRecentByRoomIDs +
//     computeBaseline). A developer who applies the same filter to
//     FindLatestByRoomID by reflex breaks next-month inheritance for
//     every room that ever has a recovery row — the strengthened
//     assertion fails immediately.
//   - Any future change that breaks the prev=curr inheritance through
//     populatePrevious. Today's lineage chain assertion catches that
//     before it ships.
func TestRecovery_FindLatestByRoomIDReturnsMostRecentByDate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	// ── Scenario graph ──
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "X-201")
	tn := fixtures.SeedTenant(t, db)
	_ = fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 4)

	// ── Wire meter service through real repos ──
	txMgr := database.NewTxManager(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	contractRepo := contract.NewContractRepository(db)
	roomRepo := room.NewRoomRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	meterSvc := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txMgr)

	// ── Month N-1 (2026-03): first MONTHLY — previous defaults to 0 ──
	nMinusOne, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-03",
		ElectricityCurrent: 100,
		WaterCurrent:       30,
	})
	if err != nil {
		t.Fatalf("create Month N-1: %v", err)
	}

	// ── Month N (2026-04): subsequent MONTHLY — previous inherits Month N-1 curr ──
	n, err := meterSvc.Create(ctx, apt.ID, meterreading.CreateRequest{
		RoomID:             rm.ID.String(),
		BillingMonth:       "2026-04",
		ElectricityCurrent: 150,
		WaterCurrent:       40,
	})
	if err != nil {
		t.Fatalf("create Month N: %v", err)
	}

	// ── Assert 1: lineage chain consistency via populatePrevious ──
	// Month N's previous MUST = Month N-1's current. This is the contract
	// populatePrevious() pins. Phase 2 strengthening will add an
	// anchor_reason recovery row whose `previous` MUST also equal the
	// prior reading's `current` (the recovery row is the new lineage
	// anchor). Pin the inheritance shape today so the Phase 2 PR can
	// extend with confidence.
	if n.ElectricityPrevious != nMinusOne.ElectricityCurrent {
		t.Errorf("Month N elec previous = %d, want %d (Month N-1 curr — lineage chain via populatePrevious)",
			n.ElectricityPrevious, nMinusOne.ElectricityCurrent)
	}
	if n.WaterPrevious != nMinusOne.WaterCurrent {
		t.Errorf("Month N water previous = %d, want %d (Month N-1 curr)",
			n.WaterPrevious, nMinusOne.WaterCurrent)
	}

	// ── Assert 2: FindLatestByRoomID returns the most-recent row ──
	// Today this is a generic latest-by-date assertion. Phase 2's
	// strengthening hand-seeds a third reading with anchor_reason set
	// directly via raw INSERT (recovery service surface is Phase 5).
	// That row MUST be returned by FindLatestByRoomID — the locked guard
	// forbids the analytics-pool exclusion from leaking into lineage.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != n.ID {
		t.Errorf("FindLatestByRoomID = %v, want Month N %v (most-recent by billing_month)", latest.ID, n.ID)
	}
}
