package billing

import (
	"context"
	"fmt"
	"time"

	"nana/internal/contract"
	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BillingRepository interface {
	FindAll(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Bill, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error)
	FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]Bill, error)
	FindApartmentIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error)
	FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error)
	FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error)
	Create(ctx context.Context, bill *Bill) error
	Update(ctx context.Context, bill *Bill) error
	DeleteLineItemsBySource(ctx context.Context, billID uuid.UUID, source LineItemSource) error
	CreateLineItems(ctx context.Context, items []BillLineItem) error
	SumPaidByContractSince(ctx context.Context, contractID uuid.UUID, sinceMonth string) (int64, error)
	HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, moveOutMonth string) (bool, error)
	FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error)
	FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error)

	CreateBatch(ctx context.Context, batch *BillGenerationBatch, items []BillGenerationBatchItem) error
	FindBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error)
	FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error)

	// Commit flow
	LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*BillGenerationBatch, error)
	ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]BillGenerationBatchItem, error)
	UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error
	UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error
	UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status CommitStatus, committedAt *time.Time) error

	// Batch finalize flow
	ListBillsByBatchID(ctx context.Context, batchID uuid.UUID) ([]Bill, error)

	// Correction flow — row-locks the bill before void+recreate so concurrent
	// correction attempts on the same bill serialize cleanly. Caller MUST be
	// inside a TX (uses SELECT FOR UPDATE).
	LockBillForCorrection(ctx context.Context, billID uuid.UUID) (*Bill, error)
}

type billingRepository struct {
	db *gorm.DB
}

var _ BillingRepository = (*billingRepository)(nil)

func NewBillingRepository(db *gorm.DB) BillingRepository {
	return &billingRepository{db: db}
}

func (r *billingRepository) baseJoinQuery(ctx context.Context) *gorm.DB {
	return database.DB(ctx, r.db).
		Model(&Bill{}).
		Joins("JOIN contracts ON contracts.id = bills.contract_id AND contracts.deleted_at IS NULL").
		Joins("JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("JOIN rooms ON rooms.id = contracts.room_id AND rooms.deleted_at IS NULL").
		Joins("JOIN apartments ON apartments.id = rooms.apartment_id AND apartments.deleted_at IS NULL").
		Where("bills.deleted_at IS NULL")
}

func (r *billingRepository) selectColumns() string {
	return `bills.*,
		tenants.full_name AS tenant_name,
		rooms.number AS room_number,
		apartments.name AS apartment_name,
		apartments.id AS apartment_id`
}

func (r *billingRepository) FindAll(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error) {
	var total int64
	query := r.baseJoinQuery(ctx)

	if params.ContractID != "" {
		query = query.Where("bills.contract_id = ?", params.ContractID)
	}
	if params.ApartmentID != "" {
		query = query.Where("apartments.id = ?", params.ApartmentID)
	}
	if params.Month != "" {
		query = query.Where("bills.billing_month = ?", params.Month)
	}
	if params.Status != "" {
		query = query.Where("bills.status = ?", params.Status)
	}
	if params.BillType != "" {
		query = query.Where("bills.bill_type = ?", params.BillType)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(tenants.full_name ILIKE ? OR rooms.number ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order,
		[]string{"billing_month", "total_amount", "status", "created_at"}, "created_at")
	orderClause := fmt.Sprintf("bills.%s %s", col, order)

	type joinRow struct {
		Bill
		TenantName    string    `gorm:"column:tenant_name"`
		RoomNumber    string    `gorm:"column:room_number"`
		ApartmentName string    `gorm:"column:apartment_name"`
		ApartmentID   uuid.UUID `gorm:"column:apartment_id"`
	}

	var rows []joinRow
	err := query.
		Select(r.selectColumns()).
		Order(orderClause).
		Offset(params.Offset()).
		Limit(params.Limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	result := make([]BillWithRelations, len(rows))
	for i, row := range rows {
		result[i] = BillWithRelations{
			Bill:          row.Bill,
			TenantName:    row.TenantName,
			RoomNumber:    row.RoomNumber,
			ApartmentName: row.ApartmentName,
			ApartmentID:   row.ApartmentID,
		}
	}
	return result, total, nil
}

func (r *billingRepository) FindByID(ctx context.Context, id uuid.UUID) (*Bill, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Preload("LineItems", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC")
		}).
		Where("id = ?", id).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *billingRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Preload("LineItems", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC")
		}).
		Where("id = ?", id).
		First(&b).Error
	if err != nil {
		return nil, err
	}

	var rel struct {
		TenantName    string    `gorm:"column:tenant_name"`
		RoomNumber    string    `gorm:"column:room_number"`
		ApartmentName string    `gorm:"column:apartment_name"`
		ApartmentID   uuid.UUID `gorm:"column:apartment_id"`
	}
	err = database.DB(ctx, r.db).
		Table("contracts c").
		Select(`t.full_name AS tenant_name,
			rm.number AS room_number,
			a.name AS apartment_name,
			a.id AS apartment_id`).
		Joins("LEFT JOIN tenants t ON t.id = c.tenant_id AND t.deleted_at IS NULL").
		Joins("LEFT JOIN rooms rm ON rm.id = c.room_id AND rm.deleted_at IS NULL").
		Joins("LEFT JOIN apartments a ON a.id = rm.apartment_id AND a.deleted_at IS NULL").
		Where("c.id = ? AND c.deleted_at IS NULL", b.ContractID).
		Scan(&rel).Error
	if err != nil {
		return nil, err
	}

	return &BillWithRelations{
		Bill:          b,
		TenantName:    rel.TenantName,
		RoomNumber:    rel.RoomNumber,
		ApartmentName: rel.ApartmentName,
		ApartmentID:   rel.ApartmentID,
	}, nil
}

func (r *billingRepository) FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Where("contract_id = ? AND billing_month = ? AND bill_type = ? AND status IN ?",
			contractID, billingMonth, billType, []BillStatus{BillStatusFinalized, BillStatusPaid}).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// FindNonVoidedByContractAndMonth finds all non-voided bills for a contract in a billing month.
// Used by settlement generation to handle monthly → settlement replacement.
func (r *billingRepository) FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]Bill, error) {
	var bills []Bill
	err := database.DB(ctx, r.db).
		Where("contract_id = ? AND billing_month = ? AND status != ?",
			contractID, billingMonth, BillStatusVoid).
		Find(&bills).Error
	return bills, err
}

