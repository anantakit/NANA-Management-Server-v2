package meterreading

import (
	"testing"
	"time"

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
		ReadingType:        ReadingTypeMonthly,
		BillingMonth:       strPtr(billingMonth),
		ElectricityCurrent: elecCurrent,
		WaterCurrent:       waterCurrent,
	}
}

func makeExitLatest(roomID uuid.UUID, dateStr string, elecCurrent, waterCurrent int) *MeterReading {
	t, _ := time.Parse("2006-01-02", dateStr)
	return &MeterReading{
		ID:                 uuid.New(),
		RoomID:             roomID,
		ReadingType:        ReadingTypeExit,
		ReadingDateActual:  &t,
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
	if m.ReadingType != ReadingTypeMonthly {
		t.Errorf("ReadingType = %s, want MONTHLY", m.ReadingType)
	}
	if m.BillingMonth == nil || *m.BillingMonth != "2026-03" {
		t.Errorf("BillingMonth mismatch")
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

// --- temporalMonth ---

func TestTemporalMonth_Monthly(t *testing.T) {
	m := MeterReading{ReadingType: ReadingTypeMonthly, BillingMonth: strPtr("2026-03")}
	if got := m.temporalMonth(); got != "2026-03" {
		t.Errorf("temporalMonth() = %q, want %q", got, "2026-03")
	}
}

func TestTemporalMonth_Exit(t *testing.T) {
	d := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m := MeterReading{ReadingType: ReadingTypeExit, ReadingDateActual: &d}
	if got := m.temporalMonth(); got != "2026-03" {
		t.Errorf("temporalMonth() = %q, want %q", got, "2026-03")
	}
}

// ==============================
// EXIT Reading Tests
// ==============================

func TestNewExitReading_FirstReading(t *testing.T) {
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m, err := NewExitReading(roomA, date, 100, 50, nil, noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ReadingType != ReadingTypeExit {
		t.Errorf("ReadingType = %s, want EXIT", m.ReadingType)
	}
	if m.ReadingDateActual == nil || !m.ReadingDateActual.Equal(date) {
		t.Error("ReadingDateActual mismatch")
	}
	if m.BillingMonth != nil {
		t.Errorf("BillingMonth should be nil for EXIT, got %v", m.BillingMonth)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Error("first reading previous should be 0")
	}
}

func TestNewExitReading_AutoPopulateFromMonthlyLatest(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 200, 100)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m, err := NewExitReading(roomA, date, 350, 130, latest, noReplace, noRollover)
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

func TestNewExitReading_AutoPopulateFromExitLatest(t *testing.T) {
	latest := makeExitLatest(roomA, "2026-03-10", 300, 150)
	date := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	m, err := NewExitReading(roomA, date, 350, 160, latest, noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 300 {
		t.Errorf("ElectricityPrevious = %d, want 300", m.ElectricityPrevious)
	}
	if m.WaterPrevious != 150 {
		t.Errorf("WaterPrevious = %d, want 150", m.WaterPrevious)
	}
}

func TestNewExitReading_DateBeforeLatestMonth(t *testing.T) {
	latest := makeLatest(roomA, "2026-04", 200, 100)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	_, err := NewExitReading(roomA, date, 250, 120, latest, noReplace, noRollover)
	if err != ErrExitDateBeforeLatest {
		t.Errorf("expected ErrExitDateBeforeLatest, got %v", err)
	}
}

func TestNewExitReading_SameMonthAsLatest_OK(t *testing.T) {
	latest := makeLatest(roomA, "2026-03", 200, 100)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	_, err := NewExitReading(roomA, date, 250, 120, latest, noReplace, noRollover)
	if err != nil {
		t.Fatalf("same month EXIT should be allowed, got: %v", err)
	}
}

func TestNewExitReading_RoomMismatch(t *testing.T) {
	latest := makeLatest(roomB, "2026-02", 200, 100)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	_, err := NewExitReading(roomA, date, 250, 120, latest, noReplace, noRollover)
	if err != ErrLatestRoomMismatch {
		t.Errorf("expected ErrLatestRoomMismatch, got %v", err)
	}
}

func TestNewExitReading_RolloverAndReplacedConflict(t *testing.T) {
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	_, err := NewExitReading(roomA, date, 20, 50, nil,
		MeterReplacedFlags{Electricity: true}, MeterRolloverFlags{Electricity: true})
	if err != ErrRolloverAndReplacedConflict {
		t.Errorf("expected ErrRolloverAndReplacedConflict, got %v", err)
	}
}

func TestNewExitReading_WithMeterReplaced(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 9999, 5000)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m, err := NewExitReading(roomA, date, 50, 10, latest, MeterReplacedFlags{Water: true, Electricity: true}, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Errorf("meter replaced should reset previous to 0")
	}
}

func TestNewExitReading_WithRollover(t *testing.T) {
	latest := makeLatest(roomA, "2026-02", 9970, 100)
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m, err := NewExitReading(roomA, date, 20, 120, latest, noReplace, MeterRolloverFlags{Electricity: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 9970 {
		t.Errorf("ElectricityPrevious = %d, want 9970", m.ElectricityPrevious)
	}
	if !m.IsRolloverElectricity {
		t.Error("IsRolloverElectricity should be true")
	}
	if got := m.ElectricityUsed(); got != 49 {
		t.Errorf("ElectricityUsed() = %d, want 49", got)
	}
}

// --- NewReading after EXIT latest (continuity) ---

func TestNewReading_AfterExitLatest(t *testing.T) {
	latest := makeExitLatest(roomA, "2026-03-15", 300, 150)
	m, err := NewReading(roomA, "2026-04", 400, 200, latest, noReplace, noRollover)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 300 {
		t.Errorf("ElectricityPrevious = %d, want 300 (from EXIT latest)", m.ElectricityPrevious)
	}
	if m.WaterPrevious != 150 {
		t.Errorf("WaterPrevious = %d, want 150 (from EXIT latest)", m.WaterPrevious)
	}
}

func TestNewReading_BeforeExitLatestMonth(t *testing.T) {
	latest := makeExitLatest(roomA, "2026-04-10", 300, 150)
	_, err := NewReading(roomA, "2026-03", 350, 160, latest, noReplace, noRollover)
	if err != ErrBillingMonthBeforeLatest {
		t.Errorf("expected ErrBillingMonthBeforeLatest, got %v", err)
	}
}

// --- readingMonth ---

func TestReadingMonth_Monthly(t *testing.T) {
	m := MeterReading{BillingMonth: strPtr("2026-03")}
	if got := readingMonth(m); got != "2026-03" {
		t.Errorf("readingMonth() = %q, want %q", got, "2026-03")
	}
}

func TestReadingMonth_Exit(t *testing.T) {
	d := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	m := MeterReading{ReadingDateActual: &d}
	if got := readingMonth(m); got != "2026-03" {
		t.Errorf("readingMonth() = %q, want %q", got, "2026-03")
	}
}

func TestReadingMonth_NilBoth(t *testing.T) {
	m := MeterReading{}
	if got := readingMonth(m); got != "" {
		t.Errorf("readingMonth() = %q, want empty", got)
	}
}

// --- ValidateAnchor — Reading Recovery doctrine (Phase 1 D1) ---
//
// DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22).
// GUARD:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22).
// DESIGN:   /Users/anantakit/.claude/plans/mutable-swimming-firefly.md (Item 4).
//
// Six tests, locking:
//   - NoAnchorReason → nil (no-op; covers regular MONTHLY/EXIT AND Workflow A
//     first readings, since FIRST_ANCHOR is derived state).
//   - AnchorReasonInvalid → ErrAnchorReasonInvalid (defense-in-depth above DB CHECK).
//   - AnchorNoteEdgeCases → covers Unicode whitespace bypass attempts.
//   - RecoveryRequiresSourceReading → ErrRecoverySourceRequired.
//   - PhysicalReplacementDoesNotRequireSource → asymmetry vs RECOVERY.
//   - RecoveryCannotReferenceItself → ErrRecoverySelfReference;
//     pre-BeforeCreate skip case documented inline.

func TestMeterReading_ValidateAnchor_NoAnchorReason_ReturnsNil(t *testing.T) {
	m := &MeterReading{
		ID:     uuid.New(),
		RoomID: roomA,
		// AnchorReason nil; other anchor fields nil.
	}
	if err := m.ValidateAnchor(); err != nil {
		t.Errorf("ValidateAnchor() = %v, want nil for non-anchor row", err)
	}

	// Stray note without a reason is also OK — doctrine guards
	// reason-without-note, not the reverse.
	stray := "stray text"
	m.AnchorNote = &stray
	if err := m.ValidateAnchor(); err != nil {
		t.Errorf("ValidateAnchor() with stray note but nil reason = %v, want nil", err)
	}
}

func TestMeterReading_ValidateAnchor_AnchorReasonInvalid_ReturnsError(t *testing.T) {
	bogus := AnchorReason("BOGUS_REASON")
	note := "anything"
	m := &MeterReading{
		ID:           uuid.New(),
		RoomID:       roomA,
		AnchorReason: &bogus,
		AnchorNote:   &note,
	}
	if err := m.ValidateAnchor(); err != ErrAnchorReasonInvalid {
		t.Errorf("ValidateAnchor() = %v, want ErrAnchorReasonInvalid", err)
	}
}

func TestMeterReading_ValidateAnchor_AnchorNoteEdgeCases(t *testing.T) {
	reason := AnchorReasonPhysicalReplacement
	cases := []struct {
		name    string
		note    *string
		wantErr error
	}{
		{"nil note", nil, ErrAnchorNoteRequired},
		{"empty string", strPtr(""), ErrAnchorNoteRequired},
		{"ascii whitespace only", strPtr("   "), ErrAnchorNoteRequired},
		{"control whitespace only", strPtr("\n\t  "), ErrAnchorNoteRequired},
		{"single char", strPtr("x"), nil},
		{"normal note", strPtr("เปลี่ยนมิเตอร์เพราะพัง"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &MeterReading{
				ID:           uuid.New(),
				RoomID:       roomA,
				AnchorReason: &reason,
				AnchorNote:   tc.note,
			}
			if got := m.ValidateAnchor(); got != tc.wantErr {
				t.Errorf("ValidateAnchor() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// TestMeterReading_ValidateAnchor_RecoverySourceOptional locks the
// source-optional relaxation (2026-07-01): a READING_RECOVERY with a nil
// RecoverySourceReadingID is VALID — absence of a source is a complete
// recovery, not a gap. (Inverted from the old ...RequiresSourceReading test,
// which asserted the now-removed ErrRecoverySourceRequired.)
func TestMeterReading_ValidateAnchor_RecoverySourceOptional(t *testing.T) {
	reason := AnchorReasonReadingRecovery
	note := "ค้นพบว่าเดือนเมษายนจดมิเตอร์ผิด"
	m := &MeterReading{
		ID:                      uuid.New(),
		RoomID:                  roomA,
		AnchorReason:            &reason,
		AnchorNote:              &note,
		RecoverySourceReadingID: nil, // no source supplied — now valid
		// prev == curr (0 == 0) satisfies Lock A.
	}
	if err := m.ValidateAnchor(); err != nil {
		t.Errorf("ValidateAnchor() = %v, want nil — source is optional", err)
	}
}

func TestMeterReading_ValidateAnchor_PhysicalReplacementDoesNotRequireSource(t *testing.T) {
	reason := AnchorReasonPhysicalReplacement
	note := "เปลี่ยนมิเตอร์เพราะพัง"
	m := &MeterReading{
		ID:                      uuid.New(),
		RoomID:                  roomA,
		AnchorReason:            &reason,
		AnchorNote:              &note,
		RecoverySourceReadingID: nil, // PHYSICAL_REPLACEMENT doesn't need it
	}
	if err := m.ValidateAnchor(); err != nil {
		t.Errorf("ValidateAnchor() = %v, want nil — replacement doesn't need source FK", err)
	}
}

func TestMeterReading_ValidateAnchor_RecoveryCannotReferenceItself(t *testing.T) {
	reason := AnchorReasonReadingRecovery
	note := "self-ref guard"

	t.Run("ID set and source==ID", func(t *testing.T) {
		id := uuid.New()
		m := &MeterReading{
			ID:                      id,
			RoomID:                  roomA,
			AnchorReason:            &reason,
			AnchorNote:              &note,
			RecoverySourceReadingID: &id,
		}
		if err := m.ValidateAnchor(); err != ErrRecoverySelfReference {
			t.Errorf("ValidateAnchor() = %v, want ErrRecoverySelfReference", err)
		}
	})

	t.Run("pre-BeforeCreate skip: ID=nil and source=nil-uuid", func(t *testing.T) {
		// Before GORM's BeforeCreate hook fires, m.ID == uuid.Nil. The
		// domain guard skips in this state and hands self-reference
		// detection to the DB CHECK constraint at INSERT time (which
		// runs AFTER BeforeCreate populates m.ID). This sub-case
		// documents that the skip is intentional, not a bug.
		nilID := uuid.Nil
		m := &MeterReading{
			ID:                      uuid.Nil,
			RoomID:                  roomA,
			AnchorReason:            &reason,
			AnchorNote:              &note,
			RecoverySourceReadingID: &nilID,
		}
		if err := m.ValidateAnchor(); err != nil {
			t.Errorf("ValidateAnchor() = %v, want nil (pre-BeforeCreate skip)", err)
		}
	})

	t.Run("ID set and source!=ID", func(t *testing.T) {
		m := &MeterReading{
			ID:                      uuid.New(),
			RoomID:                  roomA,
			AnchorReason:            &reason,
			AnchorNote:              &note,
			RecoverySourceReadingID: ptrUUID(uuid.New()),
		}
		if err := m.ValidateAnchor(); err != nil {
			t.Errorf("ValidateAnchor() = %v, want nil (distinct source)", err)
		}
	})

	t.Run("ID set and source==nil: no false self-ref, no panic", func(t *testing.T) {
		// Source-optional (2026-07-01): the nil-source guard must short-circuit
		// before the *RecoverySourceReadingID deref — a nil source can never be
		// a self-reference and must not panic.
		m := &MeterReading{
			ID:                      uuid.New(),
			RoomID:                  roomA,
			AnchorReason:            &reason,
			AnchorNote:              &note,
			RecoverySourceReadingID: nil,
		}
		if err := m.ValidateAnchor(); err != nil {
			t.Errorf("ValidateAnchor() = %v, want nil (nil source is not a self-ref)", err)
		}
	})
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

// TestMeterReading_ValidateAnchor_RecoveryRequiresPrevEqualsCurrent locks
// Phase 5 Lock A's domain-layer arm: READING_RECOVERY rows are re-anchor
// events (usage=0) and prev=curr is a doctrine invariant.
//
// Doctrine: feedback_reading_recovery_doctrine.md line 33.
// Plan:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md (Lock A).
//
// The DB CHECK constraints meter_readings_recovery_{elec,water}_prev_eq_curr
// (migration 00040) are the corruption-guard arm of the triple guard.
func TestMeterReading_ValidateAnchor_RecoveryRequiresPrevEqualsCurrent(t *testing.T) {
	recoveryReason := AnchorReasonReadingRecovery
	note := "ค้นพบว่าจดมิเตอร์ผิด"
	sourceID := uuid.New()

	cases := []struct {
		name                 string
		elecPrev, elecCurr   int
		waterPrev, waterCurr int
		wantErr              error
	}{
		{"both equal (valid)", 200, 200, 55, 55, nil},
		{"elec prev != curr", 199, 200, 55, 55, ErrRecoveryElecPrevMustEqualCurrent},
		{"water prev != curr", 200, 200, 54, 55, ErrRecoveryWaterPrevMustEqualCurrent},
		{"both differ (elec fires first)", 199, 200, 54, 55, ErrRecoveryElecPrevMustEqualCurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &MeterReading{
				RoomID:                  roomA,
				AnchorReason:            &recoveryReason,
				AnchorNote:              &note,
				RecoverySourceReadingID: &sourceID,
				ElectricityPrevious:     tc.elecPrev,
				ElectricityCurrent:      tc.elecCurr,
				WaterPrevious:           tc.waterPrev,
				WaterCurrent:            tc.waterCurr,
			}
			if got := m.ValidateAnchor(); got != tc.wantErr {
				t.Errorf("ValidateAnchor() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// TestMeterReading_ValidateAnchor_RecoveryRecordedNotBelowCurrent locks the
// Q1.5 over-record rule: recorded (previously-recorded wrong value) may never be
// below the physical current — recorded < current is an under-record (out of
// scope, L1). recorded == current (unaffected) and recorded > current
// (over-record) are both valid; NULL recorded is valid (utility not corrected).
func TestMeterReading_ValidateAnchor_RecoveryRecordedNotBelowCurrent(t *testing.T) {
	recoveryReason := AnchorReasonReadingRecovery
	note := "ค้นพบว่าจดมิเตอร์ผิด"
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name     string
		elecRec  *int
		waterRec *int
		wantErr  error
	}{
		{"both nil (valid — no utility corrected)", nil, nil, nil},
		{"elec over-record (recorded > current)", intPtr(1500), nil, nil},
		{"elec recorded == current (unaffected, valid)", intPtr(1200), nil, nil},
		{"water over-record only", nil, intPtr(300), nil},
		{"elec under-record rejected", intPtr(1199), nil, ErrRecoveryElecRecordedBelowCurrent},
		{"water under-record rejected", nil, intPtr(219), ErrRecoveryWaterRecordedBelowCurrent},
		{"elec fires before water", intPtr(1199), intPtr(219), ErrRecoveryElecRecordedBelowCurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &MeterReading{
				RoomID:              roomA,
				AnchorReason:        &recoveryReason,
				AnchorNote:          &note,
				ElectricityPrevious: 1200, ElectricityCurrent: 1200, // physical (prev=curr, Lock A)
				WaterPrevious: 220, WaterCurrent: 220,
				ElectricityRecorded: tc.elecRec,
				WaterRecorded:       tc.waterRec,
			}
			if got := m.ValidateAnchor(); got != tc.wantErr {
				t.Errorf("ValidateAnchor() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// TestMeterReading_ValidateAnchor_PhysicalReplacementAllowsPrevNeCurrent
// locks the SCOPE of Lock A's new rules: prev=curr is required ONLY for
// READING_RECOVERY. PHYSICAL_REPLACEMENT explicitly does not require it
// (a physical meter swap resets the reading independently of the prior).
func TestMeterReading_ValidateAnchor_PhysicalReplacementAllowsPrevNeCurrent(t *testing.T) {
	replacementReason := AnchorReasonPhysicalReplacement
	note := "เปลี่ยนมิเตอร์ใหม่"

	m := &MeterReading{
		RoomID:              roomA,
		AnchorReason:        &replacementReason,
		AnchorNote:          &note,
		ElectricityPrevious: 0,
		ElectricityCurrent:  50,
		WaterPrevious:       0,
		WaterCurrent:        10,
	}
	if err := m.ValidateAnchor(); err != nil {
		t.Errorf("ValidateAnchor() = %v, want nil — prev=curr rule is RECOVERY-only", err)
	}
}
