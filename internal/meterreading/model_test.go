package meterreading

import (
	"testing"

	"github.com/google/uuid"
)

// --- helpers ---

func intPtr(v int) *int { return &v }

var (
	roomA = uuid.New()
	roomB = uuid.New()
)

func makeLatest(roomID uuid.UUID, billingMonth string, elecCurrent, waterCurrent int) *MeterReading {
	return &MeterReading{
		ID:                 uuid.New(),
		RoomID:             roomID,
		BillingMonth:       billingMonth,
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
	m, err := NewReading(roomA, "2026-03", 100, 50, nil, MeterReplacedFlags{}, noRollover)
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
	latest := makeLatest(roomA, "2026-02", 200, 100)
	m, err := NewReading(roomA, "2026-03", 350, 130, latest, MeterReplacedFlags{}, noRollover)
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
	latest := makeLatest(roomA, "2026-02", 9999, 5000)
	m, err := NewReading(roomA, "2026-03", 50, 10, latest, MeterReplacedFlags{Water: true, Electricity: true}, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Errorf("meter replaced should reset previous to 0, got elec=%d water=%d", m.ElectricityPrevious, m.WaterPrevious)
	}
}

func TestNewReading_WaterReplacedOnly(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 5000)
	m, err := NewReading(roomA, "2026-03", 250, 10, latest, MeterReplacedFlags{Water: true}, noRollover)
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
	latest := makeLatest(roomA, "2026-02", 9999, 100)
	m, err := NewReading(roomA, "2026-03", 50, 120, latest, MeterReplacedFlags{Electricity: true}, noRollover)
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

func TestNewReading_SameMonth_OK(t *testing.T) {
	latest := makeLatest(roomA, "2026-03", 200, 100)
	_, err := NewReading(roomA, "2026-03", 250, 120, latest, MeterReplacedFlags{}, noRollover)
	if err != nil {
		t.Fatalf("same month should be allowed, got: %v", err)
	}
}

func TestNewReading_ElecCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 100)
	_, err := NewReading(roomA, "2026-03", 150, 120, latest, MeterReplacedFlags{}, noRollover)
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_WaterCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 100)
	_, err := NewReading(roomA, "2026-03", 250, 80, latest, MeterReplacedFlags{}, noRollover)
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_LatestRoomMismatch(t *testing.T) {
	latest := makeLatest(roomB, "2026-02", 200, 100)
	_, err := NewReading(roomA, "2026-03", 250, 120, latest, MeterReplacedFlags{}, noRollover)
	if err != ErrLatestRoomMismatch {
		t.Errorf("expected ErrLatestRoomMismatch, got %v", err)
	}
}

func TestNewReading_MonthBeforeLatest(t *testing.T) {
	latest := makeLatest(roomA, "2026-03", 200, 100)
	_, err := NewReading(roomA, "2026-02", 250, 120, latest, MeterReplacedFlags{}, noRollover)
	if err != ErrBillingMonthBeforeLatest {
		t.Errorf("expected ErrBillingMonthBeforeLatest, got %v", err)
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

var (
	noReplace  = MeterReplacedFlags{}
	noRollover = MeterRolloverFlags{}
)

func TestApplyUpdate_PartialElectricity(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(250), nil, noReplace, noRollover)
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
	err := m.ApplyUpdate(nil, intPtr(90), noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterCurrent != 90 {
		t.Errorf("WaterCurrent = %d, want 90", m.WaterCurrent)
	}
}

func TestApplyUpdate_BothFields(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(300), intPtr(100), noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 300 || m.WaterCurrent != 100 {
		t.Errorf("got elec=%d water=%d, want 300/100", m.ElectricityCurrent, m.WaterCurrent)
	}
}

func TestApplyUpdate_NilBoth_NoChange(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, nil, noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 200 || m.WaterCurrent != 80 {
		t.Errorf("values should be unchanged")
	}
}

func TestApplyUpdate_ElecBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(50), nil, noReplace, noRollover)
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestApplyUpdate_WaterBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, intPtr(30), noReplace, noRollover)
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}

// --- ApplyUpdate with meter replaced ---

