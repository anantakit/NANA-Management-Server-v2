package meterreading

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// --- helpers ---

func date(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func intPtr(v int) *int { return &v }

var (
	roomA = uuid.New()
	roomB = uuid.New()
)

func makeLatest(roomID uuid.UUID, readingDate string, elecCurrent, waterCurrent int) *MeterReading {
	return &MeterReading{
		ID:                 uuid.New(),
		RoomID:             roomID,
		ReadingDate:        date(readingDate),
		ElectricityCurrent: elecCurrent,
		WaterCurrent:       waterCurrent,
	}
}

// --- ElectricityUsed / WaterUsed ---

func TestElectricityUsed(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 250}
	if got := m.ElectricityUsed(); got != 150 {
		t.Errorf("ElectricityUsed() = %d, want 150", got)
	}
}

func TestWaterUsed(t *testing.T) {
	m := MeterReading{WaterPrevious: 50, WaterCurrent: 80}
	if got := m.WaterUsed(); got != 30 {
		t.Errorf("WaterUsed() = %d, want 30", got)
	}
}

// --- NewReading ---

func TestNewReading_FirstReading_NilLatest(t *testing.T) {
	m, err := NewReading(roomA, date("2026-03-01"), 100, 50, nil, MeterReplacedFlags{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Errorf("first reading should have previous=0, got elec=%d water=%d", m.ElectricityPrevious, m.WaterPrevious)
	}
	if m.ElectricityCurrent != 100 || m.WaterCurrent != 50 {
		t.Errorf("current values mismatch")
	}
}

func TestNewReading_AutoPopulatePrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 100)
	m, err := NewReading(roomA, date("2026-03-01"), 350, 130, latest, MeterReplacedFlags{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 200 {
		t.Errorf("ElectricityPrevious = %d, want 200", m.ElectricityPrevious)
	}
	if m.WaterPrevious != 100 {
		t.Errorf("WaterPrevious = %d, want 100", m.WaterPrevious)
	}
}

func TestNewReading_MeterReplaced_PreviousZero(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 9999, 5000)
	m, err := NewReading(roomA, date("2026-03-01"), 50, 10, latest, MeterReplacedFlags{Water: true, Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Errorf("meter replaced should reset previous to 0, got elec=%d water=%d", m.ElectricityPrevious, m.WaterPrevious)
	}
}

func TestNewReading_WaterReplacedOnly(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 5000)
	m, err := NewReading(roomA, date("2026-03-01"), 250, 10, latest, MeterReplacedFlags{Water: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterPrevious != 0 {
		t.Errorf("WaterPrevious = %d, want 0", m.WaterPrevious)
	}
	if m.ElectricityPrevious != 200 {
		t.Errorf("ElectricityPrevious = %d, want 200 (unchanged)", m.ElectricityPrevious)
	}
}

func TestNewReading_ElecReplacedOnly(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 9999, 100)
	m, err := NewReading(roomA, date("2026-03-01"), 50, 120, latest, MeterReplacedFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 {
		t.Errorf("ElectricityPrevious = %d, want 0", m.ElectricityPrevious)
	}
	if m.WaterPrevious != 100 {
		t.Errorf("WaterPrevious = %d, want 100 (unchanged)", m.WaterPrevious)
	}
}

func TestNewReading_SameDate_OK(t *testing.T) {
	latest := makeLatest(roomA, "2026-03-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 120, latest, MeterReplacedFlags{})
	if err != nil {
		t.Fatalf("same date should be allowed, got: %v", err)
	}
}

func TestNewReading_ElecCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 150, 120, latest, MeterReplacedFlags{})
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_WaterCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 80, latest, MeterReplacedFlags{})
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_LatestRoomMismatch(t *testing.T) {
	latest := makeLatest(roomB, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 120, latest, MeterReplacedFlags{})
	if err != ErrLatestRoomMismatch {
		t.Errorf("expected ErrLatestRoomMismatch, got %v", err)
	}
}

func TestNewReading_DateBeforeLatest(t *testing.T) {
	latest := makeLatest(roomA, "2026-03-01", 200, 100)
	_, err := NewReading(roomA, date("2026-02-15"), 250, 120, latest, MeterReplacedFlags{})
	if err != ErrReadingDateBeforeLatest {
		t.Errorf("expected ErrReadingDateBeforeLatest, got %v", err)
	}
}

// --- CanUpdate ---

func TestCanUpdate_IsLatest(t *testing.T) {
	id := uuid.New()
	m := MeterReading{ID: id}
	if err := m.CanUpdate(id); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCanUpdate_NotLatest(t *testing.T) {
	m := MeterReading{ID: uuid.New()}
	if err := m.CanUpdate(uuid.New()); err != ErrOnlyLatestCanBeUpdated {
		t.Errorf("expected ErrOnlyLatestCanBeUpdated, got %v", err)
	}
}

// --- ApplyUpdate ---

var noReplace = MeterReplacedFlags{}

func TestApplyUpdate_PartialElectricity(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(250), nil, noReplace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 250 {
		t.Errorf("ElectricityCurrent = %d, want 250", m.ElectricityCurrent)
	}
	if m.WaterCurrent != 80 {
		t.Errorf("WaterCurrent should be unchanged, got %d", m.WaterCurrent)
	}
}

func TestApplyUpdate_PartialWater(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, intPtr(90), noReplace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterCurrent != 90 {
		t.Errorf("WaterCurrent = %d, want 90", m.WaterCurrent)
	}
}

func TestApplyUpdate_BothFields(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(300), intPtr(100), noReplace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 300 || m.WaterCurrent != 100 {
		t.Errorf("got elec=%d water=%d, want 300/100", m.ElectricityCurrent, m.WaterCurrent)
	}
}

func TestApplyUpdate_NilBoth_NoChange(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, nil, noReplace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 200 || m.WaterCurrent != 80 {
		t.Errorf("values should be unchanged")
	}
}

func TestApplyUpdate_ElecBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(50), nil, noReplace)
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestApplyUpdate_WaterBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, intPtr(30), noReplace)
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}

