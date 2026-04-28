package moveout

import (
	"context"
	"fmt"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MoveOutRepository interface {
	FindAll(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error)
	FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error)
	FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*MoveOutNotice, error)
	HasActiveByContractID(ctx context.Context, contractID uuid.UUID) (bool, error)
	FindRoomIDsWithPendingNotice(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	ListActive(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error)
	ListHistory(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error)
	Create(ctx context.Context, notice *MoveOutNotice) error
	Update(ctx context.Context, notice *MoveOutNotice) error
}

type moveOutRepository struct {
	db *gorm.DB
}

var _ MoveOutRepository = (*moveOutRepository)(nil)

func NewMoveOutRepository(db *gorm.DB) MoveOutRepository {
	return &moveOutRepository{db: db}
}

func (r *moveOutRepository) baseJoinQuery(ctx context.Context) *gorm.DB {
	return database.DB(ctx, r.db).
		Model(&MoveOutNotice{}).
		Joins("JOIN contracts ON contracts.id = move_out_notices.contract_id AND contracts.deleted_at IS NULL").
		Joins("JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Joins("JOIN rooms ON rooms.id = contracts.room_id AND rooms.deleted_at IS NULL").
		Joins("JOIN apartments ON apartments.id = rooms.apartment_id AND apartments.deleted_at IS NULL").
		Joins("LEFT JOIN bills ON bills.id = move_out_notices.settlement_bill_id AND bills.deleted_at IS NULL").
		Where("move_out_notices.deleted_at IS NULL")
}

func (r *moveOutRepository) selectColumns() string {
	return `move_out_notices.*,
		contracts.room_id AS room_id,
		rooms.apartment_id AS apartment_id,
		tenants.full_name AS tenant_name,
		rooms.number AS room_number,
		apartments.name AS apartment_name,
		COALESCE(bills.settlement_rent_mode, '') AS settlement_rent_mode,
		COALESCE((SELECT COUNT(*) FROM bill_line_items WHERE bill_line_items.bill_id = bills.id AND bill_line_items.source = 'MANUAL'), 0) AS manual_item_count`
}

type joinRow struct {
	MoveOutNotice
	RoomID              uuid.UUID `gorm:"column:room_id"`
	ApartmentID         uuid.UUID `gorm:"column:apartment_id"`
	TenantName          string    `gorm:"column:tenant_name"`
	RoomNumber          string    `gorm:"column:room_number"`
	ApartmentName       string    `gorm:"column:apartment_name"`
	SettlementRentMode  string    `gorm:"column:settlement_rent_mode"`
	ManualItemCount     int       `gorm:"column:manual_item_count"`
}

func rowToRelation(row joinRow) MoveOutWithRelations {
	return MoveOutWithRelations{
		MoveOutNotice:      row.MoveOutNotice,
		RoomID:             row.RoomID,
		ApartmentID:        row.ApartmentID,
		TenantName:         row.TenantName,
		RoomNumber:         row.RoomNumber,
		ApartmentName:      row.ApartmentName,
		SettlementRentMode: row.SettlementRentMode,
		ManualItemCount:    row.ManualItemCount,
	}
}

func (r *moveOutRepository) FindAll(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error) {
	var total int64
	query := r.baseJoinQuery(ctx)

	if params.Status != "" {
		query = query.Where("move_out_notices.status = ?", params.Status)
	}
	if params.ApartmentID != "" {
		query = query.Where("rooms.apartment_id = ?", params.ApartmentID)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(tenants.full_name ILIKE ? OR rooms.number ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"notice_date", "scheduled_move_out_date", "status", "created_at"}, "created_at")
	orderClause := fmt.Sprintf("move_out_notices.%s %s", col, order)

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

	result := make([]MoveOutWithRelations, len(rows))
	for i, row := range rows {
		result[i] = rowToRelation(row)
	}
	return result, total, nil
}

func (r *moveOutRepository) FindByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var row joinRow
	err := r.baseJoinQuery(ctx).
		Select(r.selectColumns()).
		Where("move_out_notices.id = ?", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}
	result := rowToRelation(row)
	return &result, nil
}

