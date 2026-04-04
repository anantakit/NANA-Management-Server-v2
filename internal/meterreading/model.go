package meterreading

import (
	"errors"
	"time"

	"nana/internal/room"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Reading types ---

type ReadingType string

const (
	ReadingTypeMonthly ReadingType = "MONTHLY"
	ReadingTypeExit    ReadingType = "EXIT"
)

// --- Domain errors ---

var (
	ErrElectricityCurrentBelowPrevious = errors.New("ค่าไฟฟ้าปัจจุบันต้องมากกว่าหรือเท่ากับค่าก่อนหน้า")
	ErrWaterCurrentBelowPrevious       = errors.New("ค่าน้ำปัจจุบันต้องมากกว่าหรือเท่ากับค่าก่อนหน้า")
	ErrLatestRoomMismatch              = errors.New("ข้อมูลมิเตอร์ล่าสุดไม่ตรงกับห้อง")
	ErrBillingMonthBeforeLatest        = errors.New("เดือนที่จดมิเตอร์ต้องไม่ย้อนหลังกว่าครั้งล่าสุด")
	ErrExitDateBeforeLatest            = errors.New("วันจดมิเตอร์ย้ายออกต้องไม่ก่อนการจดมิเตอร์ครั้งล่าสุด")
	ErrOnlyLatestCanBeUpdated          = errors.New("แก้ไขได้เฉพาะรายการจดมิเตอร์ล่าสุดเท่านั้น")
	ErrRolloverAndReplacedConflict     = errors.New("ครบรอบมิเตอร์กับเปลี่ยนมิเตอร์ไม่สามารถเลือกพร้อมกันได้")
	ErrRolloverWithZeroPrevious        = errors.New("ไม่สามารถระบุครบรอบมิเตอร์ได้เมื่อค่าก่อนหน้าเป็น 0")
)

// --- Model ---

type MeterReading struct {
	ID                    uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	RoomID                uuid.UUID      `gorm:"type:uuid;not null" json:"room_id"`
	ReadingType           ReadingType    `gorm:"type:varchar(10);not null;default:'MONTHLY'" json:"reading_type"`
	BillingMonth          *string        `gorm:"type:varchar(7)" json:"billing_month"`
	ReadingDateActual     *time.Time     `gorm:"type:date" json:"reading_date_actual"`
	ElectricityPrevious   int            `gorm:"not null;default:0" json:"electricity_previous"`
	ElectricityCurrent    int            `gorm:"not null;default:0" json:"electricity_current"`
	WaterPrevious         int            `gorm:"not null;default:0" json:"water_previous"`
	WaterCurrent          int            `gorm:"not null;default:0" json:"water_current"`
	ReadBy                *uuid.UUID     `gorm:"type:uuid" json:"read_by"`
	IsRolloverElectricity bool           `gorm:"not null;default:false" json:"is_rollover_electricity"`
	IsRolloverWater       bool           `gorm:"not null;default:false" json:"is_rollover_water"`
	IsAnomalyElectricity  bool           `gorm:"not null;default:false" json:"is_anomaly_electricity"`
	IsAnomalyWater        bool           `gorm:"not null;default:false" json:"is_anomaly_water"`
	CreatedAt             time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt             time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt             gorm.DeletedAt `gorm:"index" json:"-"`

	Room *room.Room `gorm:"foreignKey:RoomID" json:"-"`
}

func (MeterReading) TableName() string { return "meter_readings" }

func (m *MeterReading) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

// --- Type helpers ---

func (m *MeterReading) IsMonthly() bool { return m.ReadingType == ReadingTypeMonthly }
func (m *MeterReading) IsExit() bool    { return m.ReadingType == ReadingTypeExit }

// temporalMonth returns the month portion for temporal comparison.
// MONTHLY → billing_month, EXIT → month of reading_date_actual.
func (m *MeterReading) temporalMonth() string {
	if m.BillingMonth != nil {
		return *m.BillingMonth
	}
	if m.ReadingDateActual != nil {
		return toMonth(*m.ReadingDateActual)
	}
	return ""
}

// --- Domain methods (pure, no DB, no side effects) ---

// digitMax returns 10^(number of digits in n) - 1.
// Used to infer the meter's max value from the previous reading.
// e.g. 9970 → 9999, 500 → 999, 0 → 0.
func digitMax(n int) int {
	if n <= 0 {
		return 0
	}
	max := 10
	for max <= n {
		max *= 10
	}
	return max - 1
}

func (m *MeterReading) ElectricityUsed() int {
	if m.IsRolloverElectricity {
		return (digitMax(m.ElectricityPrevious) - m.ElectricityPrevious) + m.ElectricityCurrent
	}
	return m.ElectricityCurrent - m.ElectricityPrevious
}

func (m *MeterReading) WaterUsed() int {
	if m.IsRolloverWater {
		return (digitMax(m.WaterPrevious) - m.WaterPrevious) + m.WaterCurrent
	}
	return m.WaterCurrent - m.WaterPrevious
}

func (m *MeterReading) validate() error {
	// Mutual exclusion: rollover + replaced cannot coexist per meter
	if m.IsRolloverElectricity && m.ElectricityPrevious == 0 {
		return ErrRolloverWithZeroPrevious
	}
	if m.IsRolloverWater && m.WaterPrevious == 0 {
		return ErrRolloverWithZeroPrevious
	}

	// Normal validation: current >= previous (skip when rollover)
	if !m.IsRolloverElectricity && m.ElectricityCurrent < m.ElectricityPrevious {
		return ErrElectricityCurrentBelowPrevious
	}
	if !m.IsRolloverWater && m.WaterCurrent < m.WaterPrevious {
		return ErrWaterCurrentBelowPrevious
	}
	return nil
}

// MeterReplacedFlags indicates which meters have been physically replaced.
type MeterReplacedFlags struct {
	Water       bool
	Electricity bool
}

// MeterRolloverFlags indicates which meters have rolled over (cycled past max).
type MeterRolloverFlags struct {
	Water       bool
	Electricity bool
}

// --- Month helpers (billing_month = "YYYY-MM") ---

// toMonth extracts "YYYY-MM" from a time.Time.
// Used for month-level comparisons between billing_month and contract dates.
// billingMonth is treated as whole-month coverage — even if contract starts mid-month, that month counts.
func toMonth(t time.Time) string {
	return t.Format("2006-01")
}

// isBeforeMonth returns true if month a is strictly before month b.
// Both must be "YYYY-MM" format. Single source of truth for month comparison.
func isBeforeMonth(a, b string) bool {
	return a < b
}

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

// --- Factory: MONTHLY reading ---

// NewReading creates a MONTHLY MeterReading with auto-populated previous values.
// Replaced meters start at previous = 0; rollover meters keep original previous.
func NewReading(roomID uuid.UUID, billingMonth string, elecCurrent, waterCurrent int, latest *MeterReading, replaced MeterReplacedFlags, rollover MeterRolloverFlags) (*MeterReading, error) {
	// Mutual exclusion: rollover + replaced cannot coexist per meter
	if rollover.Electricity && replaced.Electricity {
		return nil, ErrRolloverAndReplacedConflict
	}
	if rollover.Water && replaced.Water {
		return nil, ErrRolloverAndReplacedConflict
	}

	if latest != nil {
		// Guard: latest must belong to the same room
		if latest.RoomID != roomID {
			return nil, ErrLatestRoomMismatch
		}
		// Guard: billing month must not be before latest
		if isBeforeMonth(billingMonth, latest.temporalMonth()) {
			return nil, ErrBillingMonthBeforeLatest
		}
	}

	elecPrev, waterPrev := populatePrevious(latest, replaced)

	m := &MeterReading{
		RoomID:                roomID,
		ReadingType:           ReadingTypeMonthly,
		BillingMonth:          strPtr(billingMonth),
		ElectricityPrevious:   elecPrev,
		ElectricityCurrent:    elecCurrent,
		WaterPrevious:         waterPrev,
		WaterCurrent:          waterCurrent,
		IsRolloverElectricity: rollover.Electricity,
		IsRolloverWater:       rollover.Water,
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// --- Factory: EXIT reading ---

// NewExitReading creates an EXIT MeterReading for move-out settlement.
// EXIT readings use reading_date_actual instead of billing_month.
func NewExitReading(roomID uuid.UUID, readingDateActual time.Time, elecCurrent, waterCurrent int, latest *MeterReading, replaced MeterReplacedFlags, rollover MeterRolloverFlags) (*MeterReading, error) {
	// Mutual exclusion: rollover + replaced cannot coexist per meter
	if rollover.Electricity && replaced.Electricity {
		return nil, ErrRolloverAndReplacedConflict
	}
	if rollover.Water && replaced.Water {
		return nil, ErrRolloverAndReplacedConflict
	}

	if latest != nil {
		if latest.RoomID != roomID {
			return nil, ErrLatestRoomMismatch
		}
		exitMonth := toMonth(readingDateActual)
		if isBeforeMonth(exitMonth, latest.temporalMonth()) {
			return nil, ErrExitDateBeforeLatest
		}
	}

	elecPrev, waterPrev := populatePrevious(latest, replaced)

	m := &MeterReading{
		RoomID:                roomID,
		ReadingType:           ReadingTypeExit,
		ReadingDateActual:     &readingDateActual,
		ElectricityPrevious:   elecPrev,
		ElectricityCurrent:    elecCurrent,
		WaterPrevious:         waterPrev,
		WaterCurrent:          waterCurrent,
		IsRolloverElectricity: rollover.Electricity,
		IsRolloverWater:       rollover.Water,
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// populatePrevious derives previous values from the latest reading.
func populatePrevious(latest *MeterReading, replaced MeterReplacedFlags) (elecPrev, waterPrev int) {
	if latest == nil {
		return 0, 0
	}
	if !replaced.Electricity {
		elecPrev = latest.ElectricityCurrent
	}
	if !replaced.Water {
		waterPrev = latest.WaterCurrent
	}
	return
}

// CanUpdate checks that only the latest reading for a room can be updated.
func (m *MeterReading) CanUpdate(latestID uuid.UUID) error {
	if m.ID != latestID {
		return ErrOnlyLatestCanBeUpdated
	}
	return nil
}

// ApplyUpdate mutates current values and re-validates.
// Replaced meters reset their previous to 0; rollover meters keep original previous.
// Caller must verify CanUpdate() first.
func (m *MeterReading) ApplyUpdate(elecCurrent, waterCurrent *int, replaced MeterReplacedFlags, rollover MeterRolloverFlags) error {
	// Mutual exclusion: rollover + replaced cannot coexist per meter
	if rollover.Electricity && replaced.Electricity {
		return ErrRolloverAndReplacedConflict
	}
	if rollover.Water && replaced.Water {
		return ErrRolloverAndReplacedConflict
	}

	// Apply replaced flags (previous → 0)
	if replaced.Electricity {
		m.ElectricityPrevious = 0
	}
	if replaced.Water {
		m.WaterPrevious = 0
	}

	// Apply rollover flags (keep original previous)
	m.IsRolloverElectricity = rollover.Electricity
	m.IsRolloverWater = rollover.Water

	if elecCurrent != nil {
		m.ElectricityCurrent = *elecCurrent
	}
	if waterCurrent != nil {
		m.WaterCurrent = *waterCurrent
	}
	return m.validate()
}

// --- Anomaly detection ---

// RoomBaseline holds historical usage baselines per meter type.
// Computed from the last 3–6 persisted readings (excluding current batch).
type RoomBaseline struct {
	ElectricityBaseline      int
	WaterBaseline            int
	ElectricityHasEnoughData bool
	WaterHasEnoughData       bool
}

// IsAnomalousUsage detects whether a usage value is anomalous given a historical baseline.
// Single source of truth — frontend must use the equivalent formula.
//
//	baseline == 0 → usage > 100
//	else         → usage > baseline*3/2 && usage > 50
func IsAnomalousUsage(usage, baseline int) bool {
	if baseline == 0 {
		return usage > 100
	}
	return usage > baseline*3/2 && usage > 50
}

// ComputeAnomalies sets anomaly flags based on baselines.
// Each meter is evaluated independently only if it has enough historical data.
// Rollover meters are skipped — admin has already confirmed the reading.
func (m *MeterReading) ComputeAnomalies(bl RoomBaseline) {
	if bl.ElectricityHasEnoughData && !m.IsRolloverElectricity {
		m.IsAnomalyElectricity = IsAnomalousUsage(m.ElectricityUsed(), bl.ElectricityBaseline)
	}
	if bl.WaterHasEnoughData && !m.IsRolloverWater {
		m.IsAnomalyWater = IsAnomalousUsage(m.WaterUsed(), bl.WaterBaseline)
	}
}

// readingMonth returns the effective month for a reading (works for both MONTHLY and EXIT).
func readingMonth(m MeterReading) string {
	if m.BillingMonth != nil {
		return *m.BillingMonth
	}
	if m.ReadingDateActual != nil {
		return toMonth(*m.ReadingDateActual)
	}
	return ""
}
