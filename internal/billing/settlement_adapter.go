package billing

import (
	"context"

	"github.com/google/uuid"
)

// TODO(architecture): SettlementAdapter exists as an EXTRACTION SEAM — it
// enables the settlement sub-workflow to live in internal/billing/settlement/
// while the bill + bill_line_items + bill_audit_log tables stay at billing
// root. Unlike MonthlyAdapter, this adapter does NOT carry a "shrinks after
// repo split" promise: settlement bills share the bills table with monthly,
// so a future settlement-specific repo split has weaker justification (5 of
// 15 methods are settlement-specific; the rest touch the shared bills table
// regardless). Treat width as a stable boundary, not a transitional one.
// Tracked as Architecture Backlog §2 — see project_billing_extraction_backlog.md.
//
// SettlementAdapter satisfies the settlement workflow's two billing-root
// ports (settlement.BillStore, settlement.AuditStore) with a deliberately
// narrow dependency surface: only BillingRepository + BillAuditRepository.
//
// Mirrors MonthlyAdapter + PaymentAdapter + ReconciliationAdapter —
// billing-as-provider always exposes consumer-defined ports via a narrow
// adapter struct, never by widening the BillingService interface with
// consumer-shaped methods.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE MODIFYING THIS FILE
// ===========================================================================
//
//  1. Every method MUST remain PURE DELEGATION — a single passthrough to
//     repo.X / audit.X or a call into a package-level shared primitive
//     (recordAudit). If any method here ever grows past one line of
//     meaningful body, that is a signal the parent (billing root) is
//     missing a primitive — extract it there first.
//
//  2. DO NOT add business logic to SettlementAdapter. Not validation, not
//     branching, not state computation, not error classification, not
//     workflow orchestration. Those belong in settlement.Service
//     (workflow) or in billing.* (domain primitives shared across
//     workflows). The adapter is the boundary, not a decision point.
//
//  3. SettlementAdapter is an EXTRACTION SEAM, not an architectural seam.
//     Don't widen ports speculatively on the expectation that a settlement
//     repo split is coming — see TODO(architecture) above.
//
// ===========================================================================
//
// All methods that mutate state must be called with a txCtx from the
// caller's RunInTx so writes (and the audit row) join the same transaction.
//
// Commit 2 of the settlement extraction (W4, 2026-06-19): adapter created
// with 15 methods (14 BillStore + 1 AuditStore). Workflow methods migrate
// from billing root to settlement in commits 3-5; until then the adapter
// is consumed only by the settlement.Service constructor in cmd/main.go.
type SettlementAdapter struct {
	repo  BillingRepository
	audit BillAuditRepository
}

func NewSettlementAdapter(repo BillingRepository, audit BillAuditRepository) *SettlementAdapter {
	return &SettlementAdapter{repo: repo, audit: audit}
}

// --- settlement.BillStore: queries ---

func (a *SettlementAdapter) FindByID(ctx context.Context, id uuid.UUID) (*Bill, error) {
	return a.repo.FindByID(ctx, id)
}

func (a *SettlementAdapter) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	return a.repo.FindByIDWithRelations(ctx, id)
}

func (a *SettlementAdapter) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error) {
	return a.repo.FindByContractAndMonth(ctx, contractID, billingMonth, billType)
}

func (a *SettlementAdapter) FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]Bill, error) {
	return a.repo.FindNonVoidedByContractAndMonth(ctx, contractID, billingMonth)
}

func (a *SettlementAdapter) FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	return a.repo.FindUnpaidMonthlyByContractID(ctx, contractID)
}

func (a *SettlementAdapter) FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	return a.repo.FindAbsorbedByContractID(ctx, contractID)
}

func (a *SettlementAdapter) HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, moveOutMonth string) (bool, error) {
	return a.repo.HasPaidAdvanceRentForMonth(ctx, contractID, moveOutMonth)
}

func (a *SettlementAdapter) FindApartmentIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error) {
	return a.repo.FindApartmentIDByRoomID(ctx, roomID)
}

