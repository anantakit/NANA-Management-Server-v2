package billing

import (
	"context"
	"fmt"
	"time"

	"regexp"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var billingMonthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// feeLineTypes maps billing config fee types to bill line item types.
var feeLineTypes = map[billingconfig.FeeType]LineItemType{
	billingconfig.FeeTypeCleaningFee: LineItemCleaningFee,
	billingconfig.FeeTypeKeyService:  LineItemKeyService,
}

// feeDescriptions maps billing config fee types to Thai descriptions.
var feeDescriptions = map[billingconfig.FeeType]string{
	billingconfig.FeeTypeCleaningFee: "ค่าทำความสะอาด",
	billingconfig.FeeTypeKeyService:  "ค่าบริการกุญแจ",
}

type BillingService interface {
	List(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error)
	GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error)
	GetByID(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest) (*BillWithRelations, error)
	CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest) (*BillWithRelations, error)
	FinalizeBill(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest) (*BillWithRelations, error)
	MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error)
	BatchCreateMonthlyBills(ctx context.Context, req BatchCreateMonthlyBillsRequest, createdBy *uuid.UUID) (*BillGenerationBatch, error)
	CommitBatch(ctx context.Context, batchID uuid.UUID) (*CommitBatchResult, error)
	GetBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error)
	GetBatchItems(ctx context.Context, id uuid.UUID) ([]BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error)

	// Settlement draft editing
	UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest) (*BillWithRelations, error)

	// Move-out workflow ports (satisfies moveout.BillingCommander)
	GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time) (*moveout.SettlementBillResult, error)
	RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time) (*moveout.SettlementBillResult, error)
	FinalizeSettlement(ctx context.Context, billID uuid.UUID) error
	VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error
}

type billingService struct {
	repo      BillingRepository
	contracts ContractQuerier
	meters    MeterReadingQuerier
	configs   BillingConfigQuerier
	moveOuts  MoveOutQuerier
	tx        database.TxManager
}

var _ BillingService = (*billingService)(nil)

func NewBillingService(
	repo BillingRepository,
	contracts ContractQuerier,
	meters MeterReadingQuerier,
	configs BillingConfigQuerier,
	moveOuts MoveOutQuerier,
	tx database.TxManager,
) BillingService {
	return &billingService{
		repo:      repo,
		contracts: contracts,
		meters:    meters,
		configs:   configs,
		moveOuts:  moveOuts,
		tx:        tx,
	}
}

func (s *billingService) List(ctx context.Context, params BillListParams) ([]BillWithRelations, int64, error) {
	params.Normalize()
	return s.repo.FindAll(ctx, params)
}

func (s *billingService) GetSummary(ctx context.Context, params BillSummaryParams) (*BillSummaryRaw, error) {
	return s.repo.GetSummary(ctx, params)
}

func (s *billingService) GetByID(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("get bill: %w", err)
	}
	return b, nil
}

