package billing

import (
	"context"

	"github.com/google/uuid"
)

// RecoveryAppliedChecker satisfies meterreading.BillingApplicationChecker
// with a thin delegation to the canonical repo probe.
//
// Mirrors PaymentAdapter / RecoveryAdapter — billing-as-provider always
// exposes consumer-defined ports via a narrow adapter struct (see
// cross-feature-patterns.md §4). The repo method
// HasNonVoidAdjustmentLineByRecoveryID owns the actual SQL; this adapter
// just satisfies the consumer-shaped interface so meterreading need not
// import billing at the type level.
//
// Phase 7 doctrine (locked 2026-06-25): applied state is DERIVED from
// inverse-FK presence, never stored. This adapter is the only path
// meterreading uses to read that derived state.
type RecoveryAppliedChecker struct {
	repo BillingRepository
}

func NewRecoveryAppliedChecker(repo BillingRepository) *RecoveryAppliedChecker {
	return &RecoveryAppliedChecker{repo: repo}
}

// HasNonVoidAdjustmentLine delegates to BillingRepository — see the repo
// method for the exact SQL semantics. VOIDed bills DO NOT count.
func (a *RecoveryAppliedChecker) HasNonVoidAdjustmentLine(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error) {
	return a.repo.HasNonVoidAdjustmentLineByRecoveryID(ctx, recoveryReadingID)
}

// HasNonVoidAdjustmentLineForUtility delegates the Q1.5 per-utility applied-state
// probe. The utility crosses the boundary as a primitive string; billing owns
// the AdjustmentUtility type.
func (a *RecoveryAppliedChecker) HasNonVoidAdjustmentLineForUtility(ctx context.Context, recoveryReadingID uuid.UUID, utility string) (bool, error) {
	return a.repo.HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx, recoveryReadingID, AdjustmentUtility(utility))
}
