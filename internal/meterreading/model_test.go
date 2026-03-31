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
	m, err := NewReading(roomA, date("2026-03-01"), 100, 50, nil, false)
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
	m, err := NewReading(roomA, date("2026-03-01"), 350, 130, latest, false)
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
	m, err := NewReading(roomA, date("2026-03-01"), 50, 10, latest, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityPrevious != 0 || m.WaterPrevious != 0 {
		t.Errorf("meter replaced should reset previous to 0, got elec=%d water=%d", m.ElectricityPrevious, m.WaterPrevious)
	}
}

func TestNewReading_SameDate_OK(t *testing.T) {
	latest := makeLatest(roomA, "2026-03-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 120, latest, false)
	if err != nil {
		t.Fatalf("same date should be allowed, got: %v", err)
	}
}

func TestNewReading_ElecCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 150, 120, latest, false)
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_WaterCurrentBelowPrevious(t *testing.T) {
	latest := makeLatest(roomA, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 80, latest, false)
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}

func TestNewReading_LatestRoomMismatch(t *testing.T) {
	latest := makeLatest(roomB, "2026-02-01", 200, 100)
	_, err := NewReading(roomA, date("2026-03-01"), 250, 120, latest, false)
	if err != ErrLatestRoomMismatch {
		t.Errorf("expected ErrLatestRoomMismatch, got %v", err)
	}
}

func TestNewReading_DateBeforeLatest(t *testing.T) {
	latest := makeLatest(roomA, "2026-03-01", 200, 100)
	_, err := NewReading(roomA, date("2026-02-15"), 250, 120, latest, false)
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

func TestApplyUpdate_PartialElectricity(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(250), nil)
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
	err := m.ApplyUpdate(nil, intPtr(90))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WaterCurrent != 90 {
		t.Errorf("WaterCurrent = %d, want 90", m.WaterCurrent)
	}
}

func TestApplyUpdate_BothFields(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(300), intPtr(100))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 300 || m.WaterCurrent != 100 {
		t.Errorf("got elec=%d water=%d, want 300/100", m.ElectricityCurrent, m.WaterCurrent)
	}
}

func TestApplyUpdate_NilBoth_NoChange(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ElectricityCurrent != 200 || m.WaterCurrent != 80 {
		t.Errorf("values should be unchanged")
	}
}

func TestApplyUpdate_ElecBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(intPtr(50), nil)
	if err != ErrElectricityCurrentBelowPrevious {
		t.Errorf("expected ErrElectricityCurrentBelowPrevious, got %v", err)
	}
}

func TestApplyUpdate_WaterBelowPrevious(t *testing.T) {
	m := MeterReading{ElectricityPrevious: 100, ElectricityCurrent: 200, WaterPrevious: 50, WaterCurrent: 80}
	err := m.ApplyUpdate(nil, intPtr(30))
	if err != ErrWaterCurrentBelowPrevious {
		t.Errorf("expected ErrWaterCurrentBelowPrevious, got %v", err)
	}
}