// CreateMonthlyBill generates a FINALIZED monthly bill.
// Monthly = ค่าห้องเดือนถัดไป (advance) + ค่าน้ำไฟเดือนนี้ (meter)
func (s *billingService) CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest) (*BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}
	if !billingMonthRe.MatchString(req.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	meterID, err := uuid.Parse(req.MeterReadingID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("meter_reading_id ไม่ถูกต้อง")
	}

	// Validate contract
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}
	if !c.IsActive() {
		return nil, ErrContractNotActive
	}

	// Check duplicate
	_, err = s.repo.FindByContractAndMonth(ctx, contractID, req.BillingMonth, BillTypeMonthly)
	if err == nil {
		return nil, ErrBillAlreadyExists
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}

	// Get and validate meter reading
	reading, err := s.meters.FindByIDSimple(ctx, meterID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMeterNotFound
		}
		return nil, fmt.Errorf("find meter: %w", err)
	}
	if !reading.IsMonthly() {
		return nil, ErrMeterTypeMismatch
	}
	if reading.RoomID != c.RoomID {
		return nil, ErrMeterRoomMismatch
	}
	if reading.BillingMonth == nil || *reading.BillingMonth != req.BillingMonth {
		return nil, ErrMeterMonthMismatch
	}

	snapshot := computeMonthlyBillSnapshot(req.BillingMonth,
		c.MonthlyRent, c.ElectricityRatePerUnit, c.WaterRatePerUnit, reading)

	bill := Bill{
		ContractID:   contractID,
		BillingMonth: req.BillingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems:    snapshot.ToLineItems(uuid.Nil),
		TotalAmount:  snapshot.TotalAmount,
	}
	if err := bill.Finalize(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Create(ctx, &bill); err != nil {
		return nil, fmt.Errorf("create monthly bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, bill.ID)
}

// CreateSettlementBill generates a DRAFT settlement bill for a move-out.
// Requires move-out notice to be COMPLETED (contract already ENDED, room VACANT).
// Settlement = pro-rate + EXIT meter + configurable fees + deposit netting
func (s *billingService) CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest) (*BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}

	// Get contract data (rates, deposit) — no status check here because
	// settlement runs after move-out complete (contract is already ENDED)
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}

	// Validate move-out notice — must be COMPLETED
	notice, err := s.moveOuts.FindActiveByContractID(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	if !notice.IsCompleted() {
		return nil, ErrMoveOutNotCompleted
	}

	moveOutDate := notice.ScheduledMoveOutDate
	billingMonth := toMonth(moveOutDate)

	// Check duplicate settlement
	_, err = s.repo.FindByContractAndMonth(ctx, contractID, billingMonth, BillTypeSettlement)
	if err == nil {
		return nil, ErrBillAlreadyExists
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}

	// Get EXIT meter reading
	exitReading, err := s.meters.FindLatestByRoomID(ctx, c.RoomID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrExitReadingMissing
		}
		return nil, fmt.Errorf("find exit reading: %w", err)
	}
	if !exitReading.IsExit() {
		return nil, ErrExitReadingMissing
	}

	// Build line items
	var items []BillLineItem
	order := 1

	// Rent adjustment: check if advance rent was already paid
	var rentPaid bool
	items, order, rentPaid, err = s.addRentAdjustment(ctx, items, order, c, moveOutDate)
	if err != nil {
		return nil, err
	}

	// Water + Electricity from EXIT reading
	waterUnits := exitReading.WaterUsed()
	elecUnits := exitReading.ElectricityUsed()
	items = append(items,
		NewWaterLine(waterUnits, c.WaterRatePerUnit,
			fmt.Sprintf("ค่าน้ำ %d หน่วย (ย้ายออก)", waterUnits), order),
	)
	order++
	items = append(items,
		NewElectricityLine(elecUnits, c.ElectricityRatePerUnit,
			fmt.Sprintf("ค่าไฟฟ้า %d หน่วย (ย้ายออก)", elecUnits), order),
	)
	order++

	// Configurable fees from billing_configs
	items, order, err = s.addConfigFees(ctx, items, order, c.RoomID)
	if err != nil {
		return nil, err
	}
	_ = order

	bill := Bill{
		ContractID:    contractID,
		BillingMonth:  billingMonth,
		BillType:      BillTypeSettlement,
		Status:        BillStatusDraft,
		RentPaid:      rentPaid,
		DepositAmount: effectiveDeposit(c, moveOutDate),
		LineItems:     items,
	}
	bill.CalculateTotal()

	// Void existing monthly bills for this month (within tx)
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.voidExistingMonthlyBills(txCtx, contractID, billingMonth); err != nil {
			return err
		}
		return s.repo.Create(txCtx, &bill)
	}); err != nil {
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, bill.ID)
}

func (s *billingService) FinalizeBill(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}

	if err := b.Finalize(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Update(ctx, b); err != nil {
		return nil, fmt.Errorf("finalize bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, b.ID)
}

func (s *billingService) VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest) (*BillWithRelations, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}

	if err := b.Void(req.Reason); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Update(ctx, b); err != nil {
		return nil, fmt.Errorf("void bill: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, b.ID)
}

func (s *billingService) MarkPaid(ctx context.Context, id uuid.UUID) (*BillWithRelations, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}

	if err := b.MarkPaid(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Update(ctx, b); err != nil {
		return nil, fmt.Errorf("mark paid: %w", err)
	}
	return s.repo.FindByIDWithRelations(ctx, b.ID)
}

// UpdateSettlementDraft replaces all MANUAL line items and updates the note
// on a DRAFT settlement bill. AUTO items are untouched.
func (s *billingService) UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest) (*BillWithRelations, error) {
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		b, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrBillNotFound
			}
			return fmt.Errorf("find bill: %w", err)
		}
		if !b.IsDraft() {
			return respond.ErrBadRequest.WithMessage(ErrNotDraft.Error())
		}
		if !b.IsSettlement() {
			return respond.ErrBadRequest.WithMessage("แก้ไขได้เฉพาะบิลสรุปยอด")
		}

		// Delete existing MANUAL items
		if err := s.repo.DeleteLineItemsBySource(txCtx, id, LineItemSourceManual); err != nil {
			return fmt.Errorf("delete manual items: %w", err)
		}

		// Validate + build new MANUAL items (sort after AUTO)
		for _, item := range req.ManualItems {
			if !IsValidManualLineType(LineItemType(item.LineType)) {
				return respond.ErrBadRequest.WithMessage(
					fmt.Sprintf("ประเภทรายการ %q ไม่สามารถเพิ่มเองได้", item.LineType))
			}
		}
		autoCount := 0
		for _, li := range b.LineItems {
			if li.IsAuto() {
				autoCount++
			}
		}
		baseOrder := autoCount + 1
		var manualItems []BillLineItem
		for i, item := range req.ManualItems {
			manualItems = append(manualItems, BillLineItem{
				BillID:      id,
				LineType:    LineItemType(item.LineType),
				Source:      LineItemSourceManual,
				Description: item.Description,
				Amount:      money.ToSatang(item.Amount),
				SortOrder:   baseOrder + i,
			})
		}
		if err := s.repo.CreateLineItems(txCtx, manualItems); err != nil {
			return fmt.Errorf("create manual items: %w", err)
		}

		// Reload line items and recompute totals
		reloaded, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("reload bill: %w", err)
		}
		reloaded.CalculateTotal()
		if req.Note != nil {
			reloaded.Note = *req.Note
		}
		return s.repo.Update(txCtx, reloaded)
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("update settlement draft: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, id)
}

