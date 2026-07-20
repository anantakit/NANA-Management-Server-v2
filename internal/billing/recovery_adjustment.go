package billing

import (
	"errors"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// --- Q1.5 Over-Record Re-model — refund/waive decision primitives ---
//
// North-star doctrine: recovery never discovers truth; it acknowledges that the
// previously recorded baseline was wrong. Every rule here is a consequence:
// refund-only (a too-high baseline means over-payment) and deterministic amount
// (recorded−physical at current rate). The decision is ACCEPT (refund the full
// deterministic amount) or WAIVE (refund nothing) — no partial path; any other
// money is a separate billing line item.
// See project_reading_recovery_overrecord_model_lock + Q1_5_OVERRECORD_REMODEL_PLAN.md.

var (
	ErrCorrectionUtilityNotAffected = errors.New("utility นี้ไม่ใช่ over-record (recorded ต้องมากกว่าค่าที่อ่านได้จริง)")
	ErrCorrectionUtilityInvalid     = errors.New("utility ไม่ถูกต้อง (ต้องเป็น ELECTRICITY หรือ WATER)")
)

// IsAffected reports whether a utility is over-recorded (recorded > physical).
// Derived, never stored. A utility with physical == recorded is unaffected and
// produces no refund line.
func IsAffected(recorded, physical int64) bool { return recorded > physical }

// RecommendRefund returns the deterministic refund (negative satang) for an
// over-record correction: the tenant over-paid (recorded−physical) units at the
// current rate. Precondition (Q1.5 §0b): recorded > physical. When recorded <=
// physical it is not an over-record — returns (0, false) and the flow must not
// engage. ratePerUnit is satang per unit.
func RecommendRefund(recorded, physical, ratePerUnit int64) (refundSatang int64, ok bool) {
	if recorded <= physical {
		return 0, false
	}
	overUnits := recorded - physical
	return -(overUnits * ratePerUnit), true
}

// ParseAdjustmentUtility validates a raw utility string.
func ParseAdjustmentUtility(s string) (AdjustmentUtility, error) {
	u := AdjustmentUtility(s)
	if !u.IsValid() {
		return "", ErrCorrectionUtilityInvalid
	}
	return u, nil
}

// ResolveCorrection derives the deterministic refund resolution for one utility
// of a recovery meter row (Q1.6 auto-refund — NO decision, NO note). Given the
// recovery row, the utility, and the utility's current contract rate
// (satang/unit), it:
//   - enforces the over-record precondition (recorded > physical; else
//     ErrCorrectionUtilityNotAffected);
//   - computes the refund deterministically (RecommendRefund) from the rate +
//     recorded/physical;
//   - returns the RecoveryResolution ready for BuildRecoveryAdjustmentLine plus
//     the AUTO line type that is (already) re-baselined to usage 0.
//
// A zero-rate over-record resolves with Amount 0 (nothing was charged, nothing
// to refund) — the caller emits no line. Money authority stays here; there is no
// operator money input anywhere.
func ResolveCorrection(
	recovery *meterreading.MeterReading,
	utility AdjustmentUtility,
	ratePerUnitSatang int64,
) (RecoveryResolution, LineItemType, error) {
	var (
		recorded *int
		physical int
		lineType LineItemType
	)
	switch utility {
	case AdjustmentUtilityElectricity:
		recorded, physical, lineType = recovery.ElectricityRecorded, recovery.ElectricityCurrent, LineItemElectricity
	case AdjustmentUtilityWater:
		recorded, physical, lineType = recovery.WaterRecorded, recovery.WaterCurrent, LineItemWater
	default:
		return RecoveryResolution{}, "", ErrCorrectionUtilityInvalid
	}

	if recorded == nil {
		return RecoveryResolution{}, "", ErrCorrectionUtilityNotAffected
	}
	recorded64, physical64 := int64(*recorded), int64(physical)
	recommended, ok := RecommendRefund(recorded64, physical64, ratePerUnitSatang)
	if !ok {
		return RecoveryResolution{}, "", ErrCorrectionUtilityNotAffected
	}

	return RecoveryResolution{
		RecoveryReadingID: recovery.ID,
		Utility:           utility,
		Amount:            recommended, // negative satang; 0 for a zero-rate over-record
		Recorded:          recorded64,
		Physical:          physical64,
		RatePerUnit:       ratePerUnitSatang,
	}, lineType, nil
}

// RecoveryResolution is the deterministic refund derived from one utility of a
// recovery meter row (Q1.6 auto-refund — no operator decision). Amount < 0 for a
// real over-record on a charged utility; Amount == 0 for a zero-rate over-record
// (the caller emits no line).
//
// Utility names which metered utility this settles (electricity/water are
// independent). Recorded/Physical/RatePerUnit carry the over-record EVIDENCE
// onto the line so every surface (admin edit + tenant delivery) can explain the
// refund: "ค่าที่จด {Recorded} → ค่าที่อ่านได้ {Physical}", "เกิน {Recorded−Physical} หน่วย ×
// {rate}". The refund amount is exactly (Recorded−Physical)×RatePerUnit.
type RecoveryResolution struct {
	RecoveryReadingID uuid.UUID
	Utility           AdjustmentUtility
	Amount            int64
	Recorded          int64 // the previously-recorded (wrong) reading
	Physical          int64 // the physical (correct) reading; Recorded > Physical
	RatePerUnit       int64 // satang per unit — the utility's contract rate
}

// BuildRecoveryAdjustmentLine constructs and validates the refund ADJUSTMENT line
// for a resolved recovery, ready to persist on billID at sortOrder. Pure: no I/O.
//
// Single construction+validation site shared by every destination (monthly +
// settlement generation) so amount/quantity/description/utility never drift.
// Q1.6 is refund-only: reason is always METER_RECOVERY with Amount < 0
// (ValidateAdjustment rejects zero/positive — a zero-rate over-record is skipped
// by the caller, never reaches here).
func BuildRecoveryAdjustmentLine(billID uuid.UUID, res RecoveryResolution, sortOrder int) (BillLineItem, error) {
	reason := AdjustmentReasonMeterRecovery
	recoveryFK := res.RecoveryReadingID

	// Evidence carried on the line so every surface can explain the refund:
	// quantity = over-record units, unit_price = utility rate, description states
	// recorded → physical. The amount is exactly −(quantity × unit_price); the
	// render shows "เกิน {qty} หน่วย × {rate}".
	overUnits := res.Recorded - res.Physical

	line := BillLineItem{
		BillID:                      billID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		Description:                 buildAdjustmentDescription(res),
		Amount:                      res.Amount,
		Quantity:                    int(overUnits),
		UnitPrice:                   res.RatePerUnit,
		SortOrder:                   sortOrder,
		AdjustmentRecoveryReadingID: &recoveryFK,
		AdjustmentReasonCode:        &reason,
	}
	if res.Utility != "" {
		u := res.Utility
		line.AdjustmentUtility = &u
	}
	if err := line.ValidateAdjustment(); err != nil {
		return BillLineItem{}, err
	}
	return line, nil
}
