// Package settlement owns the settlement bill workflow inside the billing
// domain.
//
// ===========================================================================
// SCOPE BOUNDARY — READ BEFORE ADDING ANYTHING TO THIS PACKAGE
// ===========================================================================
//
// This package is the SETTLEMENT BILLING WORKFLOW only. It is not, and must
// never become, a general document-replacement engine, a "place for any
// correction-shaped flow", or a settlement+correction+adjustments catch-all.
//
// The settlement workflow scope is precisely:
//   - Preview / Create DRAFT / Update DRAFT (manual + overrides + deposit app)
//   - Generate from move-out / Regenerate / Finalize / Void
//   - Settlement correction (void+recreate on a FINALIZED settlement bill)
//
// Two correction flavors are intentionally separate:
//   - MONTHLY correction lives at billing root (CorrectBill in service_correct.go)
//   - SETTLEMENT correction lives here (it's a settlement-workflow step that
//     happens to be a document replacement).
//
// They are NOT merged into a billing/correction/ catch-all. The folder name
// represents the product workflow that owns the document, not the technique
// (correction) used to replace it. Locked 2026-06-19.
// ===========================================================================
//
// CURRENT STAGE OWNERSHIP (W4 Commit 2 scaffold, 2026-06-19):
//
//   - settlement currently exposes only the package scaffold: ports, the
//     Service struct + constructor, the Handler scaffold, and the
//     SettlementAdapter compile-time check. NO workflow methods or DTOs
//     are present in this package yet — they migrate from billing root in
//     commits 3-6 of the W4 plan.
//
//   - billing root STILL owns: the Bill model + bill_line_items, all
//     settlement column types (SettlementRentMode, DepositApplication,
//     BillTypeSettlement, SettlementOptions), all Bill.* domain methods
//     (Void, MarkAbsorbedBySettlement, RestoreFromAbsorbed, DepositBreakdown,
//     CanCorrect, etc.), the BillingRepository + BillAuditRepository, plus
//     all settlement-touching service code (service_settlement.go,
//     service_settlement_plan.go, service_moveout_ports.go,
//     settlement-flavored helpers in service_helpers.go). These migrate
//     out incrementally; the SettlementAdapter exists so settlement reads
//     them through ports rather than direct field access.
//
//   - settlement is NOT a generic correction package and not a generic
//     deposit-math package. Its scope boundary above is load-bearing.
//
//   - A FUTURE settlement-specific repository split is FUTURE INVESTIGATION
//     ONLY (project_billing_extraction_backlog.md §2). Evidence is weaker
//     than monthly's batch-table case because settlement bills share the
//     bills table with monthly. SettlementAdapter exists as an EXTRACTION
//     SEAM, not as an architectural seam telegraphing a future split.
//
// Staged ownership (intentional, NOT omissions — to be migrated later):
//   - Workflow methods (PreviewSettlement, CreateSettlementBill, etc.) move
//     into this package in commit 4 of W4. Until then they remain on
//     billing.BillingService.
//   - Moveout-port satisfaction (GenerateSettlement, CorrectSettlement,
//     FinalizeSettlement, VoidSettlement, PreviewSettlementForNotice,
//     RegenerateSettlement) moves into this package in commit 5 of W4.
//     Until then billing.BillingService still satisfies moveout's port
//     interfaces; the cmd/main.go wiring still passes billService to
//     moveout.NewMoveOutService.
//   - DTOs + handler routes move in commit 6 of W4.
//
// Import direction (Option A, locked 2026-06-19, mirrors monthly):
//   - settlement may import billing for shared domain types: *billing.Bill,
//     BillLineItem, BillType, BillAuditAction, LineItemSource, status / type
//     / audit-action constants, error sentinels.
//   - settlement may import moveout for the port shapes it eventually
//     satisfies (SettlementBillResult, RentMode, CorrectSettlementInput,
//     SettlementPreviewResult) — moveout already defines these in its own
//     port.go to avoid cycles with billing today.
//   - settlement MUST NOT import billing.BillingService or depend on the
//     full root service surface — only the narrow ports declared in port.go.
//   - billing MUST NOT import settlement except where strictly required for
//     wiring / adapter compile-checks (settlement/adapter_check.go imports
//     billing, not the other way around).
//   - moveout MUST NOT import settlement. Its BillingCommander +
//     BillingQuerier ports stay consumer-defined; settlement.Service will
//     satisfy them structurally at commit 5, swapped in via cmd/main.go DI.
//
// See project_billing_extraction_plan_locked.md for the full W4 plan and
// project_billing_extraction_backlog.md for post-W4 architecture review.
package settlement
