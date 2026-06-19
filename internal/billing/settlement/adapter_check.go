package settlement

import "nana/internal/billing"

// Compile-time guarantees that billing.SettlementAdapter satisfies every
// billing-root port that settlement declares. A rename or signature drift
// on either side surfaces here at build time instead of at DI assembly time.
//
// The check lives in the CONSUMER package (settlement) rather than alongside
// the adapter (PROVIDER = billing) because settlement may import billing for
// shared domain types per Option A (locked 2026-06-19). That import direction
// is already used in port.go. Putting the check inside billing would require
// billing to import settlement, which would create a cycle. Mirrors monthly's
// adapter_check.go pattern.
//
// Cross-feature Source ports (Contract / MeterReading / BillingConfig /
// MoveOut / PaymentRouting) are satisfied by provider repos/services
// directly — no adapter struct lives in any provider package for those. DI
// in cmd/main.go wires them via structural typing; mismatches surface at the
// NewService call site rather than here.
//
// Moveout port compile-checks (moveout.BillingCommander +
// moveout.BillingQuerier on *settlement.Service) DO NOT land in commit 2 —
// Service has no methods yet. They land in commit 5 of the W4 plan when the
// moveout-port methods migrate from billing root.
var (
	_ BillStore  = (*billing.SettlementAdapter)(nil)
	_ AuditStore = (*billing.SettlementAdapter)(nil)
)