func TestApplyUpdate_WaterReplaced_ResetsPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 5000, WaterCurrent: 5100}
	err := m.ApplyUpdate(nil, intPtr(10), MeterReplacedFlags{Water: true}, noRollover)
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
	err := m.ApplyUpdate(intPtr(20), nil, MeterReplacedFlags{Electricity: true}, noRollover)
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
		ElectricityPrevious: 1000, ElectricityCurrent: 1200,
		WaterPrevious: 100, WaterCurrent: 300,
	}
	bl := RoomBaseline{
		ElectricityBaseline: 100, WaterBaseline: 100,
		ElectricityHasEnoughData: true, WaterHasEnoughData: true,
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
		ElectricityPrevious: 1000, ElectricityCurrent: 1200,
		WaterPrevious: 100, WaterCurrent: 300,
	}
	bl := RoomBaseline{
		ElectricityBaseline: 100, WaterBaseline: 100,
		ElectricityHasEnoughData: true, WaterHasEnoughData: false,
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
	bl := RoomBaseline{ElectricityHasEnoughData: false, WaterHasEnoughData: false}
	m.ComputeAnomalies(bl)
	if m.IsAnomalyElectricity || m.IsAnomalyWater {
		t.Error("neither should be flagged when not enough data")
	}
}

func TestComputeAnomalies_NormalUsage_NoAnomaly(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 1000, ElectricityCurrent: 1100,
		WaterPrevious: 100, WaterCurrent: 110,
	}
	bl := RoomBaseline{
		ElectricityBaseline: 100, WaterBaseline: 100,
		ElectricityHasEnoughData: true, WaterHasEnoughData: true,
	}
	m.ComputeAnomalies(bl)
	if m.IsAnomalyElectricity {
		t.Error("electricity should not be anomaly")
	}
	if m.IsAnomalyWater {
		t.Error("water should not be anomaly")
	}
}

// --- digitMax ---

func TestDigitMax(t *testing.T) {
	tests := []struct {
		input, want int
	}{
		{0, 0}, {5, 9}, {9, 9}, {10, 99}, {99, 99},
		{100, 999}, {500, 999}, {999, 999},
		{9970, 9999}, {9999, 9999}, {10000, 99999},
	}
	for _, tt := range tests {
		if got := digitMax(tt.input); got != tt.want {
			t.Errorf("digitMax(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// --- Rollover usage ---

func TestElectricityUsed_Rollover(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 9970, ElectricityCurrent: 20, IsRolloverElectricity: true,
	}
	if got := m.ElectricityUsed(); got != 49 {
		t.Errorf("ElectricityUsed() = %d, want 49", got)
	}
}

func TestWaterUsed_Rollover(t *testing.T) {
	m := MeterReading{
		WaterPrevious: 500, WaterCurrent: 30, IsRolloverWater: true,
	}
	if got := m.WaterUsed(); got != 529 {
		t.Errorf("WaterUsed() = %d, want 529", got)
	}
}

func TestElectricityUsed_Rollover_EdgePrev999Current0(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 999, ElectricityCurrent: 0, IsRolloverElectricity: true}
	if got := m.ElectricityUsed(); got != 0 {
		t.Errorf("ElectricityUsed() = %d, want 0", got)
	}
}

func TestElectricityUsed_Rollover_EdgePrev999Current999(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 999, ElectricityCurrent: 999, IsRolloverElectricity: true}
	if got := m.ElectricityUsed(); got != 999 {
		t.Errorf("ElectricityUsed() = %d, want 999", got)
	}
}

func TestElectricityUsed_Rollover_EdgePrev9999Current0(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 9999, ElectricityCurrent: 0, IsRolloverElectricity: true}
	if got := m.ElectricityUsed(); got != 0 {
		t.Errorf("ElectricityUsed() = %d, want 0", got)
	}
}

// --- NewReading with rollover ---

func TestNewReading_Rollover_Electricity(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 9970, 100)
	m, err := NewReading(roomA, "2026-03", 20, 120, latest,
		MeterReplacedFlags{}, MeterRolloverFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 9970 {
		t.Errorf("ElectricityPrevious = %d, want 9970 (preserved)", m.ElectricityPrevious)
	}
	if !m.IsRolloverElectricity {
		t.Error("IsRolloverElectricity should be true")
	}
	if got := m.ElectricityUsed(); got != 49 {
		t.Errorf("ElectricityUsed() = %d, want 49", got)
	}
}

