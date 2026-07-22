package meterreading

import (
	"testing"

	"github.com/google/uuid"
)

// intp returns a pointer to v (test helper).
func intp(v int) *int { return &v }

// seedLatest builds a prior reading carrying the given per-utility currents,
// used as `latest` for NewReplacementAnchor (its currents become the frozen
// old_previous of a replaced utility).
func seedLatest(elecCur, waterCur int) *MeterReading {
	return &MeterReading{
		ID:                 uuid.New(),
		RoomID:             uuid.New(),
		ReadingType:        ReadingTypeMonthly,
		ElectricityCurrent: elecCur,
		WaterCurrent:       waterCur,
	}
}

// D1 — both utilities replaced: anchor shape is a re-anchor to new_initial with
// the frozen old-meter boundary + provenance FK, PHYSICAL_REPLACEMENT + note.
func TestNewReplacementAnchor_BothUtilities_Shape(t *testing.T) {
	latest := seedLatest(1000, 500)
	roomID := latest.RoomID
	m, err := NewReplacementAnchor(roomID, latest, "2026-04", "หน้าปัดอ่านไม่ชัด",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(560), NewInitial: 20})
	if err != nil {
		t.Fatalf("NewReplacementAnchor: %v", err)
	}
	if !m.IsPhysicalReplacement() {
		t.Fatal("want PHYSICAL_REPLACEMENT anchor_reason")
	}
	// Re-anchor: previous==current==new_initial → forward baseline; standalone usage 0.
	if m.ElectricityPrevious != 350 || m.ElectricityCurrent != 350 {
		t.Errorf("elec prev/cur = %d/%d, want 350/350 (new_initial)", m.ElectricityPrevious, m.ElectricityCurrent)
	}
	if m.WaterPrevious != 20 || m.WaterCurrent != 20 {
		t.Errorf("water prev/cur = %d/%d, want 20/20", m.WaterPrevious, m.WaterCurrent)
	}
	if m.ElectricityUsed() != 0 || m.WaterUsed() != 0 {
		t.Errorf("anchor standalone usage must be 0, got elec=%d water=%d", m.ElectricityUsed(), m.WaterUsed())
	}
	// Frozen old boundary snapshotted from `latest`.
	if m.ElectricityOldPrevious == nil || *m.ElectricityOldPrevious != 1000 || m.ElectricityOldFinal == nil || *m.ElectricityOldFinal != 1120 {
		t.Errorf("elec old boundary = %v→%v, want 1000→1120", m.ElectricityOldPrevious, m.ElectricityOldFinal)
	}
	if m.WaterOldPrevious == nil || *m.WaterOldPrevious != 500 || m.WaterOldFinal == nil || *m.WaterOldFinal != 560 {
		t.Errorf("water old boundary = %v→%v, want 500→560", m.WaterOldPrevious, m.WaterOldFinal)
	}
	if m.PredecessorReadingID == nil || *m.PredecessorReadingID != latest.ID {
		t.Errorf("predecessor FK = %v, want %v", m.PredecessorReadingID, latest.ID)
	}
	if m.AnchorNote == nil || *m.AnchorNote != "หน้าปัดอ่านไม่ชัด" {
		t.Errorf("note not carried: %v", m.AnchorNote)
	}
	if m.ElectricityReplacementTail() != 120 || m.WaterReplacementTail() != 60 {
		t.Errorf("tails = %d/%d, want 120/60", m.ElectricityReplacementTail(), m.WaterReplacementTail())
	}
}

// D2 — electricity replaced only: water is untouched (baseline carried forward,
// no water tail).
func TestNewReplacementAnchor_ElecOnly_WaterUntouched(t *testing.T) {
	latest := seedLatest(1000, 500)
	m, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "มิเตอร์ไฟเสีย",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("NewReplacementAnchor: %v", err)
	}
	// Water carried forward unchanged; no water old boundary.
	if m.WaterPrevious != 500 || m.WaterCurrent != 500 {
		t.Errorf("water prev/cur = %d/%d, want 500/500 (carried)", m.WaterPrevious, m.WaterCurrent)
	}
	if m.WaterOldFinal != nil || m.WaterOldPrevious != nil {
		t.Errorf("water old boundary must be nil (not replaced), got %v/%v", m.WaterOldPrevious, m.WaterOldFinal)
	}
	if m.WaterReplacementTail() != 0 {
		t.Errorf("water tail = %d, want 0", m.WaterReplacementTail())
	}
	if m.ElectricityReplacementTail() != 120 {
		t.Errorf("elec tail = %d, want 120", m.ElectricityReplacementTail())
	}
}

