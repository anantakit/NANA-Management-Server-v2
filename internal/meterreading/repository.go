package meterreading

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// temporalDateExpr returns a SQL expression that converts both reading types to a comparable date.
// MONTHLY → last day of billing_month (e.g. "2026-04" → 2026-04-30)
// EXIT → reading_date_actual as-is
// This ensures MONTHLY sorts AFTER EXIT readings in the same month,
// because a MONTHLY reading represents end-of-month coverage.
func temporalDateExpr(prefix string) string {
	if prefix != "" {
		prefix += "."
	}
	return fmt.Sprintf(
		`CASE WHEN %sreading_type = 'EXIT' THEN %sreading_date_actual `+
			`ELSE (TO_DATE(%sbilling_month, 'YYYY-MM') + INTERVAL '1 month - 1 day')::date END`,
		prefix, prefix, prefix,
	)
}

type MeterReadingRepository interface {
	FindAll(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*MeterReading, error)
	FindByRoomID(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error)
	FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	FindLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error)
	FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error)
	HasMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (bool, error)
	FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*MeterReading, error)
	FindExitByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
	Create(ctx context.Context, reading *MeterReading) error
	Update(ctx context.Context, reading *MeterReading) error
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error

	// Phase 7 baseline-correction surfaces.
	//
	// FindBaselineCorrectionByID loads a row matching id + anchor_reason =
	// READING_RECOVERY + scoped to the apartment + room. Returns
	// gorm.ErrRecordNotFound on mismatch (don't leak existence).
	FindBaselineCorrectionByID(ctx context.Context, apartmentID, roomID, id uuid.UUID) (*MeterReading, error)

	// FindLatestBaselineCorrectionByRoomID returns the most recent
	// READING_RECOVERY anchor row for the room (ORDER BY billing_month
	// DESC, created_at DESC). Used by the "latest baseline" invariant
	// guard for Soft Delete.
	FindLatestBaselineCorrectionByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error)
}

type meterReadingRepository struct {
	db *gorm.DB
}

func NewMeterReadingRepository(db *gorm.DB) MeterReadingRepository {
	return &meterReadingRepository{db: db}
}

func (r *meterReadingRepository) FindAll(ctx context.Context, apartmentID uuid.UUID, params ListParams) ([]MeterReadingWithRoom, int64, error) {
	var total int64

	query := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Joins("JOIN rooms ON rooms.id = meter_readings.room_id AND rooms.deleted_at IS NULL").
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("rooms.apartment_id = ? AND meter_readings.deleted_at IS NULL", apartmentID)

	if params.Month != "" {
		query = query.Where("meter_readings.billing_month = ?", params.Month)
	}
	if params.ReadingType != "" {
		query = query.Where("meter_readings.reading_type = ?", params.ReadingType)
	}
	if params.Search != "" {
		search := "%" + params.Search + "%"
		query = query.Where("(rooms.number ILIKE ? OR tenants.full_name ILIKE ?)", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := pagination.SafeSort(params.Sort, params.Order, []string{"billing_month", "room_number", "created_at"}, "billing_month")
	var orderCol string
	switch col {
	case "room_number":
		orderCol = "rooms.number"
	case "billing_month":
		orderCol = temporalDateExpr("meter_readings")
	default:
		orderCol = "meter_readings." + col
	}
	orderClause := fmt.Sprintf("%s %s", orderCol, order)

	type joinRow struct {
		MeterReading
		RoomNumber string `gorm:"column:room_number"`
		Floor      int    `gorm:"column:room_floor"`
		TenantName string `gorm:"column:tenant_name"`
	}

	var rows []joinRow
	err := query.
		Select(`meter_readings.*,
			rooms.number AS room_number,
			rooms.floor AS room_floor,
			COALESCE(tenants.full_name, '') AS tenant_name`).
		Order(orderClause).
		Offset(params.Offset()).
		Limit(params.Limit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	result := make([]MeterReadingWithRoom, len(rows))
	for i, row := range rows {
		result[i] = MeterReadingWithRoom{
			MeterReading: row.MeterReading,
			RoomNumber:   row.RoomNumber,
			Floor:        row.Floor,
			TenantName:   row.TenantName,
		}
	}
	return result, total, nil
}

func (r *meterReadingRepository) FindByID(ctx context.Context, id uuid.UUID) (*MeterReadingWithRoom, error) {
	type joinRow struct {
		MeterReading
		RoomNumber string `gorm:"column:room_number"`
		Floor      int    `gorm:"column:room_floor"`
		TenantName string `gorm:"column:tenant_name"`
	}

	var row joinRow
	err := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Select(`meter_readings.*,
			rooms.number AS room_number,
			rooms.floor AS room_floor,
			COALESCE(tenants.full_name, '') AS tenant_name`).
		Joins("JOIN rooms ON rooms.id = meter_readings.room_id AND rooms.deleted_at IS NULL").
		Joins("LEFT JOIN contracts ON contracts.room_id = rooms.id AND contracts.status = 'ACTIVE' AND contracts.deleted_at IS NULL").
		Joins("LEFT JOIN tenants ON tenants.id = contracts.tenant_id AND tenants.deleted_at IS NULL").
		Where("meter_readings.id = ? AND meter_readings.deleted_at IS NULL", id).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, gorm.ErrRecordNotFound
	}

	result := MeterReadingWithRoom{
		MeterReading: row.MeterReading,
		RoomNumber:   row.RoomNumber,
		Floor:        row.Floor,
		TenantName:   row.TenantName,
	}
	return &result, nil
}

func (r *meterReadingRepository) FindByIDSimple(ctx context.Context, id uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *meterReadingRepository) FindByRoomID(ctx context.Context, roomID uuid.UUID, params pagination.PaginationParams) ([]MeterReading, int64, error) {
	var total int64
	query := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Where("room_id = ? AND deleted_at IS NULL", roomID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortExpr := temporalDateExpr("")
	var readings []MeterReading
	err := query.
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr)).
		Offset(params.Offset()).
		Limit(params.Limit).
		Find(&readings).Error
	if err != nil {
		return nil, 0, err
	}
	return readings, total, nil
}

func (r *meterReadingRepository) FindLatestByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	sortExpr := temporalDateExpr("")
	var m MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr)).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// FindLatestByRoomIDBeforeDate finds the latest reading strictly before a given date.
