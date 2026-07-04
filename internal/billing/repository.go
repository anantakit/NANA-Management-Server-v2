package billing

import (
	"context"
	"fmt"
	"time"

	"nana/internal/contract"
	"nana/internal/meterreading"
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
	FindDraftBillForContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error)
	FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]Bill, error)
	FindApartmentIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error)
	FindRoomApartmentInfo(ctx context.Context, roomID uuid.UUID) (apartmentID uuid.UUID, roomNumber string, err error)
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

	// Batch finalize flow — reads bills associated with a batch. Batch-table
	// persistence itself lives in monthly.BatchRepository; this method stays
	// here because it queries the shared `bills` table.
	ListBillsByBatchID(ctx context.Context, batchID uuid.UUID) ([]Bill, error)

	// Per-month finalize flow (Phase 1D shell). Returns every non-VOID, non-
	// soft-deleted MONTHLY bill for (apartment, billing_month), preloading
	// LineItems so CanFinalize can validate ErrNoLineItems without a per-bill
	// round-trip. Filtering for DRAFT happens at the service layer to mirror
	// the BatchFinalizeAll idempotency semantics (already-FINALIZED rows
	// skipped silently, settlement rows guarded). The reconciliation Generate
	// path creates bills without a Batch wrapper per the anti-promotion
	// doctrine (service_generate.go), so the batch-scoped finalize query
	// can't find them — this method is the doctrine-aligned alternative.
	ListMonthlyBillsByApartmentMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string) ([]Bill, error)

	// Correction flow — row-locks the bill before void+recreate so concurrent
	// correction attempts on the same bill serialize cleanly. Caller MUST be
	// inside a TX (uses SELECT FOR UPDATE).
	LockBillForCorrection(ctx context.Context, billID uuid.UUID) (*Bill, error)

	// FindCorrectedFromBillID returns the ID of the VOID(CORRECTION) bill
	// that was replaced by this bill, or (nil, nil) when this bill is not
	// the replacement side of a correction chain. Single-row indexed lookup
	// — used by GetByID to surface the reverse link on the BillDrawer.
	FindCorrectedFromBillID(ctx context.Context, billID uuid.UUID) (*uuid.UUID, error)

	// Payment flow — row-locks the bill before recording payment so concurrent
	// payment attempts serialize cleanly. Caller MUST be inside a TX.
	LockBillForPayment(ctx context.Context, billID uuid.UUID) (*Bill, error)

	// FindPaymentsByBillIDs batch-loads payment records for a set of bill IDs.
	// Returns a map keyed by bill_id so list callers can merge without N+1.
	// Bills without a payment record are absent from the map (not an error).
	FindPaymentsByBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]*BillPaymentRecord, error)

	// HasNonVoidAdjustmentLineByRecoveryID is the inverse-FK probe used by
	// the baseline-correction applied-state derivation (Phase 7 doctrine
	// line 87–94 in feedback_reading_recovery_doctrine.md). Returns true
	// iff a non-VOID bill currently references the recovery row via an
	// ADJUSTMENT line item. Backs RecoveryAppliedChecker (meterreading
	// BillingApplicationChecker port).
	HasNonVoidAdjustmentLineByRecoveryID(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error)

	// HasNonVoidAdjustmentLineByRecoveryIDAndUtility is the Q1.5 per-utility
	// inverse-FK probe: true iff a non-VOID bill references the recovery row via
	// an ADJUSTMENT line for THAT utility. Electricity and water resolve
	// independently, so applied-state is derived per (recovery, utility).
	HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx context.Context, recoveryReadingID uuid.UUID, utility AdjustmentUtility) (bool, error)

	// HasUnresolvedOverRecordByContractID is the Q1.5 per-utility finalization
	// gate: true iff the contract's room has any AFFECTED (recorded > current)
	// recovery utility that lacks a non-VOID ADJUSTMENT line for that utility.
	// Only over-record utilities engage (recorded IS NULL or recorded <= current
	// never blocks — §0b). Supersedes HasPendingRecoveryByContractID once the
	// per-utility apply path lands (P3-B).
	HasUnresolvedOverRecordByContractID(ctx context.Context, contractID uuid.UUID) (bool, error)

	// CountPendingBaselineCorrectionsByRoomIDs returns, per room, the count
	// of active READING_RECOVERY meter rows that have NOT yet been applied
	// to any non-VOID bill. Mirrors the per-row applied-state derivation
	// in HasNonVoidAdjustmentLineByRecoveryID, batched by room for the
	// reconciliation workspace signal.
	//
	// Rooms with zero pending corrections are absent from the map (caller
	// reads as zero). Backs the ReconciliationAdapter's pending-count port
	// surfaced on the recon row.
	CountPendingBaselineCorrectionsByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]int, error)

	// HasPendingRecoveryByContractID is the scalar Q1 finalization-gate probe:
	// true iff the contract's room has any unresolved recovery (an active
	// READING_RECOVERY meter row with no non-VOID ADJUSTMENT line). Bills link
	// to a room via the contract, so the gate resolves room via a contracts
	// JOIN. Waived recoveries carry a zero-amount non-VOID ADJUSTMENT line →
	// resolved → do not block. No bill may finalize while this is true.
	HasPendingRecoveryByContractID(ctx context.Context, contractID uuid.UUID) (bool, error)
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
		TenantName      string     `gorm:"column:tenant_name"`
		RoomNumber      string     `gorm:"column:room_number"`
		ApartmentName   string     `gorm:"column:apartment_name"`
		ApartmentID     uuid.UUID  `gorm:"column:apartment_id"`
		DeliveryCount   int        `gorm:"column:delivery_count"`
		LastDeliveredAt *time.Time `gorm:"column:last_delivered_at"`
	}

	deliveryJoin := `LEFT JOIN LATERAL (
		SELECT COUNT(*)::int AS delivery_count, MAX(delivered_at) AS last_delivered_at
		FROM bill_deliveries WHERE bill_id = bills.id
	) bd ON true`
	deliveryCols := `, bd.delivery_count, bd.last_delivered_at`

	var rows []joinRow
	err := query.
		Joins(deliveryJoin).
		Select(r.selectColumns() + deliveryCols).
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
			Bill:            row.Bill,
			TenantName:      row.TenantName,
			RoomNumber:      row.RoomNumber,
			ApartmentName:   row.ApartmentName,
			ApartmentID:     row.ApartmentID,
			DeliveryCount:   row.DeliveryCount,
			LastDeliveredAt: row.LastDeliveredAt,
		}
	}

	// Batch-load payment data for PAID bills — one query, no N+1.
	var paidIDs []uuid.UUID
	for _, b := range result {
		if b.IsPaid() {
			paidIDs = append(paidIDs, b.ID)
		}
	}
	if len(paidIDs) > 0 {
		payments, err := r.FindPaymentsByBillIDs(ctx, paidIDs)
		if err != nil {
			return nil, 0, fmt.Errorf("load payment data for list: %w", err)
		}
		for i := range result {
			if pr, ok := payments[result[i].ID]; ok {
				result[i].PaidAt = &pr.PaidAt
				m := pr.Method
				result[i].PaymentMethod = &m
			}
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

	bwr := &BillWithRelations{
		Bill:          b,
		TenantName:    rel.TenantName,
		RoomNumber:    rel.RoomNumber,
		ApartmentName: rel.ApartmentName,
		ApartmentID:   rel.ApartmentID,
	}

	// Attach payment data for PAID bills (single-row lookup, no N+1).
	if b.IsPaid() {
		payments, err := r.FindPaymentsByBillIDs(ctx, []uuid.UUID{b.ID})
		if err != nil {
			return nil, fmt.Errorf("load payment data for bill %s: %w", b.ID, err)
		}
		if pr, ok := payments[b.ID]; ok {
			bwr.PaidAt = &pr.PaidAt
			m := pr.Method
			bwr.PaymentMethod = &m
			n := pr.Note
			bwr.PaymentNote = &n
		}
	}

	// Attach delivery aggregate — count and last-delivered-at.
	var deliveryAgg struct {
		Count  int        `gorm:"column:delivery_count"`
		LastAt *time.Time `gorm:"column:last_delivered_at"`
	}
	if err := database.DB(ctx, r.db).
		Table("bill_deliveries").
		Select("COUNT(*)::int AS delivery_count, MAX(delivered_at) AS last_delivered_at").
		Where("bill_id = ?", b.ID).
		Scan(&deliveryAgg).Error; err != nil {
		return nil, fmt.Errorf("load delivery data for bill %s: %w", b.ID, err)
	}
	bwr.DeliveryCount = deliveryAgg.Count
	bwr.LastDeliveredAt = deliveryAgg.LastAt

	return bwr, nil
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

// FindDraftBillForContractAndMonth finds the non-VOID DRAFT bill for a
// contract+billing_month+bill_type. Sibling of FindByContractAndMonth
// (which targets FINALIZED|PAID). Returns gorm.ErrRecordNotFound (use
// database.IsNotFound) when no DRAFT exists.
//
// Backed by unique index idx_bills_unique_monthly (00014) on
// (contract_id, billing_month) WHERE bill_type='MONTHLY' AND status!='VOID' —
// guarantees single-row result for MONTHLY bills (which is all Phase 5
// recovery consumes; SETTLEMENT-bill ADJUSTMENT is v1.1).
//
// Used by Phase 5 Reading Recovery commit (billing.RecoveryAdapter).
func (r *billingRepository) FindDraftBillForContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType BillType) (*Bill, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Where("contract_id = ? AND billing_month = ? AND bill_type = ? AND status = ?",
			contractID, billingMonth, billType, BillStatusDraft).
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

// FindRoomApartmentInfo returns the apartment_id and room number for a given room ID.
// Used by billing service to resolve payment routing at DRAFT creation time.
func (r *billingRepository) FindRoomApartmentInfo(ctx context.Context, roomID uuid.UUID) (uuid.UUID, string, error) {
	var row struct {
		ApartmentID string `gorm:"column:apartment_id"`
		Number      string `gorm:"column:number"`
	}
	err := database.DB(ctx, r.db).
		Table("rooms").
		Select("apartment_id", "number").
		Where("id = ? AND deleted_at IS NULL", roomID).
		Scan(&row).Error
	if err != nil {
		return uuid.Nil, "", err
	}
	if row.ApartmentID == "" {
		return uuid.Nil, "", gorm.ErrRecordNotFound
	}
	aptID, err := uuid.Parse(row.ApartmentID)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("parse apartment_id %q: %w", row.ApartmentID, err)
	}
	return aptID, row.Number, nil
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

// FindExistingByContractsAndMonth bulk-checks for an existing non-VOID MONTHLY
// bill per contract. Predicate mirrors the `idx_bills_unique_monthly` partial
// unique index exactly (bill_type = MONTHLY, status != VOID, deleted_at IS NULL —
// soft-delete filter applied automatically by GORM), so planner classification
// and commit-time INSERT agree on which contracts already have a bill.
//
// DRAFT bills count as existing — a correction-pending DRAFT (or any in-flight
// curation surface) blocks a fresh CREATE, just like the constraint does.
// Returns map[contractID]*Bill with ID always populated.
func (r *billingRepository) FindExistingByContractsAndMonth(ctx context.Context, contractIDs []uuid.UUID, month string) (map[uuid.UUID]*Bill, error) {
	if len(contractIDs) == 0 {
		return map[uuid.UUID]*Bill{}, nil
	}
	var bills []Bill
	err := database.DB(ctx, r.db).
		Where("contract_id IN ? AND billing_month = ? AND bill_type = ? AND status <> ?",
			contractIDs, month, BillTypeMonthly, BillStatusVoid).
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

// FindCorrectedFromBillID looks up the VOID(CORRECTION) bill that was
// replaced by the given bill. Returns (nil, nil) when this bill is not the
// replacement side of a correction chain (the common case — most bills).
//
// Read-only single indexed lookup on (superseded_by_bill_id, status,
// void_reason). Used by GetByID to populate the reverse-link hint on the
// BillDrawer; cost is one point query per detail fetch, which is acceptable
// because detail is opened per click, not in loops.
//
// The schema enforces a single-forward-link invariant (CHECK + partial
// UNIQUE on superseded_by_bill_id) so at most one VOID can supersede a
// given bill — Limit(1) here is defense-in-depth, not a real cap.
func (r *billingRepository) FindCorrectedFromBillID(ctx context.Context, billID uuid.UUID) (*uuid.UUID, error) {
	var raw string
	err := database.DB(ctx, r.db).
		Model(&Bill{}).
		Where("superseded_by_bill_id = ? AND status = ? AND void_reason = ?",
			billID, BillStatusVoid, voidReasonCorrection).
		Limit(1).
		Pluck("id", &raw).Error
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse corrected_from_bill_id %q: %w", raw, err)
	}
	return &id, nil
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

// ListMonthlyBillsByApartmentMonth returns every non-VOID, non-soft-deleted
// MONTHLY bill scoped to (apartment, billing_month). JOIN chain mirrors
// baseJoinQuery (contracts → rooms → apartments) to filter by apartment_id;
// VOID bills are excluded so a previously-voided correction doesn't
// resurface in the finalize loop. Soft-deleted bills excluded by the
// explicit `bills.deleted_at IS NULL` predicate. Line items preloaded so
// the service's CanFinalize check has everything it needs in one round-trip.
//
// Ordering by room floor + number gives the FE failure-list a deterministic
// order admins can scan top-to-bottom (matches BatchFinalizeAll's order).
func (r *billingRepository) ListMonthlyBillsByApartmentMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string) ([]Bill, error) {
	var bills []Bill
	err := database.DB(ctx, r.db).
		Model(&Bill{}).
		Joins("JOIN contracts ON contracts.id = bills.contract_id AND contracts.deleted_at IS NULL").
		Joins("JOIN rooms ON rooms.id = contracts.room_id AND rooms.deleted_at IS NULL").
		Joins("JOIN apartments ON apartments.id = rooms.apartment_id AND apartments.deleted_at IS NULL").
		Where("bills.deleted_at IS NULL").
		Where("apartments.id = ?", apartmentID).
		Where("bills.billing_month = ?", billingMonth).
		Where("bills.bill_type = ?", BillTypeMonthly).
		Where("bills.status <> ?", BillStatusVoid).
		Order("rooms.floor ASC, rooms.number ASC, bills.id ASC").
		Preload("LineItems").
		Find(&bills).Error
	return bills, err
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

// LockBillForPayment acquires a row-level lock (SELECT FOR UPDATE) on the bill.
// Must be called inside a transaction. No line-item preload — payment only
// needs the bill status and total_amount for validation.
func (r *billingRepository) LockBillForPayment(ctx context.Context, billID uuid.UUID) (*Bill, error) {
	var b Bill
	err := database.DB(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", billID).
		First(&b).Error
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// FindPaymentsByBillIDs batch-loads payment display data for a set of bill IDs.
// Returns a map keyed by bill_id. Bills without a payment record are absent.
// Empty input returns an empty map with no DB hit.
func (r *billingRepository) FindPaymentsByBillIDs(ctx context.Context, billIDs []uuid.UUID) (map[uuid.UUID]*BillPaymentRecord, error) {
	out := make(map[uuid.UUID]*BillPaymentRecord, len(billIDs))
	if len(billIDs) == 0 {
		return out, nil
	}
	var rows []BillPaymentRecord
	err := database.DB(ctx, r.db).
		Table("bill_payments").
		Select("bill_id, paid_at, method, note").
		Where("bill_id IN ?", billIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("batch load bill payments: %w", err)
	}
	for i := range rows {
		out[rows[i].BillID] = &rows[i]
	}
	return out, nil
}

// HasNonVoidAdjustmentLineByRecoveryID returns true iff a non-VOID bill
// currently references the given recovery meter row via an ADJUSTMENT
// line item (single COUNT/EXISTS-equivalent query).
//
// Phase 7 (locked 2026-06-25): canonical derivation site for baseline
// correction applied state. VOIDed bills (including correction-supersede
// VOIDs) DO NOT count — recovery returns to PENDING on bill correction.
//
// bill_line_items has no soft-delete column (lines live or die with
// their parent bill), so the only deleted_at filter is the parent's.
// Index path: bill_line_items(adjustment_recovery_reading_id) — partial
// index in migration 00041.
func (r *billingRepository) HasNonVoidAdjustmentLineByRecoveryID(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Table("bill_line_items AS li").
		Joins("JOIN bills b ON b.id = li.bill_id").
		Where("li.adjustment_recovery_reading_id = ?", recoveryReadingID).
		Where("b.status <> ?", BillStatusVoid).
		Where("b.deleted_at IS NULL").
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check applied adjustment line: %w", err)
	}
	return count > 0, nil
}

// HasNonVoidAdjustmentLineByRecoveryIDAndUtility is the Q1.5 per-utility variant
// of the probe above — same "applied" definition, additionally scoped to the
// utility so electricity and water resolve independently.
func (r *billingRepository) HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx context.Context, recoveryReadingID uuid.UUID, utility AdjustmentUtility) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).
		Table("bill_line_items AS li").
		Joins("JOIN bills b ON b.id = li.bill_id").
		Where("li.adjustment_recovery_reading_id = ?", recoveryReadingID).
		Where("li.adjustment_utility = ?", utility).
		Where("b.status <> ?", BillStatusVoid).
		Where("b.deleted_at IS NULL").
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check applied adjustment line by utility: %w", err)
	}
	return count > 0, nil
}