// FindApartmentIDByRoomID resolves apartment ownership via room JOIN.
// Display read — no cross-feature write.
//
// Scans into *string then parses, because GORM's generic column scan
// (Scan/Pluck) reflects into uuid.UUID as [16]byte and never calls
// uuid.UUID.Scan — this breaks silently when pgx returns UUIDs in text
// encoding (connection-dependent behavior). Using *string + uuid.Parse
// is driver-agnostic.
func (r *billingRepository) FindApartmentIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error) {
	var raw string
	err := database.DB(ctx, r.db).
		Table("rooms").
		Where("id = ? AND deleted_at IS NULL", roomID).
		Pluck("apartment_id", &raw).Error
	if err != nil {
		return uuid.Nil, err
	}
	if raw == "" {
		return uuid.Nil, gorm.ErrRecordNotFound
	}
	aptID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse apartment_id %q: %w", raw, err)
	}
	return aptID, nil
}

// FindActiveContractsByApartmentID returns active contracts with room info for batch billing.
// Display-read JOIN: contracts + rooms (cross-feature pattern level 1 — domain constant).
// Ordered by floor ASC, room number ASC for deterministic processing.
func (r *billingRepository) FindActiveContractsByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]ContractWithRoom, error) {
	var rows []struct {
		ContractID             uuid.UUID  `gorm:"column:contract_id"`
		RoomID                 uuid.UUID  `gorm:"column:room_id"`
		RoomNumber             string     `gorm:"column:room_number"`
		RoomFloor              int        `gorm:"column:room_floor"`
		StartDate              time.Time  `gorm:"column:start_date"`
		EndDate                *time.Time `gorm:"column:end_date"`
		MonthlyRent            int64      `gorm:"column:monthly_rent"`
		ElectricityRatePerUnit int64      `gorm:"column:electricity_rate_per_unit"`
		WaterRatePerUnit       int64      `gorm:"column:water_rate_per_unit"`
	}
	err := database.DB(ctx, r.db).
		Table("contracts c").
		Select(`c.id AS contract_id, c.room_id, r.number AS room_number, r.floor AS room_floor,
			c.start_date, c.end_date, c.monthly_rent, c.electricity_rate_per_unit, c.water_rate_per_unit`).
		Joins("JOIN rooms r ON r.id = c.room_id AND r.deleted_at IS NULL").
		Where("r.apartment_id = ? AND c.status = ? AND c.deleted_at IS NULL", apartmentID, contract.ContractStatusActive).
		Order("r.floor ASC, r.number ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]ContractWithRoom, len(rows))
	for i, row := range rows {
		result[i] = ContractWithRoom{
			ContractID:             row.ContractID,
			RoomID:                 row.RoomID,
			RoomNumber:             row.RoomNumber,
			RoomFloor:              row.RoomFloor,
			StartDate:              row.StartDate,
			EndDate:                row.EndDate,
			MonthlyRent:            row.MonthlyRent,
			ElectricityRatePerUnit: row.ElectricityRatePerUnit,
			WaterRatePerUnit:       row.WaterRatePerUnit,
		}
	}
	return result, nil
}

// FindExistingByContractsAndMonth bulk-checks for existing FINALIZED/PAID MONTHLY bills.
// Returns map[contractID]*Bill with ID always populated.
func (r *billingRepository) FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error) {
	if len(contractIDs) == 0 {
		return map[uuid.UUID]*Bill{}, nil
	}
	var bills []Bill
	err := database.DB(ctx, r.db).
		Where("contract_id IN ? AND billing_month = ? AND bill_type = ? AND status IN ?",
			contractIDs, month, BillTypeMonthly, []BillStatus{BillStatusFinalized, BillStatusPaid}).
		Find(&bills).Error
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]*Bill, len(bills))
	for i := range bills {
		result[bills[i].ContractID] = &bills[i]
	}
	return result, nil
}