// --- ApplyUpdate with meter replaced ---

func TestApplyUpdate_WaterReplaced_ResetsPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 5000, WaterCurrent: 5100}
	err := m.ApplyUpdate(nil, intPtr(10), MeterReplacedFlags{Water: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterPrevious != 0 {
		t.Errorf("WaterPrevious = %d, want 0", m.WaterPrevious)
	}
	if m.WaterCurrent != 10 {
		t.Errorf("WaterCurrent = %d, want 10", m.WaterCurrent)
	}
	if m.ElectricityPrevious != 100 {
		t.Errorf("ElectricityPrevious should be unchanged, got %d", m.ElectricityPrevious)
	}
}

func TestApplyUpdate_ElecReplaced_ResetsPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 9000, ElectricityCurrent: 9500, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(20), nil, MeterReplacedFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 {
		t.Errorf("ElectricityPrevious = %d, want 0", m.ElectricityPrevious)
	}
	if m.ElectricityCurrent != 20 {
		t.Errorf("ElectricityCurrent = %d, want 20", m.ElectricityCurrent)
	}
	if m.WaterPrevious != 50 {
		t.Errorf("WaterPrevious should be unchanged, got %d", m.WaterPrevious)
	}
}

// --- IsAnomalousUsage ---

func TestIsAnomalousUsage_BaselineZero_BelowThreshold(t *testing.T) {
	if IsAnomalousUsage(100, 0) {
		t.Error("usage=100, baseline=0 should NOT be anomaly (need >100)")
	}
}

func TestIsAnomalousUsage_BaselineZero_AboveThreshold(t *testing.T) {
	if !IsAnomalousUsage(101, 0) {
		t.Error("usage=101, baseline=0 should be anomaly")
	}
}