// HasUnresolvedOverRecordByContractID is the Q1.5 per-utility finalization gate.
// It enumerates the AFFECTED (recorded > current) utilities of the contract's
// recovery rows via a UNION ALL, then returns true iff any such (recovery,
// utility) pair lacks a non-VOID ADJUSTMENT line for that utility. A NOT EXISTS
// (not a LEFT JOIN) is required because a pair can hold both a VOID and a
// non-VOID line at once; only the non-VOID one resolves it. Waived pairs carry a
// zero-amount non-VOID line → resolved. Utilities with recorded IS NULL or
// recorded <= current never enter the set (not an over-record — §0b).
func (r *billingRepository) HasUnresolvedOverRecordByContractID(ctx context.Context, contractID uuid.UUID) (bool, error) {
	var exists bool
	err := database.DB(ctx, r.db).
		Raw(`SELECT EXISTS (
			SELECT 1 FROM (
				SELECT mr.id AS recovery_id, ? AS utility
				FROM meter_readings mr
				JOIN contracts c ON c.room_id = mr.room_id
				WHERE c.id = ?
				  AND mr.anchor_reason = ?
				  AND mr.deleted_at IS NULL
				  AND mr.electricity_recorded IS NOT NULL
				  AND mr.electricity_recorded > mr.electricity_current
				UNION ALL
				SELECT mr.id AS recovery_id, ? AS utility
				FROM meter_readings mr
				JOIN contracts c ON c.room_id = mr.room_id
				WHERE c.id = ?
				  AND mr.anchor_reason = ?
				  AND mr.deleted_at IS NULL
				  AND mr.water_recorded IS NOT NULL
				  AND mr.water_recorded > mr.water_current
			) affected
			WHERE NOT EXISTS (
				SELECT 1
				FROM bill_line_items bli
				JOIN bills b ON b.id = bli.bill_id
				WHERE bli.adjustment_recovery_reading_id = affected.recovery_id
				  AND bli.adjustment_utility = affected.utility
				  AND b.status <> ?
				  AND b.deleted_at IS NULL
			)
		)`,
			AdjustmentUtilityElectricity, contractID, meterreading.AnchorReasonReadingRecovery,
			AdjustmentUtilityWater, contractID, meterreading.AnchorReasonReadingRecovery,
			BillStatusVoid).
		Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check unresolved over-record by contract: %w", err)
	}
	return exists, nil
}