// FinalizeSettlement recomputes totals from line items and marks the DRAFT
// settlement bill as FINALIZED. Called by the move-out service via port.
func (s *billingService) FinalizeSettlement(ctx context.Context, billID uuid.UUID) error {
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill: %w", err)
	}
	if !b.IsSettlement() {
		return respond.ErrBadRequest.WithMessage("ยืนยันได้เฉพาะบิลสรุปยอด")
	}

	// Recompute totals from source of truth
	b.CalculateTotal()

	if err := b.Finalize(); err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}
	return s.repo.Update(ctx, b)
}

// RegenerateSettlement voids the existing draft, creates a new DRAFT with
// fresh AUTO items, and preserves MANUAL items + note from the old bill.
func (s *billingService) RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time) (*moveout.SettlementBillResult, error) {
	// Load existing bill to extract MANUAL items + note
	existing, err := s.repo.FindByID(ctx, existingBillID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find existing bill: %w", err)
	}
	manualItems := existing.ManualItems()
	note := existing.Note

	// Void the existing bill
	if err := existing.Void("REGENERATED"); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("void existing bill: %w", err)
	}

	// Generate fresh AUTO items
	result, err := s.GenerateSettlement(ctx, contractID, moveOutDate)
	if err != nil {
		return nil, err
	}

	// Carry over MANUAL items + note to the new bill
	if len(manualItems) > 0 || note != "" {
		newBill, err := s.repo.FindByID(ctx, result.BillID)
		if err != nil {
			return nil, fmt.Errorf("reload new bill: %w", err)
		}

		// Assign MANUAL items to the new bill with correct sort order
		autoCount := len(newBill.LineItems)
		for i := range manualItems {
			manualItems[i].ID = uuid.Nil // new row
			manualItems[i].BillID = result.BillID
			manualItems[i].SortOrder = autoCount + 1 + i
		}
		if err := s.repo.CreateLineItems(ctx, manualItems); err != nil {
			return nil, fmt.Errorf("carry over manual items: %w", err)
		}

		// Reload + recompute with manual items included
		newBill, err = s.repo.FindByID(ctx, result.BillID)
		if err != nil {
			return nil, fmt.Errorf("reload new bill: %w", err)
		}
		newBill.Note = note
		newBill.CalculateTotal()
		if err := s.repo.Update(ctx, newBill); err != nil {
			return nil, fmt.Errorf("update new bill: %w", err)
		}

		// Recompute net amount with manual items included
		updated := toSettlementResult(result.BillID, newBill.TotalAmount, newBill.DepositAmount)
		result.NetAmount = updated.NetAmount
		result.DepositUsed = updated.DepositUsed
	}

	return result, nil
}

// addMonthsClamped adds N months (positive) to a date, clamping to end-of-month.
// e.g. Jan 31 + 1 month = Feb 28 (not Mar 3 like Go's AddDate).
// Only tested/used with months >= 1. Caller must guard months <= 0.
func addMonthsClamped(start time.Time, months int) time.Time {
	year, month, day := start.Date()
	loc := start.Location()

	totalMonths := int(month) - 1 + months
	targetYear := year + totalMonths/12
	targetMonth := time.Month(totalMonths%12 + 1)
	if totalMonths < 0 && totalMonths%12 != 0 {
		targetYear--
		targetMonth = time.Month(totalMonths%12 + 13)
	}

	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}

	return time.Date(targetYear, targetMonth, day,
		start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), loc)
}

