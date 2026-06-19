// Package monthly owns the monthly bill workflow inside the billing domain.
//
// Scope (end-state vision): the entire monthly bill lifecycle owned in one
// place — single monthly bill creation, the monthly billing-cycle batch flow
// (preflight → batch → commit → finalize → replan), monthly draft editing,
// and monthly correction.
//
// Scope (this commit, Commit 1 of the extraction): boundary surface only —
// the consumer-defined ports monthly needs from the billing root, the
// provider adapter (billing.MonthlyAdapter) that satisfies them, and the
// extracted package-level primitives (finalizeBillInTx, recordAudit) those
// ports delegate into. No moved code, no DI wiring, no service yet.
//
// Scope (Commit 2, planned next): move the W2 batch mechanics into this
// package — preflight, batch-create, commit, finalize-all, finalize-all-by-month,
// replan, plus the BillGenerationBatch* entities and their CRUD. After commit
// 2 monthly owns the monthly billing-cycle workflow end-to-end.
//
// Staged ownership (intentional, NOT omissions — to be migrated in future PRs):
//   - Single one-off CreateMonthlyBill remains at billing root post-Commit-2.
//     Migration requires reworking the reconciliation adapter to avoid a
//     billing→monthly import cycle.
//   - Monthly draft editing (UpdateMonthlyDraft) remains at billing root
//     post-Commit-2. Folds into this package in a follow-up PR.
//   - Monthly correction (CorrectBill, monthly-only) remains at billing root
//     post-Commit-2. Folds into this package in a follow-up PR, likely
//     paired with settlement correction's own extraction.
//
// Naming rule: this folder represents the monthly product workflow, not a
// generic batch engine. Other batch-shaped workflows (delivery, collection,
// LINE sending) MUST get their own workflow folder — never placed here.
//
// Import direction (Option A, locked 2026-06-19):
//   - monthly may import billing for shared domain types: *billing.Bill,
//     BillLineItem, status / type / audit-action constants.
//   - monthly MUST NOT import billing.BillingService or depend on the full
//     root service surface — only the narrow ports declared in port.go.
//   - billing MUST NOT import monthly except where strictly required for
//     wiring / adapter compile-checks.
//
// See project_billing_extraction_plan_locked.md for the full plan.
package monthly