// D3 — canonical period usage, both segments measurable (owner Example 2 → 200).
func TestCanonicalPeriodUsage_ReplacementBothSegments(t *testing.T) {
	latest := seedLatest(1000, 0)
	anchor, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "เปลี่ยนมิเตอร์",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	// Later ordinary reading on the new meter: previous inherits new_initial (350).
	consumption := &MeterReading{ReadingType: ReadingTypeMonthly, ElectricityPrevious: 350, ElectricityCurrent: 430}
	elec, _ := CanonicalPeriodUsage(consumption, []*MeterReading{anchor})
	if elec.Total != 200 {
		t.Errorf("elec total = %d, want 200 ((1120-1000)+(430-350))", elec.Total)
	}
	if len(elec.Segments) != 2 {
		t.Fatalf("want 2 segments (old tail + reading), got %d: %+v", len(elec.Segments), elec.Segments)
	}
	// Chronological: old tail first, then the reading.
	if s := elec.Segments[0]; s.Kind != UsageSegmentReplacementOldTail || s.Previous != 1000 || s.Current != 1120 || s.Usage != 120 {
		t.Errorf("segment[0] = %+v, want old tail 1000→1120 = 120", s)
	}
	if s := elec.Segments[1]; s.Kind != UsageSegmentReading || s.Previous != 350 || s.Current != 430 || s.Usage != 80 {
		t.Errorf("segment[1] = %+v, want reading 350→430 = 80", s)
	}
}

// D4 — old meter stuck: old_final == old_previous → tail 0 → measured-only (80).
func TestCanonicalPeriodUsage_StuckMeter_MeasuredOnly(t *testing.T) {
	latest := seedLatest(1000, 0)
	anchor, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "มิเตอร์ค้าง",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1000), NewInitial: 350}, // stuck: final == previous
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	consumption := &MeterReading{ReadingType: ReadingTypeMonthly, ElectricityPrevious: 350, ElectricityCurrent: 430}
	elec, _ := CanonicalPeriodUsage(consumption, []*MeterReading{anchor})
	if elec.Total != 80 {
		t.Errorf("elec total = %d, want 80 (stuck tail 0 + 80)", elec.Total)
	}
	if elec.Segments[0].Usage != 0 {
		t.Errorf("stuck tail usage = %d, want 0", elec.Segments[0].Usage)
	}
}

// D5 — old meter unreadable (OldFinal nil): no tail segment, new segment only. No guess.
func TestCanonicalPeriodUsage_UnreadableOldMeter_NoTail(t *testing.T) {
	latest := seedLatest(1000, 0)
	anchor, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "อ่านมิเตอร์เก่าไม่ได้",
		ReplacementUtilityInput{Replaced: true, OldFinal: nil, NewInitial: 350}, // unreadable
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if anchor.ElectricityOldFinal != nil {
		t.Errorf("unreadable → old_final must be nil, got %v", anchor.ElectricityOldFinal)
	}
	consumption := &MeterReading{ReadingType: ReadingTypeMonthly, ElectricityPrevious: 350, ElectricityCurrent: 430}
	elec, _ := CanonicalPeriodUsage(consumption, []*MeterReading{anchor})
	if elec.Total != 80 || len(elec.Segments) != 1 || elec.Segments[0].Kind != UsageSegmentReading {
		t.Errorf("want total 80 with 1 READING segment, got total=%d segs=%+v", elec.Total, elec.Segments)
	}
}