// isDepositReturnable checks if the tenant stayed at least MinMonths from start date.
// Uses calendar-month clamp: Jan 31 + 1 month = Feb 28 (not Mar 3).
// Edge cases (e.g. 1 day short) are handled by admin override at the UI layer.
func isDepositReturnable(startDate time.Time, moveOutDate time.Time, minMonths int) bool {
	if moveOutDate.Before(startDate) {
		return false
	}
	if minMonths <= 0 {
		return true
	}
	eligibleAt := addMonthsClamped(startDate, minMonths)
	return !moveOutDate.Before(eligibleAt)
}

// effectiveDeposit returns the deposit amount to use in settlement.
// Returns 0 if tenant left before MinMonths (deposit forfeited).
func effectiveDeposit(c *contract.Contract, moveOutDate time.Time) int64 {
	if !isDepositReturnable(c.StartDate, moveOutDate, c.MinMonths) {
		return 0
	}
	return c.DepositAmount
}

// toSettlementResult computes net amount and deposit used from bill totals.
func toSettlementResult(billID uuid.UUID, totalAmount, depositAmount int64) *moveout.SettlementBillResult {
	netAmount := totalAmount - depositAmount
	depositUsed := depositAmount
	if totalAmount < depositAmount {
		depositUsed = totalAmount
	}
	if depositUsed < 0 {
		depositUsed = 0
	}
	return &moveout.SettlementBillResult{
		BillID:      billID,
		NetAmount:   netAmount,
		DepositUsed: depositUsed,
	}
}

// --- Private helpers ---

// addRentAdjustment implements the settlement rent rule:
//   - If advance rent for move-out month is PAID → rent = 0 (no refund, no charge)
//   - If NOT paid → prorate rent by used days (inclusive of move-out date)
//
// Day counting: move-out April 14 → used_days = 14 (days 1–14 inclusive)
//
// Rent coverage detection shortcut: bill M-1 contains advance rent for M.
// This works because the system bills rent one month in advance.
func (s *billingService) addRentAdjustment(ctx context.Context, items []BillLineItem, order int, c *contract.Contract, moveOutDate time.Time) ([]BillLineItem, int, bool, error) {
	billingMonth := toMonth(moveOutDate)

	hasPaid, err := s.repo.HasPaidAdvanceRentForMonth(ctx, c.ID, billingMonth)
	if err != nil {
		return nil, 0, false, fmt.Errorf("check advance rent: %w", err)
	}

	if hasPaid {
		// Advance rent already paid — no charge, no refund
		// ธุรกิจขายห้องเป็นรายเดือน จึงไม่คืนส่วนที่ไม่ได้อยู่
		return items, order, true, nil
	}

	// Not paid — prorate by used days (inclusive of move-out date)
	usedDays := moveOutDate.Day()
	totalDays := daysInMonth(moveOutDate)

	// Full month = no need to prorate (charge full rent)
	if usedDays >= totalDays {
		return items, order, false, nil
	}

	desc := fmt.Sprintf("ค่าห้อง %d วัน (คิดตามสัดส่วน)", usedDays)
	items = append(items, NewProrateRentLine(usedDays, totalDays, c.MonthlyRent, desc, order))
	order++
	return items, order, false, nil
}

// addConfigFees adds configurable fees (CLEANING_FEE, KEY_SERVICE) from billing_configs.
// Fallback policy: ถ้า apartment ยังไม่มี config row → ไม่เพิ่ม fee (ไม่ error)
// เพราะ config เป็น optional — admin สร้างได้ภายหลัง, บิลออกได้โดยไม่มี fee
func (s *billingService) addConfigFees(ctx context.Context, items []BillLineItem, order int, roomID uuid.UUID) ([]BillLineItem, int, error) {
	aptID, err := s.repo.FindApartmentIDByRoomID(ctx, roomID)
	if err != nil {
		return items, order, nil // skip fees if room lookup fails
	}

	configs, err := s.configs.FindByApartmentID(ctx, aptID)
	if err != nil {
		return nil, 0, fmt.Errorf("find billing configs: %w", err)
	}

	for _, cfg := range configs {
		if !cfg.IsActive || cfg.DefaultAmount <= 0 {
			continue
		}
		lt, ok := feeLineTypes[cfg.FeeType]
		if !ok {
			continue
		}
		desc := feeDescriptions[cfg.FeeType]
		items = append(items, NewFeeLine(lt, cfg.DefaultAmount, desc, order))
		order++
	}
	return items, order, nil
}