// CreateBatch persists the batch header + all items in the current transaction.
// Caller must wrap this in TxManager.RunInTx to keep batch atomic with the bills.
func (r *billingRepository) CreateBatch(ctx context.Context, batch *BillGenerationBatch, items []BillGenerationBatchItem) error {
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

func (r *billingRepository) FindBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error) {
	var b BillGenerationBatch
	err := database.DB(ctx, r.db).Where("id = ?", id).First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *billingRepository) FindBatchItemsByBatchID(ctx context.Context, batchID uuid.UUID) ([]BatchItemWithTenant, error) {
	type joinRow struct {
		BillGenerationBatchItem
		TenantName string      `gorm:"column:tenant_name"`
		BillStatus *BillStatus `gorm:"column:bill_status"`
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
	result := make([]BatchItemWithTenant, len(rows))
	for i, row := range rows {
		result[i] = BatchItemWithTenant{
			BillGenerationBatchItem: row.BillGenerationBatchItem,
			TenantName:              row.TenantName,
			BillStatus:              row.BillStatus,
		}
	}
	return result, nil
}

func (r *billingRepository) ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error) {
	query := database.DB(ctx, r.db).Model(&BillGenerationBatch{})
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
	var batches []BillGenerationBatch
	err := query.
		Order(fmt.Sprintf("%s %s, id DESC", col, order)).
		Offset(params.Offset()).
		Limit(params.Limit).
		Find(&batches).Error
	return batches, total, err
}

func (r *billingRepository) Create(ctx context.Context, bill *Bill) error {
	return database.DB(ctx, r.db).Create(bill).Error
}

func (r *billingRepository) Update(ctx context.Context, bill *Bill) error {
	return database.DB(ctx, r.db).Model(bill).Select("*").Omit("deleted_at").Updates(bill).Error
}

// SumPaidByContractSince returns total amount of PAID monthly bills for a contract
// from the given billing month onward. Used to compute PREPAID_CREDIT for settlement.
func (r *billingRepository) SumPaidByContractSince(ctx context.Context, contractID uuid.UUID, sinceMonth string) (int64, error) {
	var total int64
	err := database.DB(ctx, r.db).
		Model(&Bill{}).
		Select("COALESCE(SUM(total_amount), 0)").
		Where("contract_id = ? AND bill_type = ? AND status = ? AND billing_month >= ?",
			contractID, BillTypeMonthly, BillStatusPaid, sinceMonth).
		Scan(&total).Error
	return total, err
}