// CountPendingBaselineCorrectionsByRoomIDs is the batch sibling of
// HasNonVoidAdjustmentLineByRecoveryID — grouped per room. Returns the
// count of active READING_RECOVERY meter rows for each room that have
// NOT yet been applied to a non-VOID bill.
//
// Same "applied" definition as the per-row probe above so the recon
// workspace signal stays consistent with the BillEditDrawer pending
// list. The LEFT JOIN matches a non-VOID applied line and the
// `b.id IS NULL` filter keeps only unapplied recoveries.
//
// Rooms with zero pending corrections are absent from the map (caller
// reads absence as zero). Empty roomIDs short-circuits to empty map.
func (r *billingRepository) CountPendingBaselineCorrectionsByRoomIDs(ctx context.Context, roomIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	if len(roomIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	type row struct {
		RoomID uuid.UUID `gorm:"column:room_id"`
		Cnt    int       `gorm:"column:cnt"`
	}
	var rows []row
	err := database.DB(ctx, r.db).
		Table("meter_readings AS mr").
		Joins("LEFT JOIN bill_line_items bli ON bli.adjustment_recovery_reading_id = mr.id").
		Joins("LEFT JOIN bills b ON b.id = bli.bill_id AND b.status <> ? AND b.deleted_at IS NULL", BillStatusVoid).
		Where("mr.room_id IN ?", roomIDs).
		Where("mr.anchor_reason = ?", meterreading.AnchorReasonReadingRecovery).
		Where("mr.deleted_at IS NULL").
		Where("b.id IS NULL").
		Select("mr.room_id AS room_id, COUNT(*)::int AS cnt").
		Group("mr.room_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count pending baseline corrections: %w", err)
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.RoomID] = r.Cnt
	}
	return out, nil
}