func (a *SettlementAdapter) FindRoomApartmentInfo(ctx context.Context, roomID uuid.UUID) (uuid.UUID, string, error) {
	return a.repo.FindRoomApartmentInfo(ctx, roomID)
}

func (a *SettlementAdapter) LockBillForCorrection(ctx context.Context, billID uuid.UUID) (*Bill, error) {
	return a.repo.LockBillForCorrection(ctx, billID)
}

// --- settlement.BillStore: commands ---

func (a *SettlementAdapter) CreateBill(ctx context.Context, b *Bill) error {
	return a.repo.Create(ctx, b)
}

func (a *SettlementAdapter) UpdateBill(ctx context.Context, b *Bill) error {
	return a.repo.Update(ctx, b)
}

func (a *SettlementAdapter) DeleteLineItemsBySource(ctx context.Context, billID uuid.UUID, source LineItemSource) error {
	return a.repo.DeleteLineItemsBySource(ctx, billID, source)
}

func (a *SettlementAdapter) DeleteEditableManualLineItems(ctx context.Context, billID uuid.UUID) error {
	return a.repo.DeleteEditableManualLineItems(ctx, billID)
}

func (a *SettlementAdapter) CreateLineItems(ctx context.Context, items []BillLineItem) error {
	return a.repo.CreateLineItems(ctx, items)
}

// HasNonVoidAdjustmentLineByRecoveryID is the inverse-FK applied-state probe
// used by the settlement recovery-resolution race guard. Same derivation the
// monthly path uses — a recovery is "applied" iff a non-VOID ADJUSTMENT line
// references it, so it can never be double-applied across bills.
func (a *SettlementAdapter) HasNonVoidAdjustmentLineByRecoveryID(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error) {
	return a.repo.HasNonVoidAdjustmentLineByRecoveryID(ctx, recoveryReadingID)
}

// Q1.5 per-utility probes + re-baseline — thin delegations to the billing repo,
// so settlement shares the exact same SQL/semantics as the monthly path.
func (a *SettlementAdapter) HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx context.Context, recoveryReadingID uuid.UUID, utility AdjustmentUtility) (bool, error) {
	return a.repo.HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx, recoveryReadingID, utility)
}

func (a *SettlementAdapter) HasUnreflectedOverRecordByContractID(ctx context.Context, contractID uuid.UUID) (bool, error) {
	return a.repo.HasUnreflectedOverRecordByContractID(ctx, contractID)
}

// FindUnreflectedOverRecordsByContractID exposes the row twin of the freshness
// gate to settlement so the resolver emits exactly the pairs the gate detects
// (Epic B INV-1). Thin delegation — shares the billing repo's single predicate.
func (a *SettlementAdapter) FindUnreflectedOverRecordsByContractID(ctx context.Context, contractID uuid.UUID) ([]UnreflectedOverRecord, error) {
	return a.repo.FindUnreflectedOverRecordsByContractID(ctx, contractID)
}

// FindRecoverySourceRefundRates delegates the S0 gate + source-rate lookup to
// the billing repo so settlement prices recovery refunds at the exact same
// source-bill unit_price the monthly path uses (HC6, no rate drift).
func (a *SettlementAdapter) FindRecoverySourceRefundRates(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*SourceRefundRates, error) {
	return a.repo.FindRecoverySourceRefundRates(ctx, sourceReadingID, contractID)
}

func (a *SettlementAdapter) ZeroAutoLineUsage(ctx context.Context, billID uuid.UUID, lineType LineItemType) error {
	return a.repo.ZeroAutoLineUsage(ctx, billID, lineType)
}

// --- settlement.AuditStore ---

func (a *SettlementAdapter) RecordAudit(ctx context.Context, billID uuid.UUID, action BillAuditAction, actor *uuid.UUID, payload any) error {
	return recordAudit(ctx, a.audit, billID, action, actor, payload)
}
