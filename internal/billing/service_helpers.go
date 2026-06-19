package billing

import (
	"context"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/moveout"

	"github.com/google/uuid"
)

// --- Deposit settlement helpers ---

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
	AppliedAmount    int64 // min(AvailableToApply, grossCharges) — offsets charges
	RefundAmount     int64 // AvailableToApply - AppliedAmount (>0 only if deposit returnable)
	AmountDue        int64 // grossCharges - AppliedAmount (>0 only if charges exceed available)
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

	if !returnable {
		// Early exit: entire deposit forfeited, not applied to charges.
		// Tenant pays full charges.
		s.ForfeitedAmount = depositAmount
		s.AvailableToApply = 0
		s.AmountDue = grossCharges
		return s
	}

	// Returnable: full deposit available to offset charges
	s.AvailableToApply = depositAmount

	// Apply deposit to charges
	s.AppliedAmount = s.AvailableToApply
	if grossCharges < s.AppliedAmount {
		s.AppliedAmount = grossCharges
	}
	if s.AppliedAmount < 0 {
		s.AppliedAmount = 0
	}

	// Remaining deposit → refunded to tenant
	s.RefundAmount = s.AvailableToApply - s.AppliedAmount

	// Amount tenant still owes
	if grossCharges > s.AppliedAmount {
		s.AmountDue = grossCharges - s.AppliedAmount
	}

	return s
}

// computeDepositSettlementFromBill delegates to Bill.DepositBreakdown() as single source of truth.
// Use this for any bill that may have DepositApp or Overrides set.
func computeDepositSettlementFromBill(bill *Bill) DepositSettlementState {
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
	netAmount := int64(0)
	if ds.AmountDue > 0 {
		netAmount = ds.AmountDue // positive = tenant pays
	} else if ds.RefundAmount > 0 {
		netAmount = -ds.RefundAmount // negative = refund to tenant
	}
	return &moveout.SettlementBillResult{
		BillID:      billID,
		NetAmount:   netAmount,
		DepositUsed: ds.AppliedAmount,
	}
}

// --- Billing month helpers ---

// ToMonth formats a time.Time as "YYYY-MM". Exported so the upcoming
// settlement sub-package can share the same billing-month projection as
// billing root + monthly. Pre-extraction commit 1 (2026-06-19) — see
// project_billing_extraction_plan_locked.md.
func ToMonth(t time.Time) string {
	return t.Format("2006-01")
}

// PreviousMonth returns the YYYY-MM string for the calendar month before
// `month`. Exported alongside ToMonth for the settlement extraction.
func PreviousMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

func advanceMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}

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
func effectiveMoveOutDate(moveOutDate time.Time, mode SettlementRentMode) time.Time {
	if mode == RentModeFullMonthKeepDeposit {
		return endOfMonth(moveOutDate)
	}
	return moveOutDate
}

// --- Payment routing helpers ---

// tryResolvePaymentDestination resolves the payment destination for a room.
// Returns nil on error or when no rules are configured — never blocks bill creation.
func (s *billingService) tryResolvePaymentDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) *apartment.PaymentDestinationInfo {
	if s.paymentRouting == nil {
		return nil
	}
	dest, err := s.paymentRouting.ResolveDestination(ctx, apartmentID, roomNumber)
	if err != nil {
		slog.Warn("payment routing resolve failed, bill will have null destination",
			"apartment_id", apartmentID, "room_number", roomNumber, "error", err)
		return nil
	}
	return dest
}

// ApplyPaymentSnapshot sets the three payment snapshot fields on a bill.
// No-op when dest is nil (no rules configured). Exported so the upcoming
// settlement sub-package can share the same payment-destination snapshot
// logic as billing root + monthly. Pre-extraction commit 1 (2026-06-19).
func ApplyPaymentSnapshot(bill *Bill, dest *apartment.PaymentDestinationInfo) {
	if dest == nil {
		return
	}
	bn := dest.BankName
	an := dest.AccountNumber
	acn := dest.AccountName
	bill.PaymentBankName = &bn
	bill.PaymentAccountNumber = &an
	bill.PaymentAccountName = &acn
}