// HasPaidAdvanceRentForMonth checks if advance rent for moveOutMonth was already
// collected. In the advance-billing model, bill M-1 contains rent for M.
// So we check for a PAID monthly bill with billing_month = moveOutMonth - 1.
//
// This is a shortcut that works because the system bills rent one month in advance.
func (r *billingRepository) HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, moveOutMonth string) (bool, error) {
	t, err := time.Parse("2006-01", moveOutMonth)
	if err != nil {
		return false, fmt.Errorf("parse move-out month %q: %w", moveOutMonth, err)
	}
	prevMonth := t.AddDate(0, -1, 0).Format("2006-01")

	var count int64
	err = database.DB(ctx, r.db).
		Model(&Bill{}).
		Where("contract_id = ? AND bill_type = ? AND status = ? AND billing_month = ?",
			contractID, BillTypeMonthly, BillStatusPaid, prevMonth).
		Count(&count).Error
	return count > 0, err
}

// FindUnpaidMonthlyByContractID returns all non-paid, non-voided monthly bills
// for a contract, ordered by billing_month ASC. Used by settlement generation
// to absorb outstanding charges into a single settlement document.
func (r *billingRepository) FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	var bills []Bill
	err := database.DB(ctx, r.db).
		Preload("LineItems", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC")
		}).
		Where("contract_id = ? AND bill_type = ? AND status IN ?",
			contractID, BillTypeMonthly, []BillStatus{BillStatusDraft, BillStatusFinalized}).
		Order("billing_month ASC").
		Find(&bills).Error
	return bills, err
}

// FindAbsorbedByContractID returns monthly bills that were voided because they
// were absorbed into a settlement. Used to restore them when settlement is voided.
func (r *billingRepository) FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]Bill, error) {
	var bills []Bill
	err := database.DB(ctx, r.db).
		Where("contract_id = ? AND bill_type = ? AND status = ? AND void_reason = ?",
			contractID, BillTypeMonthly, BillStatusVoid, "ABSORBED_BY_SETTLEMENT").
		Order("billing_month ASC").
		Find(&bills).Error
	return bills, err
}

// --- Commit flow ---

