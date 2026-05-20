package billing

import (
	"context"
	"fmt"

	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BillAuditRepository owns persistence for the bill_audit_log table.
//
// Separate from BillingRepository because audit is a distinct aggregate:
// bills are mutable current-state rows; audit log is an append-only event
// stream. Keeping them in the same repo would conflate two access patterns
// (hot reads + writes for state vs cold append + occasional read for log).
//
// All writes happen inside the parent mutation's transaction — audit failure
// rolls back the mutation. See service_audit.go for the recorder helper.
type BillAuditRepository interface {
	// Create inserts one audit event. Caller must pass txCtx so the insert
	// participates in the parent mutation's transaction.
	Create(ctx context.Context, log *BillAuditLog) error

	// EditedBillIDs returns the subset of input bill IDs that have at least
	// one edit-event recorded (APPLY_OVERRIDE / ADD_MANUAL / REMOVE_MANUAL /
	// EDIT_NOTE). Backend-derived source of truth for the is_edited flag —
	// FE never queries audit semantics directly.
	//
	// Empty input → empty map, no DB hit.
	EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

type billAuditRepository struct {
	db *gorm.DB
}

var _ BillAuditRepository = (*billAuditRepository)(nil)

func NewBillAuditRepository(db *gorm.DB) BillAuditRepository {
	return &billAuditRepository{db: db}
}

func (r *billAuditRepository) Create(ctx context.Context, log *BillAuditLog) error {
	if err := database.DB(ctx, r.db).Create(log).Error; err != nil {
		return fmt.Errorf("insert bill audit log: %w", err)
	}
	return nil
}

func (r *billAuditRepository) EditedBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	result := make(map[uuid.UUID]bool, len(billIDs))
	if len(billIDs) == 0 {
		return result, nil
	}

	editActions := []BillAuditAction{
		AuditApplyOverride,
		AuditAddManual,
		AuditRemoveManual,
		AuditEditNote,
	}

	var rows []uuid.UUID
	err := database.DB(ctx, r.db).
		Model(&BillAuditLog{}).
		Distinct("bill_id").
		Where("bill_id IN ?", billIDs).
		Where("action IN ?", editActions).
		Pluck("bill_id", &rows).Error
	if err != nil {
		return nil, fmt.Errorf("query edited bill ids: %w", err)
	}

	for _, id := range rows {
		result[id] = true
	}
	return result, nil
}
