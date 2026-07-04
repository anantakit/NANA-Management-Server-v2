package billing

import (
	"errors"

	"github.com/google/uuid"
)

// --- Q1.5 Over-Record Re-model — refund/waive decision primitives ---
//
// North-star doctrine: recovery never discovers truth; it acknowledges that the
// previously recorded baseline was wrong. Every rule here is a consequence:
// refund-only (a too-high baseline means over-payment), deterministic amount
// (recorded−physical at current rate), override bounded to a smaller refund.
// See project_reading_recovery_overrecord_model_lock + Q1_5_OVERRECORD_REMODEL_PLAN.md.

var (
	ErrRecoveryDecisionInvalid     = errors.New("การตัดสินใจ recovery ไม่ถูกต้อง")
	ErrRecoveryOverrideOutOfBounds = errors.New("ยอดคืนที่ปรับต้องอยู่ระหว่างยอดแนะนำถึง 0 (คืนน้อยกว่าได้ แต่ห้ามเกิน และห้ามเรียกเก็บ)")
)

// RecoveryDecision is the operator's per-utility choice for an over-record
// discrepancy. Refund-only: there is no charge path.
type RecoveryDecision string

const (
	RecoveryDecisionAccept   RecoveryDecision = "ACCEPT"   // refund the full deterministic recommendation
	RecoveryDecisionOverride RecoveryDecision = "OVERRIDE" // refund a smaller partial amount
	RecoveryDecisionWaive    RecoveryDecision = "WAIVE"    // no money moves
)

// IsAffected reports whether a utility is over-recorded (recorded > physical),
// i.e. whether it enters the recovery decision set at all. Derived, never
// stored (Q1.5 §0a #3). A utility with physical == recorded is unaffected and
// produces no decision.
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

// ResolveRefundAmount turns an operator decision into the final signed refund
// satang, validated against the deterministic recommendation. Override must lie
// in [recommended, 0): a smaller-magnitude refund only — never a charge, never
// larger than recommended, no escape hatch (Q1.5 §0a #2). `recommended` is the
// negative satang from RecommendRefund.
func ResolveRefundAmount(decision RecoveryDecision, recommended, override int64) (int64, error) {
	switch decision {
	case RecoveryDecisionWaive:
		return 0, nil
	case RecoveryDecisionAccept:
		return recommended, nil
	case RecoveryDecisionOverride:
		// [recommended, 0): recommended <= override < 0. More negative than
		// recommended (override < recommended) over-refunds; >= 0 is a charge.
		if override >= 0 || override < recommended {
			return 0, ErrRecoveryOverrideOutOfBounds
		}
		return override, nil
	default:
		return 0, ErrRecoveryDecisionInvalid
	}
}

// PerUtilityResolution is a resolved over-record decision for one utility,
// ready to build into an ADJUSTMENT line. AmountSatang is the final signed
// refund (negative) or 0 for waive — produced by ResolveRefundAmount.
type PerUtilityResolution struct {
	Utility      AdjustmentUtility
	Decision     RecoveryDecision
	AmountSatang int64
	Note         string
}

// RecoveryResolution is an operator decision resolving one pending meter
// recovery into a bill. Q1.5 refund-only:
//   - refund → Waive=false, Amount < 0 (over-record over-payment returned)
//   - waive  → Waive=true (Amount forced to 0)
//
// Utility names which metered utility this settles (electricity/water are
// independent). Note is required in all cases (records why); for waive it is the
// operator's reason for not refunding.
type RecoveryResolution struct {
	RecoveryReadingID uuid.UUID
	Utility           AdjustmentUtility
	Amount            int64
	Note              string
	Waive             bool
}

// BuildRecoveryAdjustmentLine constructs and validates the ADJUSTMENT line for a
// resolved recovery decision, ready to persist on billID at sortOrder. Pure: no
// I/O, no DB.
//
// This is the SINGLE construction+validation site shared by every destination
// (monthly draft, settlement draft) so reason/amount routing, the tenant-visible
// description, and ValidateAdjustment can never drift between them. The I/O around
// it (loading the recovery row, the applied-state race check, persistence, audit)
// stays with each caller because those touch each package's own dependencies.
//
// Reason routing: Waive → METER_RECOVERY_WAIVED with amount 0; otherwise
// METER_RECOVERY with the refund amount (ValidateAdjustment rejects a zero or
// positive non-waive amount — refund-only, so "forgot the amount" or an
// accidental charge surfaces as an error rather than a silent waive).
func BuildRecoveryAdjustmentLine(billID uuid.UUID, res RecoveryResolution, sourceMonth string, sortOrder int) (BillLineItem, error) {
	reason := AdjustmentReasonMeterRecovery
	amount := res.Amount
	if res.Waive {
		reason = AdjustmentReasonMeterRecoveryWaived
		amount = 0
	}
	note := res.Note
	recoveryFK := res.RecoveryReadingID

	line := BillLineItem{
		BillID:                      billID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		Description:                 buildAdjustmentDescription(amount, sourceMonth),
		Amount:                      amount,
		Quantity:                    1,
		UnitPrice:                   amount,
		SortOrder:                   sortOrder,
		AdjustmentRecoveryReadingID: &recoveryFK,
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &note,
	}
	// Per-utility discriminator (Q1.5). Set only when the caller names a utility
	// — nil until the P3 apply path populates it.
	if res.Utility != "" {
		u := res.Utility
		line.AdjustmentUtility = &u
	}
	if err := line.ValidateAdjustment(); err != nil {
		return BillLineItem{}, err
	}
	return line, nil
}