// Used as settlement baseline for EXIT billing.
// excludeID prevents the EXIT reading itself from being returned as its own baseline.
func (r *meterReadingRepository) FindLatestByRoomIDBeforeDate(ctx context.Context, roomID uuid.UUID, before time.Time, excludeID *uuid.UUID) (*MeterReading, error) {
	sortExpr := temporalDateExpr("")
	q := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Where(fmt.Sprintf("%s < ?", sortExpr), before).
		Order(fmt.Sprintf("%s DESC, created_at DESC", sortExpr))

	if excludeID != nil {
		q = q.Where("id != ?", *excludeID)
	}

	var m MeterReading
	if err := q.First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// FindRecentByRoomIDs fetches the last N MONTHLY readings per room for the
// analytics baseline pool (median/anomaly threshold). EXIT readings excluded
// — partial-period usage, not regular patterns.
//
// ⚠ Analytics exclusion is NOT a lineage rule. ⚠
//
// The two filters below (anchor_reason IS NULL, NOT EXISTS subquery) are
// SPECIFIC TO THIS QUERY and must NOT be copy-pasted into other meter-reading
// queries. Lineage queries (FindLatestByRoomID, populatePrevious,
// FindLatestByRoomIDBeforeDate) MUST see recovery + source rows — the
// lineage-vs-analytics split is the project's load-bearing guard
// (feedback_recovery_lineage_vs_analytics_split.md, locked 2026-06-22).
// A1 (service_recovery_findlatest_pin_integration_test.go) catches accidental
// application of this filter to FindLatestByRoomID at PR time; B5
// (service_recovery_lineage_vs_analytics_symmetry_integration_test.go)
// catches accidental normalization of the two queries together.
//
// Reading Recovery exclusion (doctrine: feedback_reading_recovery_doctrine.md,
// locked 2026-06-22):
//
//   - anchor_reason IS NOT NULL → row is an anchor event (READING_RECOVERY
//     or PHYSICAL_REPLACEMENT), never a consumption month. Excluded.
//   - row is referenced by another row's recovery_source_reading_id → it's
//     a suspect source month whose recorded usage misled. Excluded.
//
// The NOT EXISTS subquery's `src.deleted_at IS NULL` filter naturally
// un-excludes a source when its recovery is soft-deleted (retracted) —
// matches the cancel/close lineage doctrine parallel.
//
// Index: idx_meter_readings_recovery_source (partial, WHERE
// recovery_source_reading_id IS NOT NULL — migration 00038) backs the
// NOT EXISTS subquery.
func (r *meterReadingRepository) FindRecentByRoomIDs(ctx context.Context, roomIDs []uuid.UUID, limit int) (map[uuid.UUID][]MeterReading, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID][]MeterReading{}, nil
	}

	var readings []MeterReading
	subQuery := database.DB(ctx, r.db).
		Table("meter_readings AS mr").
		Select("mr.*, ROW_NUMBER() OVER (PARTITION BY mr.room_id ORDER BY mr.billing_month DESC, mr.created_at DESC) AS rn").
		Where("mr.room_id IN ? AND mr.reading_type = 'MONTHLY' AND mr.deleted_at IS NULL", roomIDs).
		Where("mr.anchor_reason IS NULL").
		Where(`NOT EXISTS (
			SELECT 1 FROM meter_readings src
			WHERE src.recovery_source_reading_id = mr.id
			  AND src.deleted_at IS NULL
		)`)

	err := database.DB(ctx, r.db).
		Table("(?) AS sub", subQuery).
		Where("rn <= ?", limit).
		Order("room_id, rn").
		Find(&readings).Error
	if err != nil {
		return nil, err
	}

	result := make(map[uuid.UUID][]MeterReading, len(roomIDs))
	for _, reading := range readings {
		result[reading.RoomID] = append(result[reading.RoomID], reading)
	}
	return result, nil
}

