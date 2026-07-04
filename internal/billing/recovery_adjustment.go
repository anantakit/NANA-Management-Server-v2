package billing

import "github.com/google/uuid"

// RecoveryResolution is an operator decision resolving one pending meter
// recovery into a bill. Exactly one of two shapes:
//   - charge/refund → Waive=false, Amount != 0 (signed satang; >0 charge, <0 refund)
//   - waive/no-charge → Waive=true (Amount is forced to 0)
//
// Note is required in all cases (records why); for waive it is the operator's
// reason for not charging. Q1 Recovery Decision doctrine:
// project_reading_recovery_q1_unified_decision_surface_lock.
type RecoveryResolution struct {
	RecoveryReadingID uuid.UUID
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
// METER_RECOVERY with the signed amount (ValidateAdjustment rejects a zero
// non-waive amount, so "forgot to enter the amount" surfaces as an error rather
// than a silent waive).
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
	if err := line.ValidateAdjustment(); err != nil {
		return BillLineItem{}, err
	}
	return line, nil
}
