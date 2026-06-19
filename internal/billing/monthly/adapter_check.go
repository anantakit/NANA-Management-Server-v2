package monthly

import "nana/internal/billing"

// Compile-time guarantees that billing.MonthlyAdapter satisfies every port
// the monthly workflow declares. A rename or signature drift on either side
// surfaces here at build time instead of at DI assembly time.
//
// The check lives in the CONSUMER package (monthly) rather than alongside
// the adapter (PROVIDER = billing) because Option A allows monthly to import
// billing for shared domain types — that import direction is already in
// port.go. Putting the check inside billing would require billing to import
// monthly, which would create a cycle. See project_billing_extraction_plan_locked.md.
var (
	_ BillReader    = (*billing.MonthlyAdapter)(nil)
	_ BillCommander = (*billing.MonthlyAdapter)(nil)
	_ AuditEmitter  = (*billing.MonthlyAdapter)(nil)
)
