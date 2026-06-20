package monthly

import (
	"context"
	"fmt"
	"time"

	"nana/internal/billing"
	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BatchRepository owns persistence for the monthly batch lifecycle tables —
// bill_generation_batches + bill_generation_batch_items. SQL that previously
// lived on billing.BillingRepository for these tables now lives here.
//
// Batch entity TYPES (BillGenerationBatch, BillGenerationBatchItem,
// BatchItemWithTenant, BatchListParams, BatchStatus, CommitStatus, ResultType,
// ReasonCode*, ComputedSnapshot) intentionally stay in the billing root
// package — they are shared shapes that handler DTOs + the service interface
// already reference, and moving them would multiply rename churn without
// changing the persistence-ownership win this split delivers.
type BatchRepository interface {
	CreateBatch(ctx context.Context, batch *billing.BillGenerationBatch, items []billing.BillGenerationBatchItem) error
	FindBatchByID(ctx context.Context, id uuid.UUID) (*billing.BillGenerationBatch, error)
	FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]billing.BatchItemWithTenant, error)
	FindBatchItemByID(ctx context.Context, itemID uuid.UUID) (*billing.BillGenerationBatchItem, error)
	FindBatchItemByIDWithTenant(ctx context.Context, itemID uuid.UUID) (*billing.BatchItemWithTenant, error)
	UpdateBatchItemPlan(
		ctx context.Context,
		itemID uuid.UUID,
		resultType billing.ResultType,
		reasonCode, reasonText string,
		billID *uuid.UUID,
		snapshot billing.ComputedSnapshot,
	) error
	ListBatches(ctx context.Context, params billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error)

	LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*billing.BillGenerationBatch, error)
	ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]billing.BillGenerationBatchItem, error)
	UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error
	UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error
	UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status billing.CommitStatus, committedAt *time.Time) error
}

type batchRepository struct {
	db *gorm.DB
}

var _ BatchRepository = (*batchRepository)(nil)

func NewBatchRepository(db *gorm.DB) BatchRepository {
	return &batchRepository{db: db}
}

// CreateBatch persists the batch header + all items in the current transaction.
// Caller must wrap this in TxManager.RunInTx to keep batch atomic with the bills.
func (r *batchRepository) CreateBatch(ctx context.Context, batch *billing.BillGenerationBatch, items []billing.BillGenerationBatchItem) error {
	db := database.DB(ctx, r.db)
	if err := db.Create(batch).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	for i := range items {
		items[i].BatchID = batch.ID
	}
	return db.Create(&items).Error
}

