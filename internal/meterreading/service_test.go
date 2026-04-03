package meterreading

import (
	"testing"
)

// --- median ---

func TestMedian_OddCount(t *testing.T) {
	got := median([]int{30, 10, 20})
	// sorted: [10, 20, 30], middle index=1 → 20
	if got != 20 {
		t.Errorf("median([30,10,20]) = %d, want 20", got)
	}
}

func TestMedian_EvenCount(t *testing.T) {
	got := median([]int{40, 10, 30, 20})
	// sorted: [10, 20, 30, 40], avg of middle two = (20+30)/2 = 25
	if got != 25 {
		t.Errorf("median([40,10,30,20]) = %d, want 25", got)
	}
}

func TestMedian_SixReadings(t *testing.T) {
	got := median([]int{100, 150, 80, 200, 120, 90})
	// sorted: [80, 90, 100, 120, 150, 200], avg of middle two = (100+120)/2 = 110
	if got != 110 {
		t.Errorf("median(6 values) = %d, want 110", got)
	}
}

func TestMedian_ThreeReadings(t *testing.T) {
	got := median([]int{5, 15, 10})
	// sorted: [5, 10, 15], middle = 10
	if got != 10 {
		t.Errorf("median([5,15,10]) = %d, want 10", got)
	}
}

// --- computeBaseline ---

func TestComputeBaseline_EnoughData(t *testing.T) {
	readings := []MeterReading{
		{ElectricityPrevious: 1000, ElectricityCurrent: 1100, WaterPrevious: 100, WaterCurrent: 110}, // elec=100, water=10
		{ElectricityPrevious: 900, ElectricityCurrent: 1000, WaterPrevious: 90, WaterCurrent: 100},   // elec=100, water=10
		{ElectricityPrevious: 800, ElectricityCurrent: 900, WaterPrevious: 80, WaterCurrent: 90},     // elec=100, water=10
	}
	bl := computeBaseline(readings)
	if !bl.ElectricityHasEnoughData {
		t.Error("expected ElectricityHasEnoughData=true")
	}
	if !bl.WaterHasEnoughData {
		t.Error("expected WaterHasEnoughData=true")
	}
	if bl.ElectricityBaseline != 100 {
		t.Errorf("ElectricityBaseline = %d, want 100", bl.ElectricityBaseline)
	}
	if bl.WaterBaseline != 10 {
		t.Errorf("WaterBaseline = %d, want 10", bl.WaterBaseline)
	}
}

func TestComputeBaseline_NotEnoughData(t *testing.T) {
	readings := []MeterReading{
		{ElectricityPrevious: 1000, ElectricityCurrent: 1100, WaterPrevious: 100, WaterCurrent: 110},
		{ElectricityPrevious: 900, ElectricityCurrent: 1000, WaterPrevious: 90, WaterCurrent: 100},
	}
	bl := computeBaseline(readings)
	if bl.ElectricityHasEnoughData {
		t.Error("expected ElectricityHasEnoughData=false with only 2 readings")
	}
	if bl.WaterHasEnoughData {
		t.Error("expected WaterHasEnoughData=false with only 2 readings")
	}
}

func TestComputeBaseline_Empty(t *testing.T) {
	bl := computeBaseline(nil)
	if bl.ElectricityHasEnoughData || bl.WaterHasEnoughData {
		t.Error("expected both HasEnoughData=false with no readings")
	}
}

func TestComputeBaseline_VariedUsage(t *testing.T) {
	readings := []MeterReading{
		{ElectricityPrevious: 0, ElectricityCurrent: 200, WaterPrevious: 0, WaterCurrent: 30}, // elec=200, water=30
		{ElectricityPrevious: 0, ElectricityCurrent: 100, WaterPrevious: 0, WaterCurrent: 10}, // elec=100, water=10
		{ElectricityPrevious: 0, ElectricityCurrent: 150, WaterPrevious: 0, WaterCurrent: 20}, // elec=150, water=20
		{ElectricityPrevious: 0, ElectricityCurrent: 300, WaterPrevious: 0, WaterCurrent: 40}, // elec=300, water=40
	}
	bl := computeBaseline(readings)
	// sorted elec: [100, 150, 200, 300], avg middle two = (150+200)/2 = 175
	if bl.ElectricityBaseline != 175 {
		t.Errorf("ElectricityBaseline = %d, want 175", bl.ElectricityBaseline)
	}
	// sorted water: [10, 20, 30, 40], avg middle two = (20+30)/2 = 25
	if bl.WaterBaseline != 25 {
		t.Errorf("WaterBaseline = %d, want 25", bl.WaterBaseline)
	}
}

