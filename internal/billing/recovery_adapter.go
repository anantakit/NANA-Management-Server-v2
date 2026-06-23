package billing

import (
	"context"
	"fmt"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
)

// RecoveryAdapter satisfies meterreading.BillingAdjustmentCommander with a
// deliberately narrow dependency surface: BillingRepository + BillAuditRepository.
//
// Mirrors PaymentAdapter (billing/payment_adapter.go) — billing-as-provider
// always exposes consumer-defined ports via a narrow adapter struct, never
// by widening the BillingService interface with consumer-shaped methods.
//
// The single command method AttachAdjustmentLine encapsulates four steps
// inside the caller's transaction:
//  1. FindDraftBillForContractAndMonth (status = DRAFT, type = MONTHLY).
//  2. Construct ADJUSTMENT BillLineItem (Source=MANUAL, FK to recovery row,
//     reason code, note, tenant-visible description with source-month
//     provenance per feedback_reading_recovery_doctrine.md line 75).
//  3. ValidateAdjustment (Phase 4 domain method) before INSERT.
//  4. recordAudit(AuditApplyRecoveryAdjustment) — joins the same TX.
//
// All four writes commit or roll back atomically with the caller's
// recovery meter_readings INSERT.
//
// ============================================================================
// STEWARDSHIP CONTRACT — READ BEFORE ADDING A METHOD
// ============================================================================
//
//  1. AttachAdjustmentLine is DELIBERATELY composed (5 internal ops, ~30
//     lines). The composition is acceptable BECAUSE it is the only method
//     and covers one business outcome (attach a recovery's money side).
//     This is the only composed method allowed on this adapter.
//
//  2. RE-AUDIT TRIGGERS — if any of these land in a future phase, the
//     refactor below is required BEFORE the method is added:
//
//     (a) A SECOND composed method (e.g. ReverseAdjustment that does
//         lookup + construct + validate + write + audit). Refactor:
//         extract *billing.RecoveryService (settlement-style wrapper);
//         shrink this adapter to thin delegation methods (FindDraft,
//         CreateAdjustmentLine, RecordAudit); the service composes them.
//
//     (b) A query method (FindAdjustmentHistory, ListAdjustmentsByRecoveryID).
//         Refactor: put it in a SEPARATE BillingRecoveryQuerier port;
//         query/command split per domain-ownership.md Q4. Never grow the
//         Command adapter with read methods.
//
//     (c) A method needing history/lineage state to decide an outcome
//         (e.g. reversibility check). Refactor: extract *billing.RecoveryService
//         to own the state machine; adapter becomes dumb storage.
//
//     (d) Adapter dependencies grow beyond {repo, audit}. Strong signal
//         the parent (billing root) is missing a primitive — extract
//         there first.
//
//  3. DO NOT add validation, branching, or workflow orchestration to this
//     file beyond the locked AttachAdjustmentLine composition. If
//     "just one more line of business logic" feels natural, that is the
//     trigger to re-audit per #2.
// ============================================================================
type RecoveryAdapter struct {
	repo  BillingRepository
	audit BillAuditRepository
}

func NewRecoveryAdapter(repo BillingRepository, audit BillAuditRepository) *RecoveryAdapter {
	return &RecoveryAdapter{repo: repo, audit: audit}
}

// AttachAdjustmentLine implements the Phase 5 recovery commit's bill-side
// transaction. Caller MUST invoke with a txCtx from meterreading's
// RunInTx so the line INSERT and audit row commit atomically with the
// recovery meter_readings INSERT.
//
// Boundary DTO `meterreading.AttachAdjustmentParams` is consumer-defined
// per cross-feature-patterns.md §4 (primitive types only — no
// billing-domain types in the consumer's port surface). This adapter
// imports meterreading solely to satisfy that signature.
//
// Returns ErrRecoveryNoDraftBill when no DRAFT MONTHLY bill exists for
// the given contract+month — operator runs monthly batch first.
func (a *RecoveryAdapter) AttachAdjustmentLine(ctx context.Context, params meterreading.AttachAdjustmentParams) error {
	bill, err := a.repo.FindDraftBillForContractAndMonth(ctx, params.ContractID, params.BillingMonth, BillTypeMonthly)
	if err != nil {
		if database.IsNotFound(err) {
			return ErrRecoveryNoDraftBill
		}
		return fmt.Errorf("find draft bill for recovery: %w", err)
	}

	reasonCode := AdjustmentReasonCode(params.ReasonCode)
	line := BillLineItem{
		BillID:                      bill.ID,
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		Description:                 buildAdjustmentDescription(params.Amount, params.SourceBillingMonth),
		Amount:                      params.Amount,
		Quantity:                    1,
		UnitPrice:                   params.Amount,
		SortOrder:                   9000,
		AdjustmentRecoveryReadingID: &params.RecoveryReadingID,
		AdjustmentReasonCode:        &reasonCode,
		AdjustmentNote:              &params.Note,
	}
	if err := line.ValidateAdjustment(); err != nil {
		return err
	}
	if err := a.repo.CreateLineItems(ctx, []BillLineItem{line}); err != nil {
		return fmt.Errorf("attach adjustment line: %w", err)
	}

	return recordAudit(ctx, a.audit, bill.ID, AuditApplyRecoveryAdjustment, params.ActorID,
		AuditApplyRecoveryAdjustmentPayload{
			Amount:            params.Amount,
			RecoveryReadingID: params.RecoveryReadingID,
			ReasonCode:        params.ReasonCode,
			Note:              params.Note,
		})
}

// buildAdjustmentDescription produces the tenant-visible Description on
// the ADJUSTMENT line per feedback_reading_recovery_doctrine.md line 75.
// Two branches preserve Thai grammar for both directions; signed Amount
// drives the choice.
func buildAdjustmentDescription(amount int64, sourceMonth string) string {
	if amount < 0 {
		return fmt.Sprintf("คืนยอดที่เก็บเกินจากเดือน %s (จดมิเตอร์ผิด)", sourceMonth)
	}
	return fmt.Sprintf("เก็บยอดเพิ่มเดือน %s (จดมิเตอร์ผิด)", sourceMonth)
}
