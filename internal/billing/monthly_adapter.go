package billing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TODO(architecture): MonthlyAdapter is INTENTIONALLY WIDE because monthly
// repository ownership is deferred. When monthly owns batch persistence:
//   - Remove the entire BatchStore surface (12 methods).
//   - Remove the 4 BillStore methods that only batch uses
//     (ListByBatchID, ListMonthlyByApartmentMonth,
//      FindActiveContractsByApartmentID, FindExistingByContractsAndMonth).
//   - MonthlyAdapter shrinks from 23 methods → ~7 (bill core + audit).
//   - Batch entity types + BatchListParams move from billing root into monthly.
// Tracked as Architecture Backlog. See project_billing_extraction_backlog.md.

// MonthlyAdapter satisfies the monthly workflow's six ports
// (monthly.BillReader, monthly.BillCommander, monthly.AuditReader,
// monthly.AuditEmitter, monthly.BatchReader, monthly.BatchCommander) with
// a deliberately narrow dependency surface: only BillingRepository +
// BillAuditRepository.
//
// Mirrors PaymentAdapter and ReconciliationAdapter — billing-as-provider
// always exposes consumer-defined ports via a narrow adapter struct, never
// by widening the BillingService interface with consumer-shaped methods.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE MODIFYING THIS FILE
// ===========================================================================
//
//  1. This adapter is INTENTIONALLY WIDE in this stage (18 methods, 6 ports).
//     The width exists because the repository split is DEFERRED: batch SQL
//     still lives on billing.BillingRepository, so monthly must reach every
//     batch-table method through this adapter. The width is a load-bearing
//     consequence of "no repository split in this commit" (Option B-extended,
//     locked 2026-06-19), not a design smell.
//
//  2. Every method MUST remain PURE DELEGATION — a single passthrough to
//     repo.X / audit.X or a call into a package-level shared primitive
//     (finalizeBillInTx, recordAudit). If any method here ever grows past
//     one line of meaningful body, that is a signal the parent (billing
//     root) is missing a primitive — extract it there first.
//
//  3. DO NOT add business logic to MonthlyAdapter. Not validation, not
//     branching, not state computation, not error classification, not
//     workflow orchestration. Those belong in monthly.service (workflow)
//     or in billing.* (domain primitives shared across workflows). The
//     adapter is the boundary, not a decision point.
//
//  4. When the repository split eventually lands, this adapter shrinks —
//     methods whose SQL moved into monthly's own repo get deleted from
//     here. Until then, growth is permitted ONLY for new pure-delegation
//     entries needed by monthly; never for synthesized behavior.
// ===========================================================================
//
// All methods that mutate state must be called with a txCtx from the
// caller's RunInTx so writes (and the audit row) join the same transaction.
//
// Commit 2 of the monthly extraction (2026-06-19): adapter widens from 6
// methods to 18 to cover the batch workflow's needs. Batch entity types
// (BillGenerationBatch, BillGenerationBatchItem, BatchListParams, etc.)
// remain in billing root because the repository that owns their SQL also
// stays here. See project_billing_extraction_plan_locked.md.
type MonthlyAdapter struct {
	repo  BillingRepository
	audit BillAuditRepository
}

func NewMonthlyAdapter(repo BillingRepository, audit BillAuditRepository) *MonthlyAdapter {
	return &MonthlyAdapter{repo: repo, audit: audit}
}

// --- monthly.BillReader ---

func (a *MonthlyAdapter) FindByID(ctx context.Context, id uuid.UUID) (*Bill, error) {
	return a.repo.FindByID(ctx, id)
}

func (a *MonthlyAdapter) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error) {
	return a.repo.FindByContractAndMonth(ctx, contractID, billingMonth, billType)
}

func (a *MonthlyAdapter) ListByBatchID(ctx context.Context, batchID uuid.UUID) ([]Bill, error) {
	return a.repo.ListBillsByBatchID(ctx, batchID)
}

func (a *MonthlyAdapter) ListMonthlyByApartmentMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string) ([]Bill, error) {
	return a.repo.ListMonthlyBillsByApartmentMonth(ctx, apartmentID, billingMonth)
}

func (a *MonthlyAdapter) FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error) {
	return a.repo.FindActiveContractsByApartmentID(ctx, apartmentID)
}

func (a *MonthlyAdapter) FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, billingMonth string) (map[uuid.UUID]*Bill, error) {
	return a.repo.FindExistingByContractsAndMonth(ctx, contractIDs, billingMonth)
}

// --- monthly.BillCommander ---

func (a *MonthlyAdapter) CreateBill(ctx context.Context, b *Bill) error {
	return a.repo.Create(ctx, b)
}

func (a *MonthlyAdapter) UpdateBill(ctx context.Context, b *Bill) error {
	return a.repo.Update(ctx, b)
}

func (a *MonthlyAdapter) FinalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error {
	return finalizeBillInTx(txCtx, a.repo, a.audit, id, actor)
}

// --- monthly.AuditReader ---

func (a *MonthlyAdapter) EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return a.audit.EditedBillIDs(ctx, billIDs)
}

// --- monthly.AuditEmitter ---

func (a *MonthlyAdapter) RecordAudit(ctx context.Context, billID uuid.UUID, action BillAuditAction, actor *uuid.UUID, payload any) error {
	return recordAudit(ctx, a.audit, billID, action, actor, payload)
}

// --- monthly.BatchReader ---

func (a *MonthlyAdapter) FindBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error) {
	return a.repo.FindBatchByID(ctx, id)
}

func (a *MonthlyAdapter) FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]BatchItemWithTenant, error) {
	return a.repo.FindBatchItemsByBatchID(ctx, batchID)
}

func (a *MonthlyAdapter) FindBatchItemByID(ctx context.Context, itemID uuid.UUID) (*BillGenerationBatchItem, error) {
	return a.repo.FindBatchItemByID(ctx, itemID)
}

func (a *MonthlyAdapter) FindBatchItemByIDWithTenant(ctx context.Context, itemID uuid.UUID) (*BatchItemWithTenant, error) {
	return a.repo.FindBatchItemByIDWithTenant(ctx, itemID)
}

func (a *MonthlyAdapter) ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error) {
	return a.repo.ListBatches(ctx, params)
}

func (a *MonthlyAdapter) ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]BillGenerationBatchItem, error) {
	return a.repo.ListCommitPendingItems(ctx, batchID)
}

// --- monthly.BatchCommander ---

func (a *MonthlyAdapter) CreateBatch(ctx context.Context, batch *BillGenerationBatch, items []BillGenerationBatchItem) error {
	return a.repo.CreateBatch(ctx, batch, items)
}

func (a *MonthlyAdapter) LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*BillGenerationBatch, error) {
	return a.repo.LockBatchForCommit(ctx, batchID)
}

func (a *MonthlyAdapter) UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error {
	return a.repo.UpdateBatchItemCommitError(ctx, itemID, reasonText)
}

func (a *MonthlyAdapter) UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status CommitStatus, committedAt *time.Time) error {
	return a.repo.UpdateBatchCommitStatus(ctx, batchID, status, committedAt)
}

func (a *MonthlyAdapter) UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error {
	return a.repo.UpdateBatchItemCommitted(ctx, itemID, billID)
}

func (a *MonthlyAdapter) UpdateBatchItemPlan(ctx context.Context, itemID uuid.UUID, resultType ResultType, reasonCode, reasonText string, billID *uuid.UUID, snapshot ComputedSnapshot) error {
	return a.repo.UpdateBatchItemPlan(ctx, itemID, resultType, reasonCode, reasonText, billID, snapshot)
}