// D6 — two replacements in one period (owner Q2 → 230), order-independent, each
// tail frozen against its own predecessor (2nd old_previous = 350, not 1000).
func TestCanonicalPeriodUsage_TwoReplacements(t *testing.T) {
	// A: latest current 1000.
	latestA := seedLatest(1000, 0)
	// Replace 1 (A→B): old_final 1120, new_initial 350.
	anchor1, err := NewReplacementAnchor(latestA.RoomID, latestA, "2026-04", "เปลี่ยนครั้งที่ 1",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("anchor1: %v", err)
	}
	// Replace 2 (B→C): latest is now anchor1 (current 350) → frozen old_previous MUST be 350.
	anchor2, err := NewReplacementAnchor(latestA.RoomID, anchor1, "2026-04", "เปลี่ยนครั้งที่ 2",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(410), NewInitial: 20},
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("anchor2: %v", err)
	}
	if anchor2.ElectricityOldPrevious == nil || *anchor2.ElectricityOldPrevious != 350 {
		t.Fatalf("replace 2 old_previous = %v, want 350 (B initial, NOT 1000)", anchor2.ElectricityOldPrevious)
	}
	// Final reading on C: previous inherits anchor2 current (20).
	consumption := &MeterReading{ReadingType: ReadingTypeMonthly, ElectricityPrevious: 20, ElectricityCurrent: 70}
	elec, _ := CanonicalPeriodUsage(consumption, []*MeterReading{anchor1, anchor2})
	if elec.Total != 230 {
		t.Errorf("elec total = %d, want 230 (120+60+50)", elec.Total)
	}
	if len(elec.Segments) != 3 {
		t.Fatalf("want 3 segments, got %d", len(elec.Segments))
	}
	wantUsage := []int{120, 60, 50}
	for i, w := range wantUsage {
		if elec.Segments[i].Usage != w {
			t.Errorf("segment[%d] usage = %d, want %d", i, elec.Segments[i].Usage, w)
		}
	}
	// Order-independence of the SUM: reversing the replacement slice keeps total 230.
	elecRev, _ := CanonicalPeriodUsage(consumption, []*MeterReading{anchor2, anchor1})
	if elecRev.Total != 230 {
		t.Errorf("reversed total = %d, want 230 (sum must be order-independent)", elecRev.Total)
	}
}

// Backward-compat: no replacement events → identical to consumption.Used(), one READING segment.
func TestCanonicalPeriodUsage_NoReplacement_UnchangedBehaviour(t *testing.T) {
	consumption := &MeterReading{ReadingType: ReadingTypeMonthly, ElectricityPrevious: 1000, ElectricityCurrent: 1200, WaterPrevious: 50, WaterCurrent: 90}
	elec, water := CanonicalPeriodUsage(consumption, nil)
	if elec.Total != 200 || len(elec.Segments) != 1 || elec.Segments[0].Kind != UsageSegmentReading {
		t.Errorf("elec: want total 200 with 1 READING segment, got %d / %+v", elec.Total, elec.Segments)
	}
	if water.Total != 40 || len(water.Segments) != 1 {
		t.Errorf("water: want total 40 with 1 segment, got %d / %+v", water.Total, water.Segments)
	}
}