// HasPendingRecoveryByContractID is the scalar finalization-gate probe: true
// iff the contract's room has a READING_RECOVERY meter row with NO non-VOID
// ADJUSTMENT line (the inverse of HasNonVoidAdjustmentLineByRecoveryID's
// "applied" definition — so the two agree exactly). A NOT EXISTS subquery
// (not a LEFT JOIN + IS NULL) is required: a recovery can hold BOTH a VOID and
// a non-VOID line at once (correction voids then re-resolves), and only the
// non-VOID one resolves it.
func (r *billingRepository) HasPendingRecoveryByContractID(ctx context.Context, contractID uuid.UUID) (bool, error) {
	var exists bool
	err := database.DB(ctx, r.db).
		Raw(`SELECT EXISTS (
			SELECT 1
			FROM meter_readings mr
			JOIN contracts c ON c.room_id = mr.room_id
			WHERE c.id = ?
			  AND mr.anchor_reason = ?
			  AND mr.deleted_at IS NULL
			  AND NOT EXISTS (
				SELECT 1
				FROM bill_line_items bli
				JOIN bills b ON b.id = bli.bill_id
				WHERE bli.adjustment_recovery_reading_id = mr.id
				  AND b.status <> ?
				  AND b.deleted_at IS NULL
			  )
		)`, contractID, meterreading.AnchorReasonReadingRecovery, BillStatusVoid).
		Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check pending recovery by contract: %w", err)
	}
	return exists, nil
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
	if params.BillType != "" {
		query = query.Where("bills.bill_type = ?", params.BillType)
	}

	var result BillSummaryRaw
	err := query.Select(`
		COUNT(*) AS total_count,
		COUNT(*) FILTER (WHERE bills.status = 'FINALIZED') AS pending_count,
		COUNT(*) FILTER (WHERE bills.status = 'PAID') AS paid_count,
		COUNT(*) FILTER (WHERE bills.status = 'VOID') AS voided_count,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status != 'VOID'), 0) AS total_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status = 'FINALIZED'), 0) AS pending_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status = 'PAID'), 0) AS paid_amount,
		COALESCE(SUM(bills.total_amount) FILTER (WHERE bills.status = 'VOID'), 0) AS voided_amount
	`).Scan(&result).Error
	if err != nil {
		return nil, err
	}
	return &result, nil
}
