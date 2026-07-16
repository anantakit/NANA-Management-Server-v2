package meterreading

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Epic B Model B — move-out over-record ("เดือนก่อนจดเกิน").
//
// One physical observation (the exit reading) feeds two events: a
// READING_RECOVERY re-anchor (created first) + the EXIT reading. The re-anchor
// zeroes the corrected utility's exit usage; the settlement resolver (unchanged)
// refunds recorded − observed. See
// EPIC_B_SETTLEMENT_RECOVERY_MODELB_ONTOLOGY_SCOPE.md.

func TestNewMoveOutOverRecordAnchor_ElectricityOnly(t *testing.T) {
	// Source over-recorded electricity (current 1500); water normal (220).
	latest := makeLatest(testRoomID, "2026-06", 1500, 220)

	anchor, err := NewMoveOutOverRecordAnchor(testRoomID, latest, 1240, 228,
		MeterOverRecordFlags{Electricity: true}, "2026-07", "note")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Electricity re-anchored to the exit observation (0 usage at exit); recorded
	// = the prior over-recorded value (drives the source-priced refund).
	if anchor.ElectricityPrevious != 1240 || anchor.ElectricityCurrent != 1240 {
		t.Errorf("elec prev/current = %d/%d, want 1240/1240", anchor.ElectricityPrevious, anchor.ElectricityCurrent)
	}
	if anchor.ElectricityRecorded == nil || *anchor.ElectricityRecorded != 1500 {
		t.Errorf("elec recorded = %v, want 1500", anchor.ElectricityRecorded)
	}
	// Water carried at the prior baseline (not over-recorded → bills normally at
	// exit), no recorded → no water refund.
	if anchor.WaterPrevious != 220 || anchor.WaterCurrent != 220 {
		t.Errorf("water prev/current = %d/%d, want 220/220 (carried baseline)", anchor.WaterPrevious, anchor.WaterCurrent)
	}
	if anchor.WaterRecorded != nil {
		t.Errorf("water recorded = %v, want nil", anchor.WaterRecorded)
	}
	if anchor.AnchorReason == nil || *anchor.AnchorReason != AnchorReasonReadingRecovery {
		t.Error("anchor_reason must be READING_RECOVERY (a distinct event, not merged into EXIT)")
	}
	if anchor.RecoverySourceReadingID == nil || *anchor.RecoverySourceReadingID != latest.ID {
		t.Error("source must be the over-recorded latest reading")
	}
}

func TestNewMoveOutOverRecordAnchor_BothUtilities(t *testing.T) {
	latest := makeLatest(testRoomID, "2026-06", 1500, 300)
	anchor, err := NewMoveOutOverRecordAnchor(testRoomID, latest, 1240, 228,
		MeterOverRecordFlags{Electricity: true, Water: true}, "2026-07", "note")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if anchor.ElectricityCurrent != 1240 || anchor.ElectricityRecorded == nil || *anchor.ElectricityRecorded != 1500 {
		t.Errorf("elec re-anchor wrong: current=%d recorded=%v", anchor.ElectricityCurrent, anchor.ElectricityRecorded)
	}
	if anchor.WaterCurrent != 228 || anchor.WaterRecorded == nil || *anchor.WaterRecorded != 300 {
		t.Errorf("water re-anchor wrong: current=%d recorded=%v", anchor.WaterCurrent, anchor.WaterRecorded)
	}
}

func TestCreateExitForMoveOut_OverRecord_AnchorFirstThenZeroUsageExit(t *testing.T) {
	source := makeLatest(testRoomID, "2026-06", 1500, 220) // over-recorded elec
	var created []*MeterReading
	repo := &mockMeterRepo{
		findLatestByRoomIDFn: func(_ context.Context, _ uuid.UUID) (*MeterReading, error) { return source, nil },
		createFn:             func(_ context.Context, r *MeterReading) error { created = append(created, r); return nil },
	}
	svc := newTestService(repo, &mockMoveOutChecker{})
	date, _ := time.Parse("2006-01-02", "2026-07-15")

	err := svc.CreateExitForMoveOut(context.Background(), testRoomID, date, 1240, 228,
		false, false, false, false, /* over-record: */ true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("expected 2 rows (anchor + exit), got %d", len(created))
	}
	anchor, exit := created[0], created[1]

	// Ordering is load-bearing: the anchor is created FIRST so the exit picks up
	// previous = re-anchored current (§0.2).
	if anchor.AnchorReason == nil || *anchor.AnchorReason != AnchorReasonReadingRecovery {
		t.Fatal("first create must be the READING_RECOVERY anchor")
	}
	if anchor.ElectricityCurrent != 1240 || anchor.ElectricityRecorded == nil || *anchor.ElectricityRecorded != 1500 {
		t.Errorf("anchor elec current=%d recorded=%v, want 1240 recorded 1500", anchor.ElectricityCurrent, anchor.ElectricityRecorded)
	}
	if exit.ReadingType != ReadingTypeExit {
		t.Fatal("second create must be the EXIT reading")
	}
	// Exit: 0 usage on the corrected utility, normal usage on the other.
	if exit.ElectricityUsed() != 0 {
		t.Errorf("exit elec usage = %d, want 0 (re-anchored)", exit.ElectricityUsed())
	}
	if exit.WaterUsed() != 8 {
		t.Errorf("exit water usage = %d, want 8 (220→228, carried baseline)", exit.WaterUsed())
	}
}

func TestValidateOverRecord_Guards(t *testing.T) {
	latest := makeLatest(testRoomID, "2026-06", 1500, 220)

	if err := validateOverRecord(latest, 1240, 228, MeterOverRecordFlags{Electricity: true}, MeterReplacedFlags{}, MeterRolloverFlags{}); err != nil {
		t.Errorf("valid below-previous over-record rejected: %v", err)
	}
	if err := validateOverRecord(latest, 1600, 228, MeterOverRecordFlags{Electricity: true}, MeterReplacedFlags{}, MeterRolloverFlags{}); err != ErrOverRecordNotBelowPrevious {
		t.Errorf("not-below-previous must reject with ErrOverRecordNotBelowPrevious, got %v", err)
	}
	if err := validateOverRecord(latest, 1240, 228, MeterOverRecordFlags{Electricity: true}, MeterReplacedFlags{}, MeterRolloverFlags{Electricity: true}); err != ErrOverRecordConflictsWithHardware {
		t.Errorf("over-record + rollover (same utility) must reject, got %v", err)
	}
	if err := validateOverRecord(latest, 1240, 228, MeterOverRecordFlags{Electricity: true}, MeterReplacedFlags{Electricity: true}, MeterRolloverFlags{}); err != ErrOverRecordConflictsWithHardware {
		t.Errorf("over-record + replaced (same utility) must reject, got %v", err)
	}
	if err := validateOverRecord(nil, 1240, 228, MeterOverRecordFlags{Electricity: true}, MeterReplacedFlags{}, MeterRolloverFlags{}); err != ErrOverRecordNotBelowPrevious {
		t.Errorf("nil latest must reject, got %v", err)
	}
}