// Gate 1 — per-utility old_previous isolation: each utility's frozen old_previous
// is snapshotted from THAT utility's latest current, and an untouched utility is
// carried forward unbroken. A later replacement of the untouched utility must
// snapshot ITS carried baseline, never the other utility's value.
func TestNewReplacementAnchor_PerUtility_OldPreviousIsolation(t *testing.T) {
	latest := seedLatest(1000, 500) // elec 1000, water 500
	// Water-only replacement: elec carried (1000/1000, no boundary); water re-anchored.
	waterOnly, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "เปลี่ยนมิเตอร์น้ำ",
		ReplacementUtilityInput{Replaced: false},
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(560), NewInitial: 20})
	if err != nil {
		t.Fatalf("water-only: %v", err)
	}
	if waterOnly.ElectricityPrevious != 1000 || waterOnly.ElectricityCurrent != 1000 || waterOnly.ElectricityOldFinal != nil {
		t.Errorf("elec must carry unbroken (1000/1000, no boundary), got %d/%d final=%v",
			waterOnly.ElectricityPrevious, waterOnly.ElectricityCurrent, waterOnly.ElectricityOldFinal)
	}
	if waterOnly.WaterOldPrevious == nil || *waterOnly.WaterOldPrevious != 500 {
		t.Errorf("water old_previous = %v, want 500 (water's latest, NOT elec's 1000)", waterOnly.WaterOldPrevious)
	}
	// Now replace electricity using waterOnly as latest: elec old_previous MUST be the
	// carried 1000 (elec's own baseline threaded through the water anchor), NOT water's.
	elecNext, err := NewReplacementAnchor(latest.RoomID, waterOnly, "2026-04", "เปลี่ยนมิเตอร์ไฟ",
		ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		ReplacementUtilityInput{Replaced: false})
	if err != nil {
		t.Fatalf("elec-next: %v", err)
	}
	if elecNext.ElectricityOldPrevious == nil || *elecNext.ElectricityOldPrevious != 1000 {
		t.Errorf("elec old_previous = %v, want 1000 (carried elec baseline, NOT water)", elecNext.ElectricityOldPrevious)
	}
	// Water now carried at 20 (its new_initial from the water anchor).
	if elecNext.WaterPrevious != 20 || elecNext.WaterCurrent != 20 {
		t.Errorf("water carried from prior anchor = %d/%d, want 20/20", elecNext.WaterPrevious, elecNext.WaterCurrent)
	}
}

// Gate 2 — AffectedReplacementUtilities reflects ONLY the utilities the event
// physically replaced (the explicit flags), never mere anchor presence.
func TestAffectedReplacementUtilities_PerUtility(t *testing.T) {
	latest := seedLatest(1000, 500)
	waterOnly, _ := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "น้ำ",
		ReplacementUtilityInput{Replaced: false},
		ReplacementUtilityInput{Replaced: true, NewInitial: 20})
	if got := waterOnly.AffectedReplacementUtilities(); len(got) != 1 || got[0] != "WATER" {
		t.Errorf("water-only → %v, want [WATER] only (flag-driven, not anchor presence)", got)
	}
	both, _ := NewReplacementAnchor(latest.RoomID, latest, "2026-04", "both",
		ReplacementUtilityInput{Replaced: true, NewInitial: 350},
		ReplacementUtilityInput{Replaced: true, NewInitial: 20})
	if got := both.AffectedReplacementUtilities(); len(got) != 2 {
		t.Errorf("both → %v, want 2 utilities", got)
	}
	// A recovery anchor (also an anchor row, but NOT a replacement) → nil.
	rr := AnchorReasonReadingRecovery
	recovery := &MeterReading{ReadingType: ReadingTypeMonthly, AnchorReason: &rr}
	if got := recovery.AffectedReplacementUtilities(); got != nil {
		t.Errorf("recovery anchor → %v, want nil (not a replacement)", got)
	}
}

// D7 + validation: note is required (narrative only); guards reject bad input.
func TestNewReplacementAnchor_Validation(t *testing.T) {
	latest := seedLatest(1000, 500)
	cases := []struct {
		name    string
		note    string
		elec    ReplacementUtilityInput
		water   ReplacementUtilityInput
		wantErr error
	}{
		{"no utility replaced", "n", ReplacementUtilityInput{}, ReplacementUtilityInput{}, ErrReplacementNoUtility},
		{"missing note", "", ReplacementUtilityInput{Replaced: true, NewInitial: 350}, ReplacementUtilityInput{}, ErrAnchorNoteRequired},
		{"old_final below previous", "n", ReplacementUtilityInput{Replaced: true, OldFinal: intp(900), NewInitial: 350}, ReplacementUtilityInput{}, ErrReplacementOldFinalBelowPrevious},
		{"negative new_initial", "n", ReplacementUtilityInput{Replaced: true, NewInitial: -1}, ReplacementUtilityInput{}, ErrReplacementNewInitialNegative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewReplacementAnchor(latest.RoomID, latest, "2026-04", tc.note, tc.elec, tc.water)
			if err != tc.wantErr {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
