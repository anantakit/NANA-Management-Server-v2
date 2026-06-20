package billing

import (
	"context"
	"log/slog"
	"time"

	"nana/internal/apartment"

	"github.com/google/uuid"
)

// --- Billing month helpers ---

// ToMonth formats a time.Time as "YYYY-MM". Exported so the settlement
// sub-package can share the same billing-month projection as billing root
// + monthly. Pre-extraction commit 1 (2026-06-19).
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

// advanceMonth returns the YYYY-MM string for the calendar month after
// `month`. Used by ComputeMonthlyBillSnapshot to derive the rent line's
// description (monthly bill collects rent for M+1). Package-private —
// monthly extraction uses ComputeMonthlyBillSnapshot directly rather than
// reaching into this helper.
func advanceMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}

// --- Payment routing helpers ---
//
// tryResolvePaymentDestination + ApplyPaymentSnapshot remain in billing
// root after the W4 settlement extraction (commit 3, 2026-06-19) because
// CreateMonthlyBill (W1) and correctMonthlyBillInTx (monthly correction)
// still use them. Settlement has its own private tryResolvePaymentDestination
// over its PaymentRoutingSource port — intentional small duplicate per the
// W4 audit doctrine (settlement doesn't reach into billing's private helper).
// ApplyPaymentSnapshot stays as the single source of truth for the snapshot
// field assignment because both billing root and settlement call it.

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
// No-op when dest is nil (no rules configured). Exported so the settlement
// sub-package can share the same payment-destination snapshot logic as
// billing root. Pre-extraction commit 1 (2026-06-19).
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