func (r *batchRepository) FindBatchByID(ctx context.Context, id uuid.UUID) (*billing.BillGenerationBatch, error) {
	var b billing.BillGenerationBatch
	err := database.DB(ctx, r.db).Where("id = ?", id).First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *batchRepository) FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]billing.BatchItemWithTenant, error) {
	type joinRow struct {
		billing.BillGenerationBatchItem
		TenantName string              `gorm:"column:tenant_name"`
		BillStatus *billing.BillStatus `gorm:"column:bill_status"`
	}
	var rows []joinRow
	err := database.DB(ctx, r.db).
		Table("bill_generation_batch_items AS bi").
		Select("bi.*, t.full_name AS tenant_name, b.status AS bill_status").
		Joins("LEFT JOIN contracts c ON c.id = bi.contract_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN tenants t ON t.id = c.tenant_id AND t.deleted_at IS NULL").
		Joins("LEFT JOIN bills b ON b.id = bi.bill_id AND b.deleted_at IS NULL").
		Where("bi.batch_id = ?", batchID).
		Order("bi.room_floor ASC, bi.room_number ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]billing.BatchItemWithTenant, len(rows))
	for i, row := range rows {
		result[i] = billing.BatchItemWithTenant{
			BillGenerationBatchItem: row.BillGenerationBatchItem,
			TenantName:              row.TenantName,
			BillStatus:              row.BillStatus,
		}
	}
	return result, nil
}

// FindBatchItemByID returns the raw batch item row. Used by the single-item
// re-plan flow to authorize against the batch + verify pre-commit state
// before re-running the planner. Returns gorm.ErrRecordNotFound when the
// item ID does not exist.
func (r *batchRepository) FindBatchItemByID(ctx context.Context, itemID uuid.UUID) (*billing.BillGenerationBatchItem, error) {
	var it billing.BillGenerationBatchItem
	err := database.DB(ctx, r.db).Where("id = ?", itemID).First(&it).Error
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// FindBatchItemByIDWithTenant returns the same projection as
// FindBatchItemsByBatchID (tenant_name + bill_status joined) but scoped to
// one item. Used as the response payload for POST replan so the FE can
// patch a single row in place without refetching the whole batch.
func (r *batchRepository) FindBatchItemByIDWithTenant(ctx context.Context, itemID uuid.UUID) (*billing.BatchItemWithTenant, error) {
	type joinRow struct {
		billing.BillGenerationBatchItem
		TenantName string              `gorm:"column:tenant_name"`
		BillStatus *billing.BillStatus `gorm:"column:bill_status"`
	}
	var row joinRow
	err := database.DB(ctx, r.db).
		Table("bill_generation_batch_items AS bi").
		Select("bi.*, t.full_name AS tenant_name, b.status AS bill_status").
		Joins("LEFT JOIN contracts c ON c.id = bi.contract_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN tenants t ON t.id = c.tenant_id AND t.deleted_at IS NULL").
		Joins("LEFT JOIN bills b ON b.id = bi.bill_id AND b.deleted_at IS NULL").
		Where("bi.id = ?", itemID).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	// Scan returns zero-value (no error) when no row matches — translate to
	// ErrRecordNotFound so the service maps consistently to 404.
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}
	return &billing.BatchItemWithTenant{
		BillGenerationBatchItem: row.BillGenerationBatchItem,
		TenantName:              row.TenantName,
		BillStatus:              row.BillStatus,
	}, nil
}

// UpdateBatchItemPlan rewrites the classification snapshot for one batch
// item. Used by single-item re-plan after a state change (e.g. meter
// recorded for a MISSING_METER_READING row). Uses an explicit column list
// so the empty ComputedSnapshot for non-CREATED rows is persisted as
// `{}` rather than skipped by GORM's zero-value default in Updates(struct).
//
// `bill_id` is set to NULL except for the ALREADY_EXISTS classification
// where the planner returns a pointer to the existing bill.
func (r *batchRepository) UpdateBatchItemPlan(
	ctx context.Context,
	itemID uuid.UUID,
	resultType billing.ResultType,
	reasonCode, reasonText string,
	billID *uuid.UUID,
	snapshot billing.ComputedSnapshot,
) error {
	updates := map[string]any{
		"result_type":       resultType,
		"reason_code":       reasonCode,
		"reason_text":       reasonText,
		"bill_id":           billID,
		"computed_snapshot": snapshot,
	}
	return database.DB(ctx, r.db).
		Model(&billing.BillGenerationBatchItem{}).
		Where("id = ?", itemID).
		Updates(updates).Error
}

func (r *batchRepository) ListBatches(ctx context.Context, params billing.BatchListParams) ([]billing.BillGenerationBatch, int64, error) {
	query := database.DB(ctx, r.db).Model(&billing.BillGenerationBatch{})
	if params.ApartmentID != "" {
		query = query.Where("apartment_id = ?", params.ApartmentID)
	}
	if params.BillingMonth != "" {
		query = query.Where("billing_month = ?", params.BillingMonth)
	}
	if params.Status != "" {
		query = query.Where("status = ?", params.Status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order,
		[]string{"created_at", "billing_month", "status"}, "created_at")

	// Stable ordering: tiebreak by id DESC so "latest" never flips
	// when two batches share the same created_at (or sort column).
	var batches []billing.BillGenerationBatch
	err := query.
		Order(fmt.Sprintf("%s %s, id DESC", col, order)).
		Offset(params.Offset()).
		Limit(params.Limit).
		Find(&batches).Error
	return batches, total, err
}

// LockBatchForCommit acquires a row-level lock (SELECT FOR UPDATE) on the batch.
// Must be called inside a transaction. Prevents concurrent commit attempts.
func (r *batchRepository) LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*billing.BillGenerationBatch, error) {
	var b billing.BillGenerationBatch
	err := database.DB(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", batchID).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListCommitPendingItems returns items eligible for commit: CREATED with no bill yet.
// Idempotent — retrying after partial commit only picks up uncommitted items.
func (r *batchRepository) ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]billing.BillGenerationBatchItem, error) {
	var items []billing.BillGenerationBatchItem
	err := database.DB(ctx, r.db).
		Where("batch_id = ? AND result_type = ? AND bill_id IS NULL", batchID, billing.ResultCreated).
		Order("room_floor ASC, room_number ASC").
		Find(&items).Error
	return items, err
}

// UpdateBatchItemCommitted sets bill_id on a batch item after successful bill creation.
// Must be called in the same tx as Create(bill).
func (r *batchRepository) UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error {
	return database.DB(ctx, r.db).
		Model(&billing.BillGenerationBatchItem{}).
		Where("id = ?", itemID).
		Update("bill_id", billID).Error
}

// UpdateBatchItemCommitError records a commit error on a batch item.
// Called in a separate tx from the failed bill creation (so rollback doesn't erase it).
func (r *batchRepository) UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error {
	return database.DB(ctx, r.db).
		Model(&billing.BillGenerationBatchItem{}).
		Where("id = ?", itemID).
		Updates(map[string]any{
			"reason_code": billing.ReasonCodeCommitError,
			"reason_text": reasonText,
		}).Error
}

// UpdateBatchCommitStatus updates the commit_status and committed_at on the batch header.
func (r *batchRepository) UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status billing.CommitStatus, committedAt *time.Time) error {
	return database.DB(ctx, r.db).
		Model(&billing.BillGenerationBatch{}).
		Where("id = ?", batchID).
		Updates(map[string]any{
			"commit_status": status,
			"committed_at":  committedAt,
		}).Error
}
