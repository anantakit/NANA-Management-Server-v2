package monthly

import "nana/internal/billing"

// Compile-time guarantees that billing.MonthlyAdapter satisfies every port
// monthly declares against the billing root. A rename or signature drift
// on either side surfaces here at build time instead of at DI assembly time.
//
// The check lives in the CONSUMER package (monthly) rather than alongside
// the adapter (PROVIDER = billing) because Option A allows monthly to import
// billing for shared domain types. That import direction is already used in
// port.go. Putting the check inside billing would require billing to import
// monthly, which would create a cycle. See project_billing_extraction_plan_locked.md.
//
// Cross-feature meter + move-out ports (MeterReadingSource, MoveOutSource)
// are satisfied by the provider repos directly (meterreading.MeterReadingRepository,
// moveout.MoveOutRepository) — no adapter struct lives in billing for those.
// DI in cmd/main.go wires them via duck typing; mismatches surface at the
// NewService call site.
var (
	_ BillStore  = (*billing.MonthlyAdapter)(nil)
	_ AuditStore = (*billing.MonthlyAdapter)(nil)
	_ BatchStore = (*billing.MonthlyAdapter)(nil)
)
