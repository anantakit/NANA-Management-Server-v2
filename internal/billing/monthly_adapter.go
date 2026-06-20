package billing

import (
	"context"

	"github.com/google/uuid"
)

// MonthlyAdapter satisfies the monthly workflow's bill+audit ports
// (monthly.BillStore, monthly.AuditStore) with a deliberately narrow
// dependency surface: only BillingRepository + BillAuditRepository.
//
// Mirrors PaymentAdapter and ReconciliationAdapter — billing-as-provider
// always exposes consumer-defined ports via a narrow adapter struct, never
// by widening the BillingService interface with consumer-shaped methods.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE MODIFYING THIS FILE
// ===========================================================================
//
//  1. Every method MUST remain PURE DELEGATION — a single passthrough to
//     repo.X / audit.X or a call into a package-level shared primitive
//     (finalizeBillInTx, recordAudit). If any method here ever grows past
//     one line of meaningful body, that is a signal the parent (billing
//     root) is missing a primitive — extract it there first.
//
//  2. DO NOT add business logic to MonthlyAdapter. Not validation, not
//     branching, not state computation, not error classification, not
//     workflow orchestration. Those belong in monthly.service (workflow)
//     or in billing.* (domain primitives shared across workflows). The
//     adapter is the boundary, not a decision point.
//
//  3. Batch-table persistence is NOT this adapter's concern — monthly owns
//     bill_generation_batches + bill_generation_batch_items via
//     monthly.BatchRepository (internal/billing/monthly/repository.go).
//     Do not re-introduce batch CRUD here just because the table is
//     "billing-related"; the persistence boundary is intentional.
// ===========================================================================
//
// All methods that mutate state must be called with a txCtx from the
// caller's RunInTx so writes (and the audit row) join the same transaction.
type MonthlyAdapter struct {
	repo  BillingRepository
	audit BillAuditRepository
}

func NewMonthlyAdapter(repo BillingRepository, audit BillAuditRepository) *MonthlyAdapter {
	return &MonthlyAdapter{repo: repo, audit: audit}
}

// --- monthly.BillStore: queries ---

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

// --- monthly.BillStore: commands ---

func (a *MonthlyAdapter) CreateBill(ctx context.Context, b *Bill) error {
	return a.repo.Create(ctx, b)
}

func (a *MonthlyAdapter) UpdateBill(ctx context.Context, b *Bill) error {
	return a.repo.Update(ctx, b)
}

func (a *MonthlyAdapter) FinalizeBillInTx(txCtx context.Context, id uuid.UUID, actor *uuid.UUID) error {
	return finalizeBillInTx(txCtx, a.repo, a.audit, id, actor)
}

// --- monthly.AuditStore ---

func (a *MonthlyAdapter) EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return a.audit.EditedBillIDs(ctx, billIDs)
}

func (a *MonthlyAdapter) RecordAudit(ctx context.Context, billID uuid.UUID, action BillAuditAction, actor *uuid.UUID, payload any) error {
	return recordAudit(ctx, a.audit, billID, action, actor, payload)
}