func TestIsAnomalousUsage_Normal_NotExceeding(t *testing.T) {
	// baseline=100, threshold = 100*3/2 = 150 → usage must be >150
	if IsAnomalousUsage(150, 100) {
		t.Error("usage=150, baseline=100 should NOT be anomaly (need >150)")
	}
}

func TestIsAnomalousUsage_Normal_Exceeding(t *testing.T) {
	if !IsAnomalousUsage(151, 100) {
		t.Error("usage=151, baseline=100 should be anomaly")
	}
}

func TestIsAnomalousUsage_SmallBaseline_MinThresholdGuard(t *testing.T) {
	// baseline=10, threshold = 10*3/2 = 15 → usage=16 > 15 BUT 16 <= 50 → NOT anomaly
	if IsAnomalousUsage(16, 10) {
		t.Error("usage=16, baseline=10 should NOT be anomaly (below min threshold 50)")
	}
}

func TestIsAnomalousUsage_SmallBaseline_AboveMinThreshold(t *testing.T) {
	// baseline=10, usage=51 > 15 AND > 50 → anomaly
	if !IsAnomalousUsage(51, 10) {
		t.Error("usage=51, baseline=10 should be anomaly")
	}
}

// --- ComputeAnomalies ---

func TestComputeAnomalies_BothHaveData(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 1000, ElectricityCurrent: 1200, // used = 200
		WaterPrevious: 100, WaterCurrent: 300, // used = 200
	}
	bl := RoomBaseline{
		ElectricityBaseline:      100, // threshold = 150, usage 200 > 150 && > 50 → anomaly
		WaterBaseline:            100, // same
		ElectricityHasEnoughData: true,
		WaterHasEnoughData:       true,
	}
	m.ComputeAnomalies(bl)
	if !m.IsAnomalyElectricity {
		t.Error("expected electricity anomaly")
	}
	if !m.IsAnomalyWater {
		t.Error("expected water anomaly")
	}
}

func TestComputeAnomalies_ElecHasData_WaterDoesNot(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 1000, ElectricityCurrent: 1200, // used = 200
		WaterPrevious: 100, WaterCurrent: 300, // used = 200
	}
	bl := RoomBaseline{
		ElectricityBaseline:      100,
		WaterBaseline:            100,
		ElectricityHasEnoughData: true,
		WaterHasEnoughData:       false, // not enough data
	}
	m.ComputeAnomalies(bl)
	if !m.IsAnomalyElectricity {
		t.Error("expected electricity anomaly")
	}
	if m.IsAnomalyWater {
		t.Error("water should NOT be flagged when not enough data")
	}
}

func TestComputeAnomalies_NeitherHasData(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 1000, ElectricityCurrent: 9999,
		WaterPrevious: 100, WaterCurrent: 9999,
	}
	bl := RoomBaseline{
		ElectricityHasEnoughData: false,
		WaterHasEnoughData:       false,
	}
	m.ComputeAnomalies(bl)
	if m.IsAnomalyElectricity || m.IsAnomalyWater {
		t.Error("neither should be flagged when not enough data")
	}
}

func TestComputeAnomalies_NormalUsage_NoAnomaly(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 1000, ElectricityCurrent: 1100, // used = 100
		WaterPrevious: 100, WaterCurrent: 110, // used = 10
	}
	bl := RoomBaseline{
		ElectricityBaseline:      100, // threshold = 150, usage 100 < 150 → OK
		WaterBaseline:            100, // threshold = 150, usage 10 < 150 → OK
		ElectricityHasEnoughData: true,
		WaterHasEnoughData:       true,
	}
	m.ComputeAnomalies(bl)
	if m.IsAnomalyElectricity {
		t.Error("electricity should not be anomaly")
	}
	if m.IsAnomalyWater {
		t.Error("water should not be anomaly")
	}
}
