// Package monthly owns the monthly bill workflow inside the billing domain.
//
// ===========================================================================
// SCOPE BOUNDARY — READ BEFORE ADDING ANYTHING TO THIS PACKAGE
// ===========================================================================
//
// This package is the MONTHLY BILLING WORKFLOW only. It is not, and must
// never become, a general batch engine, a generic job runner, or a "place
// for anything that happens in bulk over bills."
//
// Other batch-shaped workflows MUST get their own workflow package:
//   - delivery batch       → billdelivery/   (or a delivery/ sub-package)
//   - collection batch     → its own package
//   - LINE-sending batch   → its own package
//   - statement export     → its own package
//
// The fact that two workflows both "process N rows" is not a reason to put
// them in the same package. The product-workflow naming rule
// (project_billing_extraction_plan_locked.md) exists precisely to prevent
// the recurrence of the original mega-feature problem under a new label.
//
// If you are tempted to add something here, ask: "Is this a step in the
// monthly billing cycle (preflight → batch → commit → finalize → replan)?"
// If no, it belongs elsewhere.
// ===========================================================================
//
// CURRENT STAGE OWNERSHIP (Commit 2b, 2026-06-19):
//
//   - monthly currently owns the MONTHLY BILLING WORKFLOW ORCHESTRATION.
//     The 9 workflow operations (PreflightMonthly, BatchCreateMonthlyBills,
//     CommitBatch, BatchFinalizeAll, FinalizeAllByMonth, GetBatchByID,
//     GetBatchItems, ListBatches, RePlanBatchItem) plus their handler +
//     DTOs + planning logic live here.
//
//   - billing root STILL owns batch persistence types + repository during
//     this stage: BillGenerationBatch, BillGenerationBatchItem,
//     BatchItemWithTenant, BatchStatus, CommitStatus, ResultType,
//     Reason* constants, CommitBatchResult, BatchListParams, plus all the
//     SQL methods on BillingRepository that mutate the batch tables.
//     Monthly reads/writes those via monthly.BillStore / AuditStore /
//     BatchStore ports, satisfied by billing.MonthlyAdapter.
//
//   - monthly is NOT a generic batch package. It is a workflow package
//     that happens to use batch mechanics today. The scope boundary above
//     is load-bearing — see it for the explicit reject list.
//
//   - A FUTURE PR may move repository + model ownership into this package
//     ONLY WHEN the import cycles caused by that move can be resolved
//     safely. Today, batch types stay at billing root because moving them
//     would force billing.BillingRepository's batch-method signatures to
//     return monthly.* types, which would create a billing → monthly
//     import cycle. That cycle goes away only after the broader monthly
//     migration completes (single CreateMonthlyBill, monthly draft, monthly
//     correction) and the reconciliation adapter has been re-routed.
//
// Staged ownership (intentional, NOT omissions — to be migrated later):
//   - Single one-off CreateMonthlyBill remains at billing root.
//     Migration requires reworking the reconciliation adapter to avoid a
//     billing→monthly import cycle.
//   - Monthly draft editing (UpdateMonthlyDraft) remains at billing root.
//   - Monthly correction (CorrectBill, monthly-only) remains at billing root.
//
// Import direction (Option A, locked 2026-06-19):
//   - monthly may import billing for shared domain types: *billing.Bill,
//     BillLineItem, ContractWithRoom, BillGenerationBatch + batch types,
//     status / type / audit-action constants, error sentinels.
//   - monthly MUST NOT import billing.BillingService or depend on the full
//     root service surface — only the narrow ports declared in port.go.
//   - billing MUST NOT import monthly except where strictly required for
//     wiring / adapter compile-checks.
//
// See project_billing_extraction_plan_locked.md for the full plan.
package monthly
