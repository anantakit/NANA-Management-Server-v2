package billing

import (
	"context"

	"github.com/google/uuid"
)

// MonthlyAdapter satisfies the monthly workflow's three ports
// (monthly.BillReader, monthly.BillCommander, monthly.AuditEmitter) with a
// deliberately narrow dependency surface: only BillingRepository +
// BillAuditRepository.
//
// Mirrors PaymentAdapter and ReconciliationAdapter — billing-as-provider
// always exposes consumer-defined ports via a narrow adapter struct, never
// by widening the BillingService interface with consumer-shaped methods.
//
// Every method here is PURE DELEGATION: either a direct repository
// passthrough or a call into the package-level shared primitives
// (finalizeBillInTx, recordAudit). No business logic, no transaction
// orchestration, no audit marshaling lives in this file. If a method
// here ever needs more than one line, that's a signal the parent
// (billing root) is missing a primitive — extract it there first.
//
// All methods that mutate state must be called with a txCtx from the
// caller's RunInTx so writes (and the audit row) join the same transaction.
//
// Commit 1 of the monthly extraction (2026-06-19): the adapter exists and
// satisfies its port surface at compile time but no DI wiring constructs
// it yet. Commit 2 will move the W2 batch mechanics into internal/billing/monthly
// and wire the adapter through monthly.NewService. See
// project_billing_extraction_plan_locked.md for the full sequence.
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

// --- monthly.AuditEmitter ---

func (a *MonthlyAdapter) RecordAudit(ctx context.Context, billID uuid.UUID, action BillAuditAction, actor *uuid.UUID, payload any) error {
	return recordAudit(ctx, a.audit, billID, action, actor, payload)
}
