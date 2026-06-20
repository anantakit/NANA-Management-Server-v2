package settlement

import (
	"nana/internal/billing"
	"nana/internal/moveout"
)

// Compile-time guarantees pinning the cross-package wiring at the type
// level. A rename or signature drift on either side surfaces here at
// build time instead of at DI assembly time.
//
// Two groups:
//
//  1. billing.SettlementAdapter satisfies settlement's billing-root ports
//     (BillStore, AuditStore). These checks live in the CONSUMER package
//     (settlement) rather than alongside the adapter (PROVIDER = billing)
//     because settlement imports billing for shared domain types per
//     Option A (locked 2026-06-19) — that import direction is already
//     used in port.go. Putting the check inside billing would require
//     billing to import settlement, which would create a cycle. Mirrors
//     monthly's adapter_check.go pattern.
//
//  2. *settlement.Service satisfies moveout.BillingCommander +
//     moveout.BillingQuerier. Added in W4 commit 3 once the 5 moveout
//     port methods (GenerateSettlement, RegenerateSettlement,
//     FinalizeSettlement, VoidSettlement, CorrectSettlement) and the
//     1 querier method (PreviewSettlementForNotice) landed on the
//     concrete *Service. cmd/main.go wires settlementService for both
//     ports — this check is the type-system guarantee that the wiring
//     stays valid as either side evolves.
//
// Cross-feature Source ports (Contract / MeterReading / BillingConfig /
// MoveOut / PaymentRouting) are satisfied by provider repos/services
// directly — no adapter struct lives in any provider package for those.
// DI in cmd/main.go wires them via structural typing; mismatches surface
// at the NewService call site rather than here.
var (
	_ BillStore                = (*billing.SettlementAdapter)(nil)
	_ AuditStore               = (*billing.SettlementAdapter)(nil)
	_ moveout.BillingCommander = (*Service)(nil)
	_ moveout.BillingQuerier   = (*Service)(nil)
)