func (r *moveOutRepository) FindByIDSimple(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error) {
	var m MoveOutNotice
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindByIDForUpdate fetches a notice and acquires a row-level lock (SELECT ... FOR UPDATE).
// Must be called inside a transaction — used by workflow commands to prevent
// lost updates from concurrent status transitions.
func (r *moveOutRepository) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*MoveOutNotice, error) {
	var m MoveOutNotice
	err := database.DB(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// FindActiveByContractID returns any non-CANCELLED notice for the contract.
// Includes COMPLETED — billing V1 CreateSettlementBill depends on finding
// COMPLETED notices through this method.
func (r *moveOutRepository) FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*MoveOutNotice, error) {
	var m MoveOutNotice
	err := database.DB(ctx, r.db).
		Where("contract_id = ? AND status != ?", contractID, MoveOutStatusCancelled).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *moveOutRepository) HasActiveByContractID(ctx context.Context, contractID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Model(&MoveOutNotice{}).
		Where("contract_id = ? AND status NOT IN ?", contractID, []MoveOutStatus{MoveOutStatusCompleted, MoveOutStatusCancelled}).
		Count(&count).Error
	return count > 0, err
}

func (r *moveOutRepository) FindRoomIDsWithPendingNotice(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	var results []struct {
		RoomID uuid.UUID `gorm:"column:room_id"`
	}
	err := database.DB(ctx, r.db).
		Table("move_out_notices").
		Select("DISTINCT contracts.room_id").
		Joins("JOIN contracts ON contracts.id = move_out_notices.contract_id AND contracts.deleted_at IS NULL").
		Where("move_out_notices.status NOT IN ? AND move_out_notices.deleted_at IS NULL AND contracts.room_id IN ?",
			[]MoveOutStatus{MoveOutStatusCompleted, MoveOutStatusCancelled}, roomIDs).
		Scan(&results).Error
	if err != nil {
		return nil, err
	}
	m := make(map[uuid.UUID]bool, len(results))
	for _, row := range results {
		m[row.RoomID] = true
	}
	return m, nil
}

// ListActive returns all non-terminal notices PLUS COMPLETED notices that
// were closed without recording a payment outcome (Phase-2 unsettled
// backlog). The service layer partitions by status, routing COMPLETED+nil
// rows into the pending_payment section so the financial backlog stays
// surfaced. Hard cap at 200.
func (r *moveOutRepository) ListActive(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error) {
	query := r.baseJoinQuery(ctx).
		Where("move_out_notices.status != ?", MoveOutStatusCancelled).
		Where("move_out_notices.status != ? OR move_out_notices.payment_outcome IS NULL", MoveOutStatusCompleted)

	if params.ApartmentID != "" {
		query = query.Where("rooms.apartment_id = ?", params.ApartmentID)
	}
	if params.Search != "" {
		s := "%" + params.Search + "%"
		query = query.Where("(tenants.full_name ILIKE ? OR rooms.number ILIKE ?)", s, s)
	}

	var rows []joinRow
	if err := query.
		Select(r.selectColumns()).
		Order("move_out_notices.scheduled_move_out_date ASC").
		Limit(200).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]MoveOutWithRelations, len(rows))
	for i, row := range rows {
		out[i] = rowToRelation(row)
	}
	return out, nil
}

// ListHistory returns terminal notices ordered by recency: CANCELLED, plus
// COMPLETED notices that have a payment_outcome on file. COMPLETED+nil rows
// stay in the active queue (financial backlog) and are intentionally
// excluded here so a notice never appears in both active and history.
// Capped at 100.
func (r *moveOutRepository) ListHistory(ctx context.Context, params MoveOutQueueParams) ([]MoveOutWithRelations, error) {
	query := r.baseJoinQuery(ctx).
		Where("move_out_notices.status = ? OR (move_out_notices.status = ? AND move_out_notices.payment_outcome IS NOT NULL)",
			MoveOutStatusCancelled, MoveOutStatusCompleted)

	if params.ApartmentID != "" {
		query = query.Where("rooms.apartment_id = ?", params.ApartmentID)
	}
	if params.Search != "" {
		s := "%" + params.Search + "%"
		query = query.Where("(tenants.full_name ILIKE ? OR rooms.number ILIKE ?)", s, s)
	}

	var rows []joinRow
	if err := query.
		Select(r.selectColumns()).
		Order("move_out_notices.updated_at DESC").
		Limit(100).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	result := make([]MoveOutWithRelations, len(rows))
	for i, row := range rows {
		result[i] = rowToRelation(row)
	}
	return result, nil
}

func (r *moveOutRepository) Create(ctx context.Context, notice *MoveOutNotice) error {
	return database.DB(ctx, r.db).Create(notice).Error
}

func (r *moveOutRepository) Update(ctx context.Context, notice *MoveOutNotice) error {
	return database.DB(ctx, r.db).Model(notice).Select("*").Omit("deleted_at").Updates(notice).Error
}
