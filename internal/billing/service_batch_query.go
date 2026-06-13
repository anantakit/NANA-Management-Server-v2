package billing

import (
	"context"
	"fmt"

	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// isNotFound delegates to database.IsNotFound for package-local callers
// that predate the shared helper. New callers should use database.IsNotFound
// directly.
func isNotFound(err error) bool {
	return database.IsNotFound(err)
}

// --- Batch query (for review page) ---

func (s *billingService) GetBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error) {
	b, err := s.repo.FindBatchByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find batch: %w", err)
	}
	return b, nil
}

// GetBatchItems returns every batch item with its tenant + edit history flag.
// Issues ONE batched EditedBillIDs query for all committed bills in the
// response (items where BillID != nil) so the Edited badge can render
// directly on BillBatchReview without forcing the admin to open every
// drawer. Pre-commit items (BillID == nil) keep IsEdited=false trivially.
//
// On audit-store failure the call returns the wrapped error per the locked
// is_edited contract — never silently hide edited state.
func (s *billingService) GetBatchItems(ctx context.Context, id uuid.UUID) ([]BatchItemWithTenant, error) {
	items, err := s.repo.FindBatchItemsByBatchID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Collect bill IDs from committed items only — uncommitted items have
	// no bill row and therefore no audit history to query.
	var billIDs []uuid.UUID
	for _, it := range items {
		if it.BillID != nil {
			billIDs = append(billIDs, *it.BillID)
		}
	}
	if len(billIDs) == 0 {
		return items, nil
	}

	editedSet, err := s.audit.EditedBillIDs(ctx, billIDs)
	if err != nil {
		return nil, fmt.Errorf("populate batch item is_edited: %w", err)
	}
	for i := range items {
		if items[i].BillID != nil && editedSet[*items[i].BillID] {
			items[i].IsEdited = true
		}
	}
	return items, nil
}

func (s *billingService) ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error) {
	params.Normalize()
	if params.Status != "" && !BatchStatus(params.Status).IsValid() {
		return nil, 0, respond.ErrBadRequest.WithMessage("status ไม่ถูกต้อง")
	}
	return s.repo.ListBatches(ctx, params)
}
