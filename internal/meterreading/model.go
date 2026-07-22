package meterreading

import (
	"errors"
	"strings"
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

// --- Anchor reason (Reading Recovery doctrine) ---

// AnchorReason annotates a meter reading that breaks the prev-chain
// continuity. FIRST_ANCHOR is NOT an enum member — inception is derived
// state (latest == nil), per the locked doctrine split. Only
// discontinuity reasons live here.
type AnchorReason string

const (
	AnchorReasonPhysicalReplacement AnchorReason = "PHYSICAL_REPLACEMENT"
	AnchorReasonReadingRecovery     AnchorReason = "READING_RECOVERY"
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

	// Epic B Model B — move-out over-record ("เดือนก่อนจดเกิน").
	ErrOverRecordConflictsWithHardware = errors.New("ตัวเลือกเดือนก่อนจดเกินใช้พร้อมกับครบรอบ/เปลี่ยนมิเตอร์ไม่ได้")
	ErrOverRecordNotBelowPrevious      = errors.New("ตัวเลือกเดือนก่อนจดเกินใช้ได้เฉพาะเมื่อค่ามิเตอร์วันนี้ต่ำกว่าค่าที่จดครั้งก่อน")

	// Reading Recovery anchor errors (Phase 1 — ValidateAnchor).
	// ErrRecoverySourceRequired removed 2026-07-01 (source-optional relaxation):
	// source is now optional narrative metadata, no longer a validation gate.
	ErrAnchorNoteRequired    = errors.New("ต้องระบุเหตุผลเมื่อบันทึก anchor reason")
	ErrRecoverySelfReference = errors.New("READING_RECOVERY ไม่สามารถอ้างถึงตัวเองได้")
	ErrAnchorReasonInvalid   = errors.New("anchor_reason ไม่ถูกต้อง")

	// Reading Recovery prev=curr invariant (Phase 5 Lock A — triple-guard
	// domain-layer arm; DB CHECKs meter_readings_recovery_{elec,water}_prev_eq_curr
	// are the corruption-guard arm; service constructor is the wiring arm).
	ErrRecoveryElecPrevMustEqualCurrent  = errors.New("READING_RECOVERY: electricity_previous ต้องเท่ากับ electricity_current")
	ErrRecoveryWaterPrevMustEqualCurrent = errors.New("READING_RECOVERY: water_previous ต้องเท่ากับ water_current")

	// Q1.5 Over-Record: recorded (previously-recorded wrong value) may never be
	// below the physical current — recorded < current is an under-record, which
	// is out of scope (L1). recorded == current is an unaffected utility;
	// recorded > current is the over-record that drives a refund.
	ErrRecoveryElecRecordedBelowCurrent  = errors.New("READING_RECOVERY: electricity_recorded ต้องไม่น้อยกว่า electricity_current")
	ErrRecoveryWaterRecordedBelowCurrent = errors.New("READING_RECOVERY: water_recorded ต้องไม่น้อยกว่า water_current")

	// Replace Meter (PHYSICAL_REPLACEMENT event) validation.
	ErrReplacementNoUtility             = errors.New("ต้องระบุการเปลี่ยนมิเตอร์อย่างน้อยหนึ่งชนิด (ไฟฟ้า/น้ำ)")
	ErrReplacementNewInitialNegative    = errors.New("เลขเริ่มต้นมิเตอร์ใหม่ต้องไม่ติดลบ")
	ErrReplacementNewInitialRequired    = errors.New("ต้องระบุเลขเริ่มต้นมิเตอร์ใหม่เมื่อเปลี่ยนมิเตอร์")
	ErrReplacementOldFinalBelowPrevious = errors.New("เลขมิเตอร์เก่าตอนถอดต้องไม่น้อยกว่าเลขที่จดครั้งก่อน")
	ErrReplacementOldBoundaryIncomplete = errors.New("ข้อมูลมิเตอร์เก่าต้องมีทั้งค่าก่อนหน้าและค่าสุดท้าย")
)

// --- Model ---

type MeterReading struct {
	ID                    uuid.UUID   `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	RoomID                uuid.UUID   `gorm:"type:uuid;not null" json:"room_id"`
	ReadingType           ReadingType `gorm:"type:varchar(10);not null;default:'MONTHLY'" json:"reading_type"`
	BillingMonth          *string     `gorm:"type:varchar(7)" json:"billing_month"`
	ReadingDateActual     *time.Time  `gorm:"type:date" json:"reading_date_actual"`
	ElectricityPrevious   int         `gorm:"not null;default:0" json:"electricity_previous"`
	ElectricityCurrent    int         `gorm:"not null;default:0" json:"electricity_current"`
	WaterPrevious         int         `gorm:"not null;default:0" json:"water_previous"`
	WaterCurrent          int         `gorm:"not null;default:0" json:"water_current"`
	ReadBy                *uuid.UUID  `gorm:"type:uuid" json:"read_by"`
	IsRolloverElectricity bool        `gorm:"not null;default:false" json:"is_rollover_electricity"`
	IsRolloverWater       bool        `gorm:"not null;default:false" json:"is_rollover_water"`
	IsAnomalyElectricity  bool        `gorm:"not null;default:false" json:"is_anomaly_electricity"`
	IsAnomalyWater        bool        `gorm:"not null;default:false" json:"is_anomaly_water"`

	// Reading Recovery anchor fields (Phase 1). All nullable; populated only
	// when a reading breaks prev-chain continuity (PHYSICAL_REPLACEMENT or
	// READING_RECOVERY). Phase 5's service surface wires these via the
	// recovery commit path; Phase 1 ships persistence + ValidateAnchor.
	AnchorReason            *AnchorReason `gorm:"type:varchar(30)" json:"anchor_reason"`
	AnchorNote              *string       `gorm:"type:text" json:"anchor_note"`
	RecoverySourceReadingID *uuid.UUID    `gorm:"type:uuid" json:"recovery_source_reading_id"`

	// Q1.5 Over-Record: the previously-recorded (wrong) value being corrected,
	// per utility. Persisted on the READING_RECOVERY row so the deterministic
	// refund = (recorded − current) × rate can be computed without lineage and
	// independent of the (narrative-only) source. NULL = utility not corrected.
	// Lock A is unaffected: previous/current still hold the physical value.
	ElectricityRecorded *int `gorm:"column:electricity_recorded" json:"electricity_recorded,omitempty"`
	WaterRecorded       *int `gorm:"column:water_recorded" json:"water_recorded,omitempty"`

	// Replace Meter — which utility the operator physically replaced (declared
	// truth of the event). True only on PHYSICAL_REPLACEMENT rows. Distinguishes a
	// replaced-but-unreadable utility (old_final NULL) from an untouched one, and
	// drives per-utility Recovery×Replacement collision detection.
	ElectricityReplaced bool `gorm:"not null;default:false;column:electricity_replaced" json:"electricity_replaced,omitempty"`
	WaterReplaced       bool `gorm:"not null;default:false;column:water_replaced" json:"water_replaced,omitempty"`

	// Replace Meter (PHYSICAL_REPLACEMENT event) — the FROZEN old-meter closing
	// boundary, per utility. Populated ONLY on PHYSICAL_REPLACEMENT anchors and
	// ONLY for a replaced utility whose old meter was readable (nil = utility not
	// replaced, or old meter unreadable → tail 0, the honest floor).
	// tail(U) = {U}OldFinal − {U}OldPrevious, FROZEN at capture so a later lineage
	// mutation (recovery retract, move-out cancel) can never move a closed segment
	// (REPLACE_METER_PREBUILD_INVARIANT_CHALLENGE.md Q1). {U}Current holds the new
	// meter's initial reading (forward baseline); old_previous is snapshotted from
	// the predecessor reading, NOT re-derived from lineage.
	ElectricityOldPrevious *int `gorm:"column:electricity_old_previous" json:"electricity_old_previous,omitempty"`
	ElectricityOldFinal    *int `gorm:"column:electricity_old_final" json:"electricity_old_final,omitempty"`
	WaterOldPrevious       *int `gorm:"column:water_old_previous" json:"water_old_previous,omitempty"`
	WaterOldFinal          *int `gorm:"column:water_old_final" json:"water_old_final,omitempty"`
	// Provenance ONLY (never calculation authority): the reading this replacement
	// physically followed. ON DELETE SET NULL — must never block a cancel/retract.
	PredecessorReadingID *uuid.UUID `gorm:"type:uuid;column:predecessor_reading_id" json:"predecessor_reading_id,omitempty"`

	CreatedAt time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

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

// AffectedRecoveryUtilities returns the utilities this recovery over-records
// (recorded > physical current), as billing-side primitive strings. Empty when
// the row is not an over-record. Q1.5 §0b — drives the per-utility pending list.
func (m *MeterReading) AffectedRecoveryUtilities() []string {
	var out []string
	if m.ElectricityRecorded != nil && *m.ElectricityRecorded > m.ElectricityCurrent {
		out = append(out, "ELECTRICITY")
	}
	if m.WaterRecorded != nil && *m.WaterRecorded > m.WaterCurrent {
		out = append(out, "WATER")
	}
	return out
}

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

// ValidateAnchor enforces the Reading Recovery doctrine for anchor fields.
// Pure: no DB, no side effects.
//
// Doctrine: feedback_reading_recovery_doctrine.md (locked 2026-06-22).
// Guard:    feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22).
// Design:   /Users/anantakit/.claude/plans/mutable-swimming-firefly.md (Item 4).
//
// Phase 1 ships the method but does not wire it into NewReading /
// NewExitReading / ApplyUpdate — those factories don't accept anchor
// params yet. Phase 5's recovery commit path calls this explicitly
// before persistence.
//
// Rules:
//  1. AnchorReason == nil → nil (no-op; vast majority of rows, including
//     Workflow A first readings per the FIRST_ANCHOR=state lock).
//  2. *AnchorReason not in {PHYSICAL_REPLACEMENT, READING_RECOVERY} →
//     ErrAnchorReasonInvalid.
//  3. AnchorNote nil or whitespace-only → ErrAnchorNoteRequired
//     (TrimSpace covers Unicode whitespace; domain owns this — DB CHECK
//     enforces only NOT NULL).
//  4. READING_RECOVERY source is OPTIONAL (source-optional relaxation,
//     locked 2026-07-01): a nil RecoverySourceReadingID is valid — absence
//     is a complete resync, not a gap. No inference fills it.
//  5. READING_RECOVERY referencing itself (only checkable when a source is
//     supplied AND m.ID is populated) → ErrRecoverySelfReference. Nil source
//     skips the check; pre-BeforeCreate state (m.ID == uuid.Nil) is handed
//     off to the DB CHECK meter_readings_recovery_no_self_reference.
//  6. READING_RECOVERY with electricity_previous != electricity_current →
//     ErrRecoveryElecPrevMustEqualCurrent. Phase 5 Lock A (triple-guard
//     domain arm; DB CHECK meter_readings_recovery_elec_prev_eq_curr is
//     the corruption-guard arm).
//  7. READING_RECOVERY with water_previous != water_current →
//     ErrRecoveryWaterPrevMustEqualCurrent. Same triple-guard pattern.
func (m *MeterReading) ValidateAnchor() error {
	if m.AnchorReason == nil {
		return nil
	}
	switch *m.AnchorReason {
	case AnchorReasonPhysicalReplacement, AnchorReasonReadingRecovery:
		// valid
	default:
		return ErrAnchorReasonInvalid
	}
	if m.AnchorNote == nil || strings.TrimSpace(*m.AnchorNote) == "" {
		return ErrAnchorNoteRequired
	}
	if *m.AnchorReason == AnchorReasonReadingRecovery {
		// Source is optional (source-optional relaxation, locked 2026-07-01):
		// a recovery without a source is a valid, complete resync — absence is
		// not a gap. Self-reference is only checkable when a source is supplied
		// AND m.ID is populated (post-BeforeCreate); nil source skips it.
		if m.RecoverySourceReadingID != nil && m.ID != uuid.Nil && *m.RecoverySourceReadingID == m.ID {
			return ErrRecoverySelfReference
		}
		// Phase 5 Lock A: recovery rows are re-anchor events (usage=0).
		// previous = current is a doctrine invariant, enforced at three
		// layers (DB CHECK + this domain rule + service constructor).
		if m.ElectricityPrevious != m.ElectricityCurrent {
			return ErrRecoveryElecPrevMustEqualCurrent
		}
		if m.WaterPrevious != m.WaterCurrent {
			return ErrRecoveryWaterPrevMustEqualCurrent
		}
		// Q1.5 over-record: recorded (when captured) must not be below the
		// physical current — an under-record is out of scope. recorded == current
		// is an unaffected utility; recorded > current drives the refund.
		if m.ElectricityRecorded != nil && *m.ElectricityRecorded < m.ElectricityCurrent {
			return ErrRecoveryElecRecordedBelowCurrent
		}
		if m.WaterRecorded != nil && *m.WaterRecorded < m.WaterCurrent {
			return ErrRecoveryWaterRecordedBelowCurrent
		}
	}
	if *m.AnchorReason == AnchorReasonPhysicalReplacement {
		// The frozen old-meter boundary is a both-or-neither pair per utility,
		// and the old meter cannot run backwards (old_final >= old_previous;
		// equal = stuck → tail 0). DB CHECKs (migration 00049) are the backstop.
		if (m.ElectricityOldPrevious == nil) != (m.ElectricityOldFinal == nil) ||
			(m.WaterOldPrevious == nil) != (m.WaterOldFinal == nil) {
			return ErrReplacementOldBoundaryIncomplete
		}
		if m.ElectricityOldFinal != nil && *m.ElectricityOldFinal < *m.ElectricityOldPrevious {
			return ErrReplacementOldFinalBelowPrevious
		}
		if m.WaterOldFinal != nil && *m.WaterOldFinal < *m.WaterOldPrevious {
			return ErrReplacementOldFinalBelowPrevious
		}
	}
	return nil
}

// IsPhysicalReplacement reports whether this row is a PHYSICAL_REPLACEMENT event.
func (m *MeterReading) IsPhysicalReplacement() bool {
	return m.AnchorReason != nil && *m.AnchorReason == AnchorReasonPhysicalReplacement
}

// ElectricityReplacementTail returns the FROZEN old-meter closing consumption for
// electricity on a replacement event (old_final − old_previous), or 0 when this
// utility was not replaced / the old meter was unreadable. Pure; reads only the
// frozen columns, never lineage.
func (m *MeterReading) ElectricityReplacementTail() int {
	if m.ElectricityOldFinal == nil || m.ElectricityOldPrevious == nil {
		return 0
	}
	return *m.ElectricityOldFinal - *m.ElectricityOldPrevious
}

// WaterReplacementTail returns the FROZEN old-meter closing consumption for water.
func (m *MeterReading) WaterReplacementTail() int {
	if m.WaterOldFinal == nil || m.WaterOldPrevious == nil {
		return 0
	}
	return *m.WaterOldFinal - *m.WaterOldPrevious
}

// AffectedReplacementUtilities returns the utilities this event physically
// replaced, as billing-side primitive strings. Empty when not a replacement.
// Mirrors AffectedRecoveryUtilities so the billing layer can intersect the two
// for per-utility Recovery×Replacement collision detection (R-b guard).
func (m *MeterReading) AffectedReplacementUtilities() []string {
	if !m.IsPhysicalReplacement() {
		return nil
	}
	var out []string
	if m.ElectricityReplaced {
		out = append(out, "ELECTRICITY")
	}
	if m.WaterReplaced {
		out = append(out, "WATER")
	}
	return out
}

// --- Replace Meter: canonical period usage (structured, single source of truth) ---

// UsageSegmentKind classifies one physical consumption segment within a period.
type UsageSegmentKind string

const (
	// UsageSegmentReading is the ordinary/new-meter segment (current − previous).
	UsageSegmentReading UsageSegmentKind = "READING"
	// UsageSegmentReplacementOldTail is the old meter's closing segment at a
	// replacement (old_final − old_previous).
	UsageSegmentReplacementOldTail UsageSegmentKind = "REPLACEMENT_OLD_TAIL"
)

// UsageSegment is one raw physical consumption segment. The bill's human-readable
// breakdown is rendered from these SAME segments the total is summed from — the
// presentation layer must never recompute usage (owner lock 2026-07-21: else BE
// says 200 while UI explains 180). Carries no internal row IDs.
type UsageSegment struct {
	Kind     UsageSegmentKind `json:"kind"`
	Previous int              `json:"previous"`
	Current  int              `json:"current"` // new_current for READING; old_final for a tail
	Usage    int              `json:"usage"`
}

// UtilityCanonicalUsage is one utility's period usage: the billable Total plus
// the physical Segments that compose it (for the breakdown).
type UtilityCanonicalUsage struct {
	Total    int            `json:"total"`
	Segments []UsageSegment `json:"segments"`
}

// CanonicalPeriodUsage computes the canonical per-utility period usage for a
// billing reading (`consumption` — a MONTHLY consumption row or an EXIT reading)
// plus any PHYSICAL_REPLACEMENT events that fall in the same billing window.
//
//	usage(U) = consumption.Used(U)             [new/normal segment]
//	         + Σ replacement.tail(U)           [each FROZEN old-meter closing segment]
//
// Pure & order-independent: every tail is self-contained on its own frozen row
// (R4.5 Q1/Q2). With no replacements it returns exactly consumption.Used(U) as a
// single READING segment — byte-identical to the pre-Replace-Meter behaviour, so
// ordinary bills are unaffected. Segments are ordered chronologically for a
// natural "before → after" breakdown: caller passes `replacements` already sorted
// oldest-first; each old tail precedes the final reading segment.
func CanonicalPeriodUsage(consumption *MeterReading, replacements []*MeterReading) (elec, water UtilityCanonicalUsage) {
	for _, r := range replacements {
		if r == nil || !r.IsPhysicalReplacement() {
			continue
		}
		if r.ElectricityOldFinal != nil {
			t := r.ElectricityReplacementTail()
			elec.Segments = append(elec.Segments, UsageSegment{
				Kind: UsageSegmentReplacementOldTail, Previous: *r.ElectricityOldPrevious, Current: *r.ElectricityOldFinal, Usage: t,
			})
			elec.Total += t
		}
		if r.WaterOldFinal != nil {
			t := r.WaterReplacementTail()
			water.Segments = append(water.Segments, UsageSegment{
				Kind: UsageSegmentReplacementOldTail, Previous: *r.WaterOldPrevious, Current: *r.WaterOldFinal, Usage: t,
			})
			water.Total += t
		}
	}
	if consumption != nil {
		eUsed, wUsed := consumption.ElectricityUsed(), consumption.WaterUsed()
		elec.Segments = append(elec.Segments, UsageSegment{
			Kind: UsageSegmentReading, Previous: consumption.ElectricityPrevious, Current: consumption.ElectricityCurrent, Usage: eUsed,
		})
		elec.Total += eUsed
		water.Segments = append(water.Segments, UsageSegment{
			Kind: UsageSegmentReading, Previous: consumption.WaterPrevious, Current: consumption.WaterCurrent, Usage: wUsed,
		})
		water.Total += wUsed
	}
	return elec, water
}

// --- Replace Meter: factory ---

// ReplacementUtilityInput is the per-utility input for a physical replacement.
// Replaced=false → this utility is untouched (baseline carried forward, no tail).
// OldFinal is optional (nil = old meter unreadable → tail 0, the honest floor).
type ReplacementUtilityInput struct {
	Replaced   bool
	OldFinal   *int
	NewInitial int
}

// NewReplacementAnchor builds a PHYSICAL_REPLACEMENT anchor from the frozen
// physical boundary observed at the swap. `latest` is the room's latest reading
// at capture time; for a replaced utility its current is snapshotted as the FROZEN
// old_previous (R4.5 Q1 — NOT re-derived later). A replaced utility re-anchors to
// new_initial (previous=current=new_initial → forward baseline); a non-replaced
// utility carries latest's current forward unchanged. `new_current` is NOT part of
// the event — it arrives later as an ordinary reading.
func NewReplacementAnchor(roomID uuid.UUID, latest *MeterReading, month, note string, elec, water ReplacementUtilityInput) (*MeterReading, error) {
	if !elec.Replaced && !water.Replaced {
		return nil, ErrReplacementNoUtility
	}
	if (elec.Replaced && elec.NewInitial < 0) || (water.Replaced && water.NewInitial < 0) {
		return nil, ErrReplacementNewInitialNegative
	}

	// old_previous = the latest known value of the meter being removed (snapshot).
	// No prior reading → inception baseline 0 (first-ever reading semantics).
	var latestElec, latestWater int
	if latest != nil {
		latestElec, latestWater = latest.ElectricityCurrent, latest.WaterCurrent
	}

	reason := AnchorReasonPhysicalReplacement
	bm := month
	m := &MeterReading{
		RoomID:              roomID,
		ReadingType:         ReadingTypeMonthly,
		BillingMonth:        &bm,
		AnchorReason:        &reason,
		AnchorNote:          &note,
		ElectricityReplaced: elec.Replaced,
		WaterReplaced:       water.Replaced,
	}
	if latest != nil {
		m.PredecessorReadingID = &latest.ID
	}

	// Electricity
	if elec.Replaced {
		m.ElectricityPrevious, m.ElectricityCurrent = elec.NewInitial, elec.NewInitial
		if elec.OldFinal != nil {
			op := latestElec
			m.ElectricityOldPrevious, m.ElectricityOldFinal = &op, elec.OldFinal
		}
	} else {
		m.ElectricityPrevious, m.ElectricityCurrent = latestElec, latestElec
	}

	// Water
	if water.Replaced {
		m.WaterPrevious, m.WaterCurrent = water.NewInitial, water.NewInitial
		if water.OldFinal != nil {
			op := latestWater
			m.WaterOldPrevious, m.WaterOldFinal = &op, water.OldFinal
		}
	} else {
		m.WaterPrevious, m.WaterCurrent = latestWater, latestWater
	}

	if err := m.ValidateAnchor(); err != nil {
		return nil, err
	}
	return m, nil
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

// MeterOverRecordFlags marks, per utility, that the prior reading was recorded
// too high ("เดือนก่อนจดเกิน") and today's move-out reading is the true current.
// Epic B Model B: mutually exclusive with rollover/replaced per utility; valid
// only when the exit value is below the latest reading's current.
type MeterOverRecordFlags struct {
	Water       bool
	Electricity bool
}

// NewMoveOutOverRecordAnchor builds a READING_RECOVERY re-anchor for a move-out
// over-record (Epic B Model B). ONE physical observation — the exit reading —
// feeds TWO events (§0.1): this re-anchor and the EXIT reading. For each flagged
// utility the anchor re-anchors to the observed exit value (so exit usage = 0)
// and records the prior over-recorded value (recorded > current → source-priced
// refund at settlement, via the unchanged resolver). The non-flagged utility
// carries its prior baseline so its exit usage bills normally.
//
// The anchor MUST be persisted BEFORE the EXIT reading so the exit picks up
// previous = the re-anchored current (proven by Test A/B in
// EPIC_B_SETTLEMENT_RECOVERY_MODELB_ONTOLOGY_SCOPE.md §0.2). `latest` is the
// over-recorded prior reading and becomes the recovery's source; the caller
// guarantees each flagged utility is below its latest current.
func NewMoveOutOverRecordAnchor(roomID uuid.UUID, latest *MeterReading, exitElec, exitWater int, over MeterOverRecordFlags, month, note string) (*MeterReading, error) {
	if latest == nil {
		return nil, ErrOverRecordNotBelowPrevious
	}
	anchorElec, anchorWater := latest.ElectricityCurrent, latest.WaterCurrent
	var recordedElec, recordedWater *int
	if over.Electricity {
		anchorElec = exitElec
		v := latest.ElectricityCurrent
		recordedElec = &v
	}
	if over.Water {
		anchorWater = exitWater
		v := latest.WaterCurrent
		recordedWater = &v
	}
	reason := AnchorReasonReadingRecovery
	bm := month
	src := latest.ID
	m := &MeterReading{
		RoomID:                  roomID,
		ReadingType:             ReadingTypeMonthly,
		BillingMonth:            &bm,
		ElectricityPrevious:     anchorElec,
		ElectricityCurrent:      anchorElec,
		WaterPrevious:           anchorWater,
		WaterCurrent:            anchorWater,
		AnchorReason:            &reason,
		AnchorNote:              &note,
		RecoverySourceReadingID: &src,
		ElectricityRecorded:     recordedElec,
		WaterRecorded:           recordedWater,
	}
	if err := m.ValidateAnchor(); err != nil {
		return nil, err
	}
	return m, nil
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