// LockBatchForCommit acquires a row-level lock (SELECT FOR UPDATE) on the batch.
// Must be called inside a transaction. Prevents concurrent commit attempts.
func (r *billingRepository) LockBatchForCommit(ctx context.Context, batchID uuid.UUID) (*BillGenerationBatch, error) {
	var b BillGenerationBatch
	err := database.DB(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", batchID).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// LockBillForCorrection acquires a row-level lock (SELECT FOR UPDATE) on the
// bill and preloads line items so the correction service has the full state
// it needs without a second round-trip. Must be called inside a transaction.
//
// Prevents the double-correction race: two concurrent POST /:id/correct
// requests serialize on the lock, the first wins (old → VOID + supersede),
// the second's CanCorrect() call sees ErrAlreadySuperseded and bails.
func (r *billingRepository) LockBillForCorrection(ctx context.Context, billID uuid.UUID) (*Bill, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Preload("LineItems", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC")
		}).
		Where("id = ?", billID).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListCommitPendingItems returns items eligible for commit: CREATED with no bill yet.
// Idempotent — retrying after partial commit only picks up uncommitted items.
func (r *billingRepository) ListCommitPendingItems(ctx context.Context, batchID uuid.UUID) ([]BillGenerationBatchItem, error) {
	var items []BillGenerationBatchItem
	err := database.DB(ctx, r.db).
		Where("batch_id = ? AND result_type = ? AND bill_id IS NULL", batchID, ResultCreated).
		Order("room_floor ASC, room_number ASC").
		Find(&items).Error
	return items, err
}

// ListBillsByBatchID returns every monthly bill produced by the given batch
// in deterministic order — JOIN through batch_items to sort by the same
// (room_floor, room_number) the admin saw on BillBatchReview, then bill_id
// as a stable tie-breaker. Returns all statuses (DRAFT / FINALIZED / VOID /
// PAID) — the caller (BatchFinalizeAll) classifies what to do with each.
//
// Settlement bills are excluded at the SQL layer (defense-in-depth, even
// though batch_id is only ever set on monthly bills by commitOneItem).
// Soft-deleted bills are excluded by GORM's default DeletedAt scope.
// Line items are preloaded so CanFinalize can validate ErrNoLineItems
// without a per-bill round-trip.
func (r *billingRepository) ListBillsByBatchID(ctx context.Context, batchID uuid.UUID) ([]Bill, error) {
	var bills []Bill
	err := database.DB(ctx, r.db).
		Model(&Bill{}).
		Joins("JOIN bill_generation_batch_items i ON i.bill_id = bills.id").
		Where("i.batch_id = ? AND bills.bill_type = ?", batchID, BillTypeMonthly).
		Order("i.room_floor ASC, i.room_number ASC, bills.id ASC").
		Preload("LineItems").
		Find(&bills).Error
	return bills, err
}

// UpdateBatchItemCommitted sets bill_id on a batch item after successful bill creation.
// Must be called in the same tx as Create(bill).
func (r *billingRepository) UpdateBatchItemCommitted(ctx context.Context, itemID uuid.UUID, billID uuid.UUID) error {
	return database.DB(ctx, r.db).
		Model(&BillGenerationBatchItem{}).
		Where("id = ?", itemID).
		Update("bill_id", billID).Error
}

// UpdateBatchItemCommitError records a commit error on a batch item.
// Called in a separate tx from the failed bill creation (so rollback doesn't erase it).
func (r *billingRepository) UpdateBatchItemCommitError(ctx context.Context, itemID uuid.UUID, reasonText string) error {
	return database.DB(ctx, r.db).
		Model(&BillGenerationBatchItem{}).
		Where("id = ?", itemID).
		Updates(map[string]any{
			"reason_code": ReasonCodeCommitError,
			"reason_text": reasonText,
		}).Error
}

// UpdateBatchCommitStatus updates the commit_status and committed_at on the batch header.
func (r *billingRepository) UpdateBatchCommitStatus(ctx context.Context, batchID uuid.UUID, status CommitStatus, committedAt *time.Time) error {
	return database.DB(ctx, r.db).
		Model(&BillGenerationBatch{}).
		Where("id = ?", batchID).
		Updates(map[string]any{
			"commit_status": status,
			"committed_at":  committedAt,
		}).Error
}

// DeleteLineItemsBySource removes all line items with the given source from a bill.
func (r *billingRepository) DeleteLineItemsBySource(ctx context.Context, billID uuid.UUID, source LineItemSource) error {
	return database.DB(ctx, r.db).
		Where("bill_id = ? AND source = ?", billID, source).
		Delete(&BillLineItem{}).Error
}

// CreateLineItems batch-inserts line items.
func (r *billingRepository) CreateLineItems(ctx context.Context, items []BillLineItem) error {
	if len(items) == 0 {
		return nil
	}
	return database.DB(ctx, r.db).Create(&items).Error
}

// GetSummary returns aggregate bill counts and total amount, filtered by apartment + month.
// Uses the same base JOIN as FindAll so apartment filtering works consistently.
func (r *billingRepository) GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error) {
	query := r.baseJoinQuery(ctx)

	if params.ApartmentID != "" {
		query = query.Where("apartments.id = ?", params.ApartmentID)
	}
	if params.Month != "" {
		query = query.Where("bills.billing_month = ?", params.Month)
	}

	var result BillSummaryRaw
	err := query.Select(`
		COUNT(*) AS total_count,
		COUNT(*) FILTER (WHERE bills.status = 'FINALIZED') AS pending_count,
		COUNT(*) FILTER (WHERE bills.status = 'PAID') AS paid_count,
		COUNT(*) FILTER (WHERE bills.status = 'VOID') AS voided_count,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status != 'VOID'), 0) AS total_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status IN ('DRAFT', 'FINALIZED')), 0) AS pending_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status = 'PAID'), 0) AS paid_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status = 'VOID'), 0) AS voided_amount
	`).Scan(&result).Error
	if err != nil {
		return nil, err
	}
	return &result, nil
}