// voidExistingMonthlyBills voids non-paid monthly bills for settlement replacement.
func (s *billingService) voidExistingMonthlyBills(ctx context.Context, contractID uuid.UUID, billingMonth string) error {
	bills, err := s.repo.FindNonVoidedByContractAndMonth(ctx, contractID, billingMonth)
	if err != nil {
		return fmt.Errorf("find existing bills: %w", err)
	}

	for i := range bills {
		b := &bills[i]
		if b.BillType != BillTypeMonthly {
			continue
		}
		// PAID monthly bills are kept — their amount goes into PREPAID_CREDIT
		if b.IsPaid() {
			continue
		}
		// DRAFT or FINALIZED → VOID
		if err := b.Void("REPLACED_BY_SETTLEMENT"); err != nil {
			continue // skip if can't void (already void etc.)
		}
		if err := s.repo.Update(ctx, b); err != nil {
			return fmt.Errorf("void monthly bill %s: %w", b.ID, err)
		}
	}
	return nil
}

// --- Month helpers ---

func toMonth(t time.Time) string {
	return t.Format("2006-01")
}

func advanceMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}

func daysInMonth(t time.Time) int {
	y, m, _ := t.Date()
	return time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// --- Move-out workflow ports ---

// GenerateSettlement creates a DRAFT settlement bill for the given contract
// and move-out date. Called by the move-out service within its transaction
// context — must NOT start its own transaction.
//
// Settlement is a reconciliation, not a new bill:
//   - If advance rent for move-out month was PAID → no rent charge (no refund either)
//   - If NOT paid → prorate rent by used days (inclusive of move-out date)
//   - Utility charges from EXIT meter reading
//   - Configurable fees (cleaning, key service)
//   - Voids any existing monthly bills for the same month
func (s *billingService) GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time) (*moveout.SettlementBillResult, error) {
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}

	// Get EXIT reading for the room
	exitReading, err := s.meters.FindLatestByRoomID(ctx, c.RoomID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrExitReadingMissing
		}
		return nil, fmt.Errorf("find exit reading: %w", err)
	}
	if !exitReading.IsExit() {
		return nil, ErrExitReadingMissing
	}

	billingMonth := toMonth(moveOutDate)

	// Build line items
	var items []BillLineItem
	order := 1

	// Rent adjustment: check if advance rent was already paid
	var rentPaid bool
	items, order, rentPaid, err = s.addRentAdjustment(ctx, items, order, c, moveOutDate)
	if err != nil {
		return nil, err
	}

	// Water + Electricity from EXIT reading
	waterUnits := exitReading.WaterUsed()
	elecUnits := exitReading.ElectricityUsed()
	items = append(items,
		NewWaterLine(waterUnits, c.WaterRatePerUnit,
			fmt.Sprintf("ค่าน้ำ %d หน่วย (ย้ายออก)", waterUnits), order),
	)
	order++
	items = append(items,
		NewElectricityLine(elecUnits, c.ElectricityRatePerUnit,
			fmt.Sprintf("ค่าไฟฟ้า %d หน่วย (ย้ายออก)", elecUnits), order),
	)
	order++

	// Configurable fees
	items, order, err = s.addConfigFees(ctx, items, order, c.RoomID)
	if err != nil {
		return nil, err
	}
	_ = order

	bill := Bill{
		ContractID:    contractID,
		BillingMonth:  billingMonth,
		BillType:      BillTypeSettlement,
		Status:        BillStatusDraft,
		RentPaid:      rentPaid,
		DepositAmount: effectiveDeposit(c, moveOutDate),
		LineItems:     items,
	}
	bill.CalculateTotal()

	// Void existing monthly bills + create settlement (within caller's tx)
	if err := s.voidExistingMonthlyBills(ctx, contractID, billingMonth); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, &bill); err != nil {
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return toSettlementResult(bill.ID, bill.TotalAmount, bill.DepositAmount), nil
}

// VoidSettlement marks a settlement bill as VOIDED with the given reason.
// Called by the move-out service within its transaction context.
func (s *billingService) VoidSettlement(ctx context.Context, billID uuid.UUID, reason string) error {
	b, err := s.repo.FindByID(ctx, billID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrBillNotFound
		}
		return fmt.Errorf("find bill for void: %w", err)
	}
	if err := b.Void(reason); err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}
	return s.repo.Update(ctx, b)
}