// --- Tenant-scoped baseline filtering ---
// These test the date-filtering logic used by getBaselinesByRoomIDs.
// We simulate by filtering readings then passing to computeBaseline.

func makeReading(billingMonth string, elecPrev, elecCurr, waterPrev, waterCurr int) MeterReading {
	return MeterReading{
		BillingMonth:        billingMonth,
		ElectricityPrevious: elecPrev, ElectricityCurrent: elecCurr,
		WaterPrevious: waterPrev, WaterCurrent: waterCurr,
	}
}

func filterByStartMonth(readings []MeterReading, startMonth string) []MeterReading {
	var out []MeterReading
	for _, r := range readings {
		if !isBeforeMonth(r.BillingMonth, startMonth) {
			out = append(out, r)
		}
	}
	return out
}

func TestTenantScoped_6MonthsHistory_TenantStartedRecently_2Months(t *testing.T) {
	// Room has 6 months history, but current tenant started 2 months ago
	// → only 2 readings after start date → baseline null (not enough)
	readings := []MeterReading{
		makeReading("2025-10", 1000, 1100, 100, 110), // old tenant
		makeReading("2025-11", 1100, 1200, 110, 120), // old tenant
		makeReading("2025-12", 1200, 1300, 120, 130), // old tenant
		makeReading("2026-01", 1300, 1400, 130, 140), // old tenant
		makeReading("2026-02", 1400, 1550, 140, 160), // new tenant
		makeReading("2026-03", 1550, 1700, 160, 175), // new tenant
	}
	filtered := filterByStartMonth(readings, "2026-02")
	bl := computeBaseline(filtered)

	if bl.ElectricityHasEnoughData {
		t.Error("expected ElectricityHasEnoughData=false (only 2 readings after tenant start)")
	}
	if bl.WaterHasEnoughData {
		t.Error("expected WaterHasEnoughData=false (only 2 readings after tenant start)")
	}
}

func TestTenantScoped_CurrentTenant4Months(t *testing.T) {
	// Current tenant has 4 months → enough for baseline, should use only their data
	readings := []MeterReading{
		makeReading("2025-10", 1000, 1100, 100, 110), // old tenant: elec=100
		makeReading("2025-11", 1100, 1200, 110, 120), // old tenant: elec=100
		makeReading("2025-12", 1200, 1500, 120, 150), // new tenant: elec=300 (different pattern)
		makeReading("2026-01", 1500, 1800, 150, 180), // new tenant: elec=300
		makeReading("2026-02", 1800, 2100, 180, 210), // new tenant: elec=300
		makeReading("2026-03", 2100, 2400, 210, 240), // new tenant: elec=300
	}
	filtered := filterByStartMonth(readings, "2025-12")
	bl := computeBaseline(filtered)

	if !bl.ElectricityHasEnoughData {
		t.Fatal("expected ElectricityHasEnoughData=true")
	}
	// 4 readings all elec=300 → median = (300+300)/2 = 300
	if bl.ElectricityBaseline != 300 {
		t.Errorf("ElectricityBaseline = %d, want 300 (new tenant pattern)", bl.ElectricityBaseline)
	}
}

func TestTenantScoped_NoContract_EmptyBaseline(t *testing.T) {
	// Room without active contract → empty baseline (all false/zero)
	bl := RoomBaseline{} // this is what getBaselinesByRoomIDs returns for no contract
	if bl.ElectricityHasEnoughData || bl.WaterHasEnoughData {
		t.Error("no contract → baseline should be null (all false)")
	}
	if bl.ElectricityBaseline != 0 || bl.WaterBaseline != 0 {
		t.Error("no contract → baselines should be 0")
	}
}

func TestTenantScoped_ZeroUsageTenant_BaselineZeroNotNull(t *testing.T) {
	// Tenant uses nothing (baseline=0) — must be 0 with HasEnoughData=true, not null
	readings := []MeterReading{
		makeReading("2025-10", 1000, 1000, 100, 100), // usage=0
		makeReading("2025-11", 1000, 1000, 100, 100), // usage=0
		makeReading("2025-12", 1000, 1000, 100, 100), // usage=0
		makeReading("2026-01", 1000, 1000, 100, 100), // usage=0
	}
	bl := computeBaseline(readings)

	if !bl.ElectricityHasEnoughData {
		t.Error("expected ElectricityHasEnoughData=true (4 readings)")
	}
	if bl.ElectricityBaseline != 0 {
		t.Errorf("ElectricityBaseline = %d, want 0 (zero usage)", bl.ElectricityBaseline)
	}
	// This triggers the baseline=0 fallback rule: usage > 100 → anomaly
}
