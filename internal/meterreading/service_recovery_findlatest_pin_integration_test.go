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

// TestRecovery_FindLatestByRoomIDReturnsMostRecentByDate is a regression
// pin for the Reading Recovery lineage-vs-analytics-split guard.
//
//	DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
//	GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
//	DESIGN:   /Users/anantakit/.claude/plans/mutable-swimming-firefly.md (Item 3)
//
// Phase 0 assertions (lineage chain pin):
//
//	Two consecutive MONTHLY readings on the same room. FindLatestByRoomID
//	returns the row with the later billing month, and the lineage chain
//	is consistent — Month N's previous equals Month N-1's current via
//	populatePrevious.
//
// Phase 1 strengthening (shipped this PR — MeterReading.AnchorReason field
// landed in commit alongside this assertion):
//
//	A third reading hand-seeded via meterRepo.Create with
//	anchor_reason=READING_RECOVERY set directly (bypassing the service
//	surface, which is Phase 5's deliverable). Assert FindLatestByRoomID
//	STILL returns it AND prev=curr invariant survives persistence
//	roundtrip. THIS IS THE LOCKED GUARD IN TEST FORM — if a future Phase
//	2 PR applies the analytics-pool exclusion to FindLatestByRoomID by
//	reflex, this fails immediately.
//
// What this pin guards against:
//
//   - Phase 2 introducing the analytics-pool exclusion. The locked guard
//     restricts the anchor_reason / recovery_source_reading_id exclusion
//     to the analytics baseline pool query (FindRecentByRoomIDs +
//     computeBaseline). A developer who applies the same filter to
//     FindLatestByRoomID by reflex breaks next-month inheritance for
//     every room that ever has a recovery row — Assert 3 fails immediately.
//   - Phase 5 (or any subsequent path that writes recovery rows)
//     violating the prev=curr invariant. Assert 4 catches a
//     persistence-layer roundtrip that drops the equality.
//   - Any future change that breaks the prev=curr inheritance through
//     populatePrevious. Asserts 1+2 catch that before it ships.
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
	// Generic latest-by-date assertion (Phase 0). Assert 3 below
	// strengthens with the anchor row.
	latest, err := meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID: %v", err)
	}
	if latest.ID != n.ID {
		t.Errorf("FindLatestByRoomID = %v, want Month N %v (most-recent by billing_month)", latest.ID, n.ID)
	}

	// ── Month N+1 (2026-05): READING_RECOVERY anchor, hand-seeded ──
	// Phase 5 will land the service-level recovery commit. Phase 1
	// hand-seeds via the repository so this PR can encode the locked
	// guard (feedback_recovery_lineage_vs_analytics_split.md): lineage
	// truth MUST see recovery rows regardless of any analytics-pool
	// exclusion shipping in Phase 2.
	//
	// Recovery semantics per doctrine: previous = current (usage=0),
	// anchor_reason = READING_RECOVERY, anchor_note required,
	// recovery_source_reading_id points at the suspect source (Month N).
	recoveryMonth := "2026-05"
	recoveryNote := "ค้นพบว่าเดือนเมษายนจดมิเตอร์ผิด — re-anchor"
	recoveryReason := meterreading.AnchorReasonReadingRecovery
	recovery := &meterreading.MeterReading{
		RoomID:                  rm.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &recoveryMonth,
		ElectricityPrevious:     200, // = current; recovery has usage=0
		ElectricityCurrent:      200,
		WaterPrevious:           55,
		WaterCurrent:            55,
		AnchorReason:            &recoveryReason,
		AnchorNote:              &recoveryNote,
		RecoverySourceReadingID: &n.ID,
	}
	if err := meterRepo.Create(ctx, recovery); err != nil {
		t.Fatalf("hand-seed recovery row: %v", err)
	}

	// ── Assert 3: FindLatestByRoomID returns the recovery row ──
	// THIS IS THE LOCKED GUARD IN TEST FORM. If a future PR applies the
	// analytics-pool exclusion to FindLatestByRoomID by reflex, this
	// fails immediately at PR time.
	latest, err = meterRepo.FindLatestByRoomID(ctx, rm.ID)
	if err != nil {
		t.Fatalf("FindLatestByRoomID after recovery: %v", err)
	}
	if latest.ID != recovery.ID {
		t.Errorf("FindLatestByRoomID = %v, want recovery %v — lineage MUST see recovery rows",
			latest.ID, recovery.ID)
	}

	// ── Assert 4: recovery row's prev=curr invariant survived persistence ──
	// Phase 5's service surface enforces this at commit; Phase 1 only
	// pins the persistence-layer roundtrip honesty.
	if latest.ElectricityPrevious != latest.ElectricityCurrent {
		t.Errorf("recovery elec prev/curr = %d/%d, want equal (usage=0 invariant)",
			latest.ElectricityPrevious, latest.ElectricityCurrent)
	}
	if latest.WaterPrevious != latest.WaterCurrent {
		t.Errorf("recovery water prev/curr = %d/%d, want equal",
			latest.WaterPrevious, latest.WaterCurrent)
	}

	// Roundtrip the anchor fields themselves, so a GORM tag drift on any
	// of the three new columns surfaces here at PR time.
	if latest.AnchorReason == nil || *latest.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("recovery anchor_reason roundtrip = %v, want READING_RECOVERY", latest.AnchorReason)
	}
	if latest.AnchorNote == nil || *latest.AnchorNote != recoveryNote {
		t.Errorf("recovery anchor_note roundtrip mismatch")
	}
	if latest.RecoverySourceReadingID == nil || *latest.RecoverySourceReadingID != n.ID {
		t.Errorf("recovery_source_reading_id roundtrip = %v, want Month N %v",
			latest.RecoverySourceReadingID, n.ID)
	}
}
