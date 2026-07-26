package settlement

import (
	"time"

	"nana/internal/billing"
	"nana/internal/moveout"

	"github.com/google/uuid"
)

// --- Settlement-only computational helpers ---
//
// Migrated from billing/service_helpers.go in W4 commit 3 (2026-06-19).
// These helpers are settlement-domain math (deposit qualification, deposit
// breakdown, day arithmetic for prorating, effective-move-out-date
// resolution) and stay package-private to settlement — they are not part
// of the workflow's external port surface.

// addMonthsClamped adds N months (positive) to a date, clamping to end-of-month.
// e.g. Jan 31 + 1 month = Feb 28 (not Mar 3 like Go's AddDate).
// Only tested/used with months >= 1. Caller must guard months <= 0.
func addMonthsClamped(start time.Time, months int) time.Time {
	year, month, day := start.Date()
	loc := start.Location()

	totalMonths := int(month) - 1 + months
	targetYear := year + totalMonths/12
	targetMonth := time.Month(totalMonths%12 + 1)
	if totalMonths < 0 && totalMonths%12 != 0 {
		targetYear--
		targetMonth = time.Month(totalMonths%12 + 13)
	}

	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}

	return time.Date(targetYear, targetMonth, day,
		start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), loc)
}

// isDepositReturnable checks if the tenant stayed at least MinMonths from start date.
// Uses calendar-month clamp: Jan 31 + 1 month = Feb 28 (not Mar 3).
// Edge cases (e.g. 1 day short) are handled by admin override at the UI layer.
func isDepositReturnable(startDate time.Time, moveOutDate time.Time, minMonths int) bool {
	if moveOutDate.Before(startDate) {
		return false
	}
	if minMonths <= 0 {
		return true
	}
	eligibleAt := addMonthsClamped(startDate, minMonths)
	return !moveOutDate.Before(eligibleAt)
}

// DepositSettlementState holds the fully computed deposit breakdown for a settlement.
// Each field has a single, unambiguous meaning — no mixing of forfeiture and application.
type DepositSettlementState struct {
	OriginalAmount   int64 // deposit held by landlord (contract.DepositAmount)
	ForfeitedAmount  int64 // deposit lost due to early exit (0 if eligible)
	AvailableToApply int64 // OriginalAmount - ForfeitedAmount
	AppliedAmount    int64 // min(AvailableToApply, positiveCharges) — offsets charges
	RefundAmount     int64 // (AvailableToApply - AppliedAmount) + surplus recovery credit
	AmountDue        int64 // positiveCharges - AppliedAmount (>0 only if charges exceed available)
}

// computeDepositSettlement computes the full deposit state for a settlement.
//
// Rules:
//   - Returnable (min months met): full deposit available to offset charges, excess refunded
//   - Not returnable (early exit): deposit entirely forfeited — not applied to charges,
//     tenant must pay full amount
//
// DEPRECATED: Use computeDepositSettlementFromBill for new code.
// Kept for commitSettlementPlan where the bill is not yet persisted.
func computeDepositSettlement(depositAmount int64, grossCharges int64, returnable bool) DepositSettlementState {
	s := DepositSettlementState{
		OriginalAmount: depositAmount,
	}

	// A negative grossCharges means recovery credits exceeded this cycle's
	// charges. That surplus is the tenant's own overpayment and must be
	// refunded on top of any deposit return — deposit application clamps at
	// zero charges, but the surplus credit is never dropped.
	positiveCharges := grossCharges
	surplusCredit := int64(0)
	if positiveCharges < 0 {
		surplusCredit = -positiveCharges
		positiveCharges = 0
	}

	if !returnable {
		// Entire deposit forfeited, not applied to charges. Tenant pays full
		// charges but still receives any surplus recovery credit.
		s.ForfeitedAmount = depositAmount
		s.AvailableToApply = 0
		s.RefundAmount = surplusCredit
		s.AmountDue = positiveCharges
		return s
	}

	// Returnable: full deposit available to offset charges
	s.AvailableToApply = depositAmount

	// Apply deposit to charges (clamped at zero charges)
	s.AppliedAmount = s.AvailableToApply
	if positiveCharges < s.AppliedAmount {
		s.AppliedAmount = positiveCharges
	}

	// Remaining deposit + surplus credit → refunded to tenant
	s.RefundAmount = s.AvailableToApply - s.AppliedAmount + surplusCredit

	// Amount tenant still owes
	s.AmountDue = positiveCharges - s.AppliedAmount

	return s
}

// computeDepositSettlementFromBill delegates to Bill.DepositBreakdown() as single source of truth.
// Use this for any bill that may have DepositApp or Overrides set.
func computeDepositSettlementFromBill(bill *billing.Bill) DepositSettlementState {
	bd := bill.DepositBreakdown()

	// ForfeitedAmount = deposit lost due to contract terms (DepositForfeited flag).
	// WithheldAmount includes both forfeited AND admin-chosen NONE — only map forfeited here.
	var forfeited int64
	if bill.DepositForfeited {
		forfeited = bill.DepositAmount
	}

	// AvailableToApply = deposit minus forfeiture (what can be offset against charges).
	available := bill.DepositAmount - forfeited

	return DepositSettlementState{
		OriginalAmount:   bd.OriginalAmount,
		ForfeitedAmount:  forfeited,
		AvailableToApply: available,
		AppliedAmount:    bd.AppliedAmount,
		RefundAmount:     bd.RefundAmount,
		AmountDue:        bd.AmountDue,
	}
}

// toSettlementResult converts deposit state into the moveout result DTO.
func toSettlementResult(billID uuid.UUID, ds DepositSettlementState) *moveout.SettlementBillResult {
	// NetAmount convention is the inverse of Bill.DepositBalance:
	//   positive = tenant still owes, negative = refund to tenant.
	// Derive it as AmountDue - RefundAmount so it stays reconciled with
	// DepositBalance (= RefundAmount - AmountDue) for every deposit mode,
	// including negative totals where a surplus recovery credit is refunded.
	return &moveout.SettlementBillResult{
		BillID:      billID,
		NetAmount:   ds.AmountDue - ds.RefundAmount,
		DepositUsed: ds.AppliedAmount,
	}
}

// --- Day arithmetic helpers ---

func daysInMonth(t time.Time) int {
	y, m, _ := t.Date()
	return time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// endOfMonth returns the last day of the month for the given date.
func endOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	lastDay := time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
	return time.Date(y, m, lastDay, 0, 0, 0, 0, t.Location())
}

// effectiveMoveOutDate returns the date used for deposit qualification.
// PRORATED uses the actual move-out date; FULL_MONTH_KEEP_DEPOSIT uses
// end-of-month so the tenant may reach MinMonths.
func effectiveMoveOutDate(moveOutDate time.Time, mode billing.SettlementRentMode) time.Time {
	if mode == billing.RentModeFullMonthKeepDeposit {
		return endOfMonth(moveOutDate)
	}
	return moveOutDate
}