// HasMonthlyByRoomAndMonth checks if a MONTHLY reading already exists for this room and month.
// Used to prevent creating EXIT readings when a MONTHLY for the same period already exists.
func (r *meterReadingRepository) HasMonthlyByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Model(&MeterReading{}).
		Where("room_id = ? AND reading_type = 'MONTHLY' AND billing_month = ? AND deleted_at IS NULL", roomID, month).
		Count(&count).Error
	return count > 0, err
}

// FindMonthlyByRoomsAndMonth bulk-fetches MONTHLY readings for multiple rooms in a single query.
// Returns map[roomID]*MeterReading. A room may have TWO MONTHLY rows for the same
// month — see the recovery-preference note below — so this is not strictly ≤1 per room.
func (r *meterReadingRepository) FindMonthlyByRoomsAndMonth(ctx context.Context, roomIDs []uuid.UUID, month string) (map[uuid.UUID]*MeterReading, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]*MeterReading{}, nil
	}
	var readings []MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id IN ? AND reading_type = ? AND billing_month = ? AND deleted_at IS NULL",
			roomIDs, ReadingTypeMonthly, month).
		Find(&readings).Error
	if err != nil {
		return nil, err
	}
	// A recovery month can hold two MONTHLY rows for the same room (migration
	// 00041): the consumption row (anchor_reason NULL) and the READING_RECOVERY
	// re-anchor row. The recovery month is a re-anchor event, NOT a consumption
	// month (feedback_reading_recovery_doctrine.md), so the recovery row governs
	// the bill (usage 0 + refund). Prefer it deterministically — otherwise the map
	// keeps whichever row Postgres returns last, and a coexisting consumption row
	// can shadow the recovery, silently dropping the refund (over-charge never
	// returned) or the re-baseline.
	result := make(map[uuid.UUID]*MeterReading, len(readings))
	for i := range readings {
		row := &readings[i]
		if existing := result[row.RoomID]; existing != nil &&
			existing.AnchorReason != nil && *existing.AnchorReason == AnchorReasonReadingRecovery {
			continue // recovery row already chosen — never downgrade to consumption
		}
		result[row.RoomID] = row
	}
	return result, nil
}

// FindExitByRoomID finds the active EXIT reading for a room.
// Returns gorm.ErrRecordNotFound if none exists.
func (r *meterReadingRepository) FindExitByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id = ? AND reading_type = ? AND deleted_at IS NULL", roomID, ReadingTypeExit).
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *meterReadingRepository) Create(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Create(reading).Error
}

func (r *meterReadingRepository) Update(ctx context.Context, reading *MeterReading) error {
	return database.DB(ctx, r.db).Model(reading).Select("*").Omit("deleted_at").Updates(reading).Error
}

// Delete soft-deletes the meter reading row by id (gorm.DeletedAt).
// Used by the Phase 7 baseline-correction Soft Delete path. Callers MUST
// apply ownership + state invariants at the service layer before invoking.
func (r *meterReadingRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).
		Where("id = ?", id).
		Delete(&MeterReading{}).Error
}

// FindBaselineCorrectionByID loads a READING_RECOVERY row scoped to
// (apartment, room, id) via JOIN on rooms. Returns gorm.ErrRecordNotFound
// on any mismatch — the caller MUST NOT leak existence (Phase 7 doctrine
// guard 1).
func (r *meterReadingRepository) FindBaselineCorrectionByID(ctx context.Context, apartmentID, roomID, id uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	err := database.DB(ctx, r.db).
		Joins("JOIN rooms ON rooms.id = meter_readings.room_id AND rooms.deleted_at IS NULL").
		Where("meter_readings.id = ?", id).
		Where("meter_readings.room_id = ?", roomID).
		Where("rooms.apartment_id = ?", apartmentID).
		Where("meter_readings.anchor_reason = ?", AnchorReasonReadingRecovery).
		Where("meter_readings.deleted_at IS NULL").
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// FindLatestBaselineCorrectionByRoomID returns the most recent
// READING_RECOVERY anchor row for the room. Sort: billing_month DESC,
// created_at DESC — matches the "latest baseline" doctrine guard.
func (r *meterReadingRepository) FindLatestBaselineCorrectionByRoomID(ctx context.Context, roomID uuid.UUID) (*MeterReading, error) {
	var m MeterReading
	err := database.DB(ctx, r.db).
		Where("room_id = ?", roomID).
		Where("anchor_reason = ?", AnchorReasonReadingRecovery).
		Where("deleted_at IS NULL").
		Order("billing_month DESC, created_at DESC").
		First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteExitByRoomID soft-deletes any active EXIT reading for a room.
// Idempotent: no-op if none exist. Used by move-out cancel to revert exit-meter prep
// so the workflow can be restarted cleanly (avoids unique-index collision on retry).
func (r *meterReadingRepository) DeleteExitByRoomID(ctx context.Context, roomID uuid.UUID) error {
	return database.DB(ctx, r.db).
		Where("room_id = ? AND reading_type = ? AND deleted_at IS NULL", roomID, ReadingTypeExit).
		Delete(&MeterReading{}).Error
}