func TestNewReading_Rollover_Water(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 9970)
	m, err := NewReading(roomA, "2026-03", 250, 20, latest,
		MeterReplacedFlags{}, MeterRolloverFlags{Water: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterPrevious != 9970 {
		t.Errorf("WaterPrevious = %d, want 9970 (preserved)", m.WaterPrevious)
	}
	if !m.IsRolloverWater {
		t.Error("IsRolloverWater should be true")
	}
	if got := m.WaterUsed(); got != 49 {
		t.Errorf("WaterUsed() = %d, want 49", got)
	}
}

func TestNewReading_RolloverAndReplacedConflict_Electricity(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 9970, 100)
	_, err := NewReading(roomA, "2026-03", 20, 120, latest,
		MeterReplacedFlags{Electricity: true}, MeterRolloverFlags{Electricity: true})
	if err != ErrRolloverAndReplacedConflict {
		t.Errorf("expected ErrRolloverAndReplacedConflict, got %v", err)
	}
}

func TestNewReading_RolloverAndReplacedConflict_Water(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 9970)
	_, err := NewReading(roomA, "2026-03", 250, 20, latest,
		MeterReplacedFlags{Water: true}, MeterRolloverFlags{Water: true})
	if err != ErrRolloverAndReplacedConflict {
		t.Errorf("expected ErrRolloverAndReplacedConflict, got %v", err)
	}
}

func TestNewReading_RolloverWithZeroPrevious(t *testing.T) {
	_, err := NewReading(roomA, "2026-03", 20, 50, nil,
		MeterReplacedFlags{}, MeterRolloverFlags{Electricity: true})
	if err != ErrRolloverWithZeroPrevious {
		t.Errorf("expected ErrRolloverWithZeroPrevious, got %v", err)
	}
}

// --- ApplyUpdate with rollover ---

func TestApplyUpdate_Rollover_Electricity(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 9970, ElectricityCurrent: 9980, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(20), nil, noReplace, MeterRolloverFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 9970 {
		t.Errorf("ElectricityPrevious = %d, want 9970 (preserved)", m.ElectricityPrevious)
	}
	if m.ElectricityCurrent != 20 {
		t.Errorf("ElectricityCurrent = %d, want 20", m.ElectricityCurrent)
	}
	if !m.IsRolloverElectricity {
		t.Error("IsRolloverElectricity should be true")
	}
	if got := m.ElectricityUsed(); got != 49 {
		t.Errorf("ElectricityUsed() = %d, want 49", got)
	}
}

func TestApplyUpdate_NormalToRollover(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 9970, ElectricityCurrent: 9980, WaterPrevious: 50, WaterCurrent: 80}
	if got := m.ElectricityUsed(); got != 10 {
		t.Errorf("before: ElectricityUsed() = %d, want 10", got)
	}
	err := m.ApplyUpdate(intPtr(20), nil, noReplace, MeterRolloverFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.ElectricityUsed(); got != 49 {
		t.Errorf("after: ElectricityUsed() = %d, want 49", got)
	}
}

func TestApplyUpdate_RolloverAndReplacedConflict(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 9970, ElectricityCurrent: 9980, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(20), nil,
		MeterReplacedFlags{Electricity: true}, MeterRolloverFlags{Electricity: true})
	if err != ErrRolloverAndReplacedConflict {
		t.Errorf("expected ErrRolloverAndReplacedConflict, got %v", err)
	}
}

func TestApplyUpdate_RolloverWithZeroPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 0, ElectricityCurrent: 100, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(20), nil, noReplace, MeterRolloverFlags{Electricity: true})
	if err != ErrRolloverWithZeroPrevious {
		t.Errorf("expected ErrRolloverWithZeroPrevious, got %v", err)
	}
}

// --- ComputeAnomalies with rollover (skip) ---

func TestComputeAnomalies_SkipRollover(t *testing.T) {
	m := MeterReading{
		ElectricityPrevious: 9970, ElectricityCurrent: 20, IsRolloverElectricity: true,
		WaterPrevious: 100, WaterCurrent: 300,
	}
	bl := RoomBaseline{
		ElectricityBaseline: 100, WaterBaseline: 100,
		ElectricityHasEnoughData: true, WaterHasEnoughData: true,
	}
	m.ComputeAnomalies(bl)
	if m.IsAnomalyElectricity {
		t.Error("rollover electricity should NOT be flagged as anomaly")
	}
	if !m.IsAnomalyWater {
		t.Error("water should still be flagged as anomaly (usage 200 > baseline 100*1.5)")
	}
}

// --- isBeforeMonth ---

func TestIsBeforeMonth(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"2026-01", "2026-02", true},
		{"2026-02", "2026-01", false},
		{"2026-03", "2026-03", false},
		{"2025-12", "2026-01", true},
	}
	for _, tt := range tests {
		if got := isBeforeMonth(tt.a, tt.b); got != tt.want {
			t.Errorf("isBeforeMonth(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
