package billing

import (
	"context"
	"fmt"
	"log/slog"
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
	PreflightMonthly(ctx context.Context, req MonthlyPreflightRequest) (*MonthlyPreflightResult, error)
	CommitBatch(ctx context.Context, batchID uuid.UUID) (*CommitBatchResult, error)
	GetBatchByID(ctx context.Context, id uuid.UUID) (*BillGenerationBatch, error)
	GetBatchItems(ctx context.Context, id uuid.UUID) ([]BatchItemWithTenant, error)
	ListBatches(ctx context.Context, params BatchListParams) ([]BillGenerationBatch, int64, error)

	// Settlement preview (non-persisting)
	PreviewSettlement(ctx context.Context, input PreviewSettlementInput) (*SettlementPreview, error)

	// Settlement draft editing
	UpdateSettlementDraft(ctx context.Context, id uuid.UUID, req UpdateSettlementDraftRequest) (*BillWithRelations, error)

	// Move-out workflow ports (satisfies moveout.BillingCommander + moveout.BillingQuerier)
	GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error)
	RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error)
	PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode moveout.RentMode) (*moveout.SettlementPreviewResult, error)
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

// PreviewSettlement computes settlement data without persisting anything.
// Calls prepareSettlementPlan (same path as create) but skips commitSettlementPlan.
func (s *billingService) PreviewSettlement(ctx context.Context, input PreviewSettlementInput) (*SettlementPreview, error) {
	// Resolve move-out date from notice (same as CreateSettlementBill)
	notice, err := s.moveOuts.FindActiveByContractID(ctx, input.ContractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := DefaultSettlementOptions()
	opts.SkipDuplicateGuard = true // preview is read-only — must work even when draft exists
	if input.RentMode != "" {
		opts.RentMode = input.RentMode
	}

	plan, err := s.prepareSettlementPlan(ctx, input.ContractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}

	return &SettlementPreview{
		Plan:                 plan,
		MinMonths:            plan.MinMonths,
		Returnable:           plan.DepositReturnable,
		MoveOutDate:          moveOutDate,
		EffectiveMoveOutDate: effectiveMoveOutDate(moveOutDate, opts.RentMode),
		RentMode:             opts.RentMode,
	}, nil
}

// CreateSettlementBill is the REST endpoint adapter (POST /bills/settlement).
// Thin adapter: resolves actual move-out date from notice, then delegates to
// the shared prepareSettlementPlan → commitSettlementPlan path.
//
// Same settlement semantics as GenerateSettlement (absorption, deposit, duplicate guard).
func (s *billingService) CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest) (*BillWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง")
	}

	// Resolve actual move-out date from notice
	notice, err := s.moveOuts.FindActiveByContractID(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrMoveOutNotFound
		}
		return nil, fmt.Errorf("find move-out: %w", err)
	}
	moveOutDate, err := notice.RequireActualDate()
	if err != nil {
		return nil, ErrActualDateRequired
	}

	opts := DefaultSettlementOptions()
	if req.RentMode != "" {
		opts.RentMode = SettlementRentMode(req.RentMode)
	}

	var billID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		plan, pErr := s.prepareSettlementPlan(txCtx, contractID, moveOutDate, opts)
		if pErr != nil {
			return pErr
		}
		result, cErr := s.commitSettlementPlan(txCtx, plan)
		if cErr != nil {
			return cErr
		}
		billID = result.BillID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return s.repo.FindByIDWithRelations(ctx, billID)
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
			li := BillLineItem{
				BillID:      id,
				LineType:    LineItemType(item.LineType),
				Source:      LineItemSourceManual,
				Description: item.Description,
				SortOrder:   baseOrder + i,
			}
			if item.Quantity != nil && item.UnitPrice != nil && *item.Quantity > 0 {
				// Quantity mode: compute amount from quantity × unit_price
				li.Quantity = *item.Quantity
				li.UnitPrice = money.ToSatang(*item.UnitPrice)
				li.Amount = int64(*item.Quantity) * li.UnitPrice
			} else {
				li.Amount = money.ToSatang(item.Amount)
			}
			manualItems = append(manualItems, li)
		}
		if err := s.repo.CreateLineItems(txCtx, manualItems); err != nil {
			return fmt.Errorf("create manual items: %w", err)
		}

		// Reload line items and recompute totals
		reloaded, err := s.repo.FindByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("reload bill: %w", err)
		}

		// Apply overrides
		if req.Overrides != nil {
			overrides := make(OverrideMap, len(req.Overrides))
			for key, amount := range req.Overrides {
				overrides[key] = money.ToSatang(amount)
			}
			reloaded.Overrides = overrides
			if err := reloaded.ValidateOverrides(); err != nil {
				return respond.ErrBadRequest.WithMessage(err.Error())
			}
		}

		// Apply deposit application
		if req.DepositApplication != nil {
			app := DepositApplication(*req.DepositApplication)
			if !app.IsValid() {
				return respond.ErrBadRequest.WithMessage("deposit_application ต้องเป็น FULL, NONE, หรือ CUSTOM")
			}
			reloaded.DepositApp = app
			if app == DepositAppCustom && req.CustomDepositApplied != nil {
				reloaded.CustomDepositApplied = money.ToSatang(*req.CustomDepositApplied)
			} else if app != DepositAppCustom {
				reloaded.CustomDepositApplied = 0
			}
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
// The persisted SettlementRentMode is carried over to the new bill.
func (s *billingService) RegenerateSettlement(ctx context.Context, existingBillID uuid.UUID, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error) {
	// Load existing bill to extract MANUAL items + note + rent mode + overrides + deposit app
	existing, err := s.repo.FindByID(ctx, existingBillID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find existing bill: %w", err)
	}
	manualItems := existing.ManualItems()
	note := existing.Note
	existingOverrides := existing.Overrides
	existingDepositApp := existing.DepositApp
	existingCustomDeposit := existing.CustomDepositApplied
	opts := SettlementOptions{RentMode: existing.SettlementRentMode}
	// Override rent mode if explicitly provided
	if rentMode != "" {
		opts.RentMode = SettlementRentMode(rentMode)
	}

	// Void the existing bill
	if err := existing.Void("REGENERATED"); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("void existing bill: %w", err)
	}

	// Restore absorbed bills so they can be re-absorbed
	if err := s.restoreAbsorbedBills(ctx, contractID); err != nil {
		return nil, fmt.Errorf("restore absorbed bills: %w", err)
	}

	// Generate fresh AUTO items with the same rent mode
	plan, err := s.prepareSettlementPlan(ctx, contractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}
	result, err := s.commitSettlementPlan(ctx, plan)
	if err != nil {
		return nil, err
	}

	// Carry over MANUAL items + note + overrides + deposit app to the new bill
	needsCarryOver := len(manualItems) > 0 || note != "" || len(existingOverrides) > 0 || existingDepositApp != DepositAppFull
	if needsCarryOver {
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
		if len(manualItems) > 0 {
			if err := s.repo.CreateLineItems(ctx, manualItems); err != nil {
				return nil, fmt.Errorf("carry over manual items: %w", err)
			}

			// Reload to include manual items in LineItems slice
			newBill, err = s.repo.FindByID(ctx, result.BillID)
			if err != nil {
				return nil, fmt.Errorf("reload new bill: %w", err)
			}
		}

		newBill.Note = note

		// Carry over overrides — prune keys that no longer match new AUTO items
		if len(existingOverrides) > 0 {
			carried := make(OverrideMap, len(existingOverrides))
			for k, v := range existingOverrides {
				carried[k] = v
			}
			newBill.Overrides = carried
			newBill.PruneStaleOverrides()
		}

		// Carry over deposit application
		newBill.DepositApp = existingDepositApp
		newBill.CustomDepositApplied = existingCustomDeposit

		newBill.CalculateTotal() // applies overrides + deposit app
		if err := s.repo.Update(ctx, newBill); err != nil {
			return nil, fmt.Errorf("update new bill: %w", err)
		}

		// Recompute net amount using single source of truth
		ds := computeDepositSettlementFromBill(newBill)
		updated := toSettlementResult(result.BillID, ds)
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

// DepositSettlementState holds the fully computed deposit breakdown for a settlement.
// Each field has a single, unambiguous meaning — no mixing of forfeiture and application.
type DepositSettlementState struct {
	OriginalAmount   int64 // deposit held by landlord (contract.DepositAmount)
	ForfeitedAmount  int64 // deposit lost due to early exit (0 if eligible)
	AvailableToApply int64 // OriginalAmount - ForfeitedAmount
	AppliedAmount    int64 // min(AvailableToApply, grossCharges) — offsets charges
	RefundAmount     int64 // AvailableToApply - AppliedAmount (>0 only if deposit returnable)
	AmountDue        int64 // grossCharges - AppliedAmount (>0 only if charges exceed available)
}

// computeDepositSettlement computes the full deposit state for a settlement.
//
// Rules:
//   - Returnable (min months met): full deposit available to offset charges, excess refunded
//   - Not returnable (early exit): deposit entirely forfeited — not applied to charges,
//     tenant must pay full amount
//
// DEPRECATED: Use computeDepositSettlementFromBill for new code.
// Kept for commitSettlementPlan where the bill is not yet persisted.
func computeDepositSettlement(depositAmount int64, grossCharges int64, returnable bool) DepositSettlementState {
	s := DepositSettlementState{
		OriginalAmount: depositAmount,
	}

	if !returnable {
		// Early exit: entire deposit forfeited, not applied to charges.
		// Tenant pays full charges.
		s.ForfeitedAmount = depositAmount
		s.AvailableToApply = 0
		s.AmountDue = grossCharges
		return s
	}

	// Returnable: full deposit available to offset charges
	s.AvailableToApply = depositAmount

	// Apply deposit to charges
	s.AppliedAmount = s.AvailableToApply
	if grossCharges < s.AppliedAmount {
		s.AppliedAmount = grossCharges
	}
	if s.AppliedAmount < 0 {
		s.AppliedAmount = 0
	}

	// Remaining deposit → refunded to tenant
	s.RefundAmount = s.AvailableToApply - s.AppliedAmount

	// Amount tenant still owes
	if grossCharges > s.AppliedAmount {
		s.AmountDue = grossCharges - s.AppliedAmount
	}

	return s
}

// computeDepositSettlementFromBill delegates to Bill.DepositBreakdown() as single source of truth.
// Use this for any bill that may have DepositApp or Overrides set.
func computeDepositSettlementFromBill(bill *Bill) DepositSettlementState {
	bd := bill.DepositBreakdown()

	// ForfeitedAmount = deposit lost due to contract terms (DepositForfeited flag).
	// WithheldAmount includes both forfeited AND admin-chosen NONE — only map forfeited here.
	var forfeited int64
	if bill.DepositForfeited {
		forfeited = bill.DepositAmount
	}

	// AvailableToApply = deposit minus forfeiture (what can be offset against charges).
	available := bill.DepositAmount - forfeited

	return DepositSettlementState{
		OriginalAmount:   bd.OriginalAmount,
		ForfeitedAmount:  forfeited,
		AvailableToApply: available,
		AppliedAmount:    bd.AppliedAmount,
		RefundAmount:     bd.RefundAmount,
		AmountDue:        bd.AmountDue,
	}
}

// toSettlementResult converts deposit state into the moveout result DTO.
func toSettlementResult(billID uuid.UUID, ds DepositSettlementState) *moveout.SettlementBillResult {
	netAmount := int64(0)
	if ds.AmountDue > 0 {
		netAmount = ds.AmountDue // positive = tenant pays
	} else if ds.RefundAmount > 0 {
		netAmount = -ds.RefundAmount // negative = refund to tenant
	}
	return &moveout.SettlementBillResult{
		BillID:      billID,
		NetAmount:   netAmount,
		DepositUsed: ds.AppliedAmount,
	}
}

// --- Settlement core (prepare/commit) ---

// settlementPlan holds computed settlement data before persistence.
// Used by both preview (read-only) and create (commit) paths.
type settlementPlan struct {
	Bill              Bill                   // DRAFT bill to create (line items + total computed)
	Deposit           DepositSettlementState // deposit breakdown
	BillsToVoid       []*Bill                // monthly bills to void (REPLACED_BY_SETTLEMENT)
	BillsToAbsorb     []*Bill                // unpaid bills to mark ABSORBED_BY_SETTLEMENT
	MinMonths         int                    // contract minimum stay (for preview display)
	DepositReturnable bool                   // whether deposit qualifies for return
}

// prepareSettlementPlan computes the full settlement without writing anything.
// All reads happen here; all writes happen in commitSettlementPlan.
func (s *billingService) prepareSettlementPlan(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, opts SettlementOptions) (*settlementPlan, error) {
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}

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

	// Duplicate guard: reject if a non-VOID settlement already exists.
	// Skipped for preview (read-only) which must work even when a draft exists.
	if !opts.SkipDuplicateGuard {
		existing, err := s.repo.FindByContractAndMonth(ctx, contractID, billingMonth, BillTypeSettlement)
		if err == nil && !existing.IsVoid() {
			return nil, ErrBillAlreadyExists
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("check existing settlement: %w", err)
		}
	}

	// Resolve apartment ID once — both addRentAdjustment (prorate rate
	// lookup) and addConfigFees (CLEANING_FEE/KEY_SERVICE lookup) need it.
	// The contract's room_id is non-nullable + the room must exist if
	// settlement reached this point, so a not-found here is an invariant
	// violation.
	apartmentID, err := s.repo.FindApartmentIDByRoomID(ctx, c.RoomID)
	if err != nil {
		return nil, fmt.Errorf("find apartment for room %s: %w", c.RoomID, err)
	}

	// Build line items
	var items []BillLineItem
	order := 1

	var rentPaid bool
	items, order, rentPaid, err = s.addRentAdjustment(ctx, items, order, c, moveOutDate, opts.RentMode, apartmentID)
	if err != nil {
		return nil, err
	}

	waterUnits := exitReading.WaterUsed()
	elecUnits := exitReading.ElectricityUsed()
	items = append(items,
		NewWaterLine(waterUnits, c.WaterRatePerUnit,
			fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits), order),
	)
	order++
	items = append(items,
		NewElectricityLine(elecUnits, c.ElectricityRatePerUnit,
			fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits), order),
	)
	order++

	items, order, err = s.addConfigFees(ctx, items, order, apartmentID)
	if err != nil {
		return nil, err
	}

	// Identify outstanding bills to absorb (read-only, no writes)
	var billsToAbsorb []*Bill
	items, order, billsToAbsorb, err = s.prepareAbsorption(ctx, items, order, contractID, billingMonth)
	if err != nil {
		return nil, err
	}
	_ = order

	// Identify monthly bills to void (read-only, no writes)
	billsToVoid, err := s.findBillsToVoid(ctx, contractID, billingMonth)
	if err != nil {
		return nil, err
	}

	bill := Bill{
		ContractID:         contractID,
		BillingMonth:       billingMonth,
		BillType:           BillTypeSettlement,
		Status:             BillStatusDraft,
		RentPaid:           rentPaid,
		DepositAmount:      c.DepositAmount,
		SettlementRentMode: opts.RentMode,
		LineItems:          items,
	}
	qualifyDate := effectiveMoveOutDate(moveOutDate, opts.RentMode)
	returnable := isDepositReturnable(c.StartDate, qualifyDate, c.MinMonths)
	bill.DepositForfeited = !returnable
	bill.CalculateTotal()

	deposit := computeDepositSettlement(c.DepositAmount, bill.TotalAmount, returnable)

	return &settlementPlan{
		Bill:              bill,
		Deposit:           deposit,
		BillsToVoid:       billsToVoid,
		BillsToAbsorb:     billsToAbsorb,
		MinMonths:         c.MinMonths,
		DepositReturnable: returnable,
	}, nil
}

// commitSettlementPlan persists a prepared settlement plan.
// Voids monthly bills, marks absorbed bills, creates the settlement bill.
// Must be called within a transaction context (caller manages tx).
func (s *billingService) commitSettlementPlan(ctx context.Context, plan *settlementPlan) (*moveout.SettlementBillResult, error) {
	// Void same-month monthly bills
	for _, b := range plan.BillsToVoid {
		if err := b.Void("REPLACED_BY_SETTLEMENT"); err != nil {
			continue
		}
		if err := s.repo.Update(ctx, b); err != nil {
			return nil, fmt.Errorf("void monthly bill %s: %w", b.ID, err)
		}
	}

	// Mark absorbed bills
	for _, b := range plan.BillsToAbsorb {
		if err := b.MarkAbsorbedBySettlement(); err != nil {
			continue
		}
		if err := s.repo.Update(ctx, b); err != nil {
			return nil, fmt.Errorf("mark absorbed bill %s: %w", b.ID, err)
		}
	}

	// Create the settlement bill
	if err := s.repo.Create(ctx, &plan.Bill); err != nil {
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return toSettlementResult(plan.Bill.ID, plan.Deposit), nil
}

// --- Private helpers ---

// addRentAdjustment implements the settlement rent rule:
//   - If advance rent for move-out month is PAID → rent = 0 (no refund, no charge)
//   - If NOT paid:
//   - PRORATED → prorate rent by actual days used at the apartment's
//     PRORATE_DAILY_RATE (flat rate × days)
//   - FULL_MONTH_KEEP_DEPOSIT → charge full-month rent
//
// Day counting: move-out April 14 → used_days = 14 (days 1–14 inclusive)
//
// Rent coverage detection shortcut: bill M-1 contains advance rent for M.
// This works because the system bills rent one month in advance.
//
// `apartmentID` is passed in (not looked up from `c.RoomID`) so the
// caller can share one apartment lookup with addConfigFees.
func (s *billingService) addRentAdjustment(ctx context.Context, items []BillLineItem, order int, c *contract.Contract, moveOutDate time.Time, rentMode SettlementRentMode, apartmentID uuid.UUID) ([]BillLineItem, int, bool, error) {
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

	// Full-month mode: charge full rent regardless of move-out day
	if rentMode == RentModeFullMonthKeepDeposit {
		desc := fmt.Sprintf("ค่าห้องเดือน %s", billingMonth)
		items = append(items, NewRoomRentLine(c.MonthlyRent, desc, order))
		order++
		return items, order, false, nil
	}

	// PRORATED — flat per-day rate × days used (inclusive of move-out date)
	usedDays := moveOutDate.Day()
	totalDays := daysInMonth(moveOutDate)

	// Full month = no need to prorate (charge full rent)
	if usedDays >= totalDays {
		return items, order, false, nil
	}

	rate, found, err := s.findProrateRate(ctx, apartmentID)
	if err != nil {
		return nil, 0, false, err
	}
	if !found {
		slog.Error("prorate rate config missing for apartment",
			"apartment_id", apartmentID, "contract_id", c.ID)
		return nil, 0, false, respond.ErrBadRequest.WithMessage(
			"กรุณาตั้งค่าเช่ารายวันก่อนสร้างบิลปิดสัญญา")
	}
	if rate <= 0 {
		slog.Error("prorate rate config invalid (zero or negative)",
			"apartment_id", apartmentID, "rate", rate)
		return nil, 0, false, respond.ErrBadRequest.WithMessage(
			"ค่าเช่ารายวันต้องมากกว่า ฿0 — กรุณาแก้ไขใน apartment settings")
	}

	desc := fmt.Sprintf("%d วัน × ฿%g/วัน", usedDays, money.ToBaht(rate))
	items = append(items, NewProrateRentLine(usedDays, rate, desc, order))
	order++
	return items, order, false, nil
}

// findProrateRate locates the apartment's PRORATE_DAILY_RATE config
// row. Returns:
//
//	(rate, true,  nil) → active config row found (rate may be 0; caller validates)
//	(0,    false, nil) → no row of this fee_type, or all candidates inactive
//	(0,    _,     err) → DB error
//
// Splitting "not found" from "found but invalid amount" lets the caller
// emit a more useful error log (admin error vs missing seed).
func (s *billingService) findProrateRate(ctx context.Context, apartmentID uuid.UUID) (rate int64, found bool, err error) {
	configs, err := s.configs.FindByApartmentID(ctx, apartmentID)
	if err != nil {
		return 0, false, fmt.Errorf("find billing configs: %w", err)
	}
	for _, cfg := range configs {
		if cfg.FeeType != billingconfig.FeeTypeProrateDailyRate {
			continue
		}
		if !cfg.IsActive {
			continue
		}
		return cfg.DefaultAmount, true, nil
	}
	return 0, false, nil
}

// addConfigFees adds configurable flat fees (CLEANING_FEE, KEY_SERVICE)
// from billing_configs. PRORATE_DAILY_RATE is intentionally NOT in
// `feeLineTypes` — it's a per-day rate consumed by addRentAdjustment,
// not a flat charge that should appear unconditionally.
//
// Graceful degradation is scoped to ONE case only:
//
//   - billing_configs is empty for the apartment → skip fees silently.
//     This is the "config is optional, admin may add later" case — the
//     for-loop below simply iterates zero times.
//
// `apartmentID` is passed in (not derived from roomID) so the caller
// can share one apartment lookup with addRentAdjustment.
//
// Fallback policy: ถ้า apartment ยังไม่มี config row → ไม่เพิ่ม fee (ไม่ error)
// เพราะ config เป็น optional — admin สร้างได้ภายหลัง, บิลออกได้โดยไม่มี fee
func (s *billingService) addConfigFees(ctx context.Context, items []BillLineItem, order int, apartmentID uuid.UUID) ([]BillLineItem, int, error) {
	configs, err := s.configs.FindByApartmentID(ctx, apartmentID)
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

// findBillsToVoid identifies non-paid monthly bills that should be voided when
// a settlement replaces them. Read-only — actual voiding happens in commitSettlementPlan.
func (s *billingService) findBillsToVoid(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]*Bill, error) {
	bills, err := s.repo.FindNonVoidedByContractAndMonth(ctx, contractID, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find existing bills: %w", err)
	}

	var toVoid []*Bill
	for i := range bills {
		b := &bills[i]
		if b.BillType != BillTypeMonthly {
			continue
		}
		// PAID monthly bills are kept — their amount goes into PREPAID_CREDIT
		if b.IsPaid() {
			continue
		}
		toVoid = append(toVoid, b)
	}
	return toVoid, nil
}

// prepareAbsorption identifies unpaid monthly bills to absorb and builds
// OUTSTANDING_BILL line items. Read-only — actual marking happens in commitSettlementPlan.
//
// Special case: bill M-1 (the one containing advance rent for move-out month M)
// only absorbs its utility portion because rent for M is already handled by prorate.
func (s *billingService) prepareAbsorption(ctx context.Context, items []BillLineItem, order int, contractID uuid.UUID, settlementMonth string) ([]BillLineItem, int, []*Bill, error) {
	unpaid, err := s.repo.FindUnpaidMonthlyByContractID(ctx, contractID)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("find unpaid bills: %w", err)
	}

	advanceRentBillMonth := previousMonth(settlementMonth)
	var toAbsorb []*Bill

	for i := range unpaid {
		b := &unpaid[i]
		// Same-month bills are replaced by findBillsToVoid, not absorbed
		if b.BillingMonth >= settlementMonth {
			continue
		}
		// Only absorb FINALIZED bills — DRAFT bills haven't been confirmed yet
		if !b.IsFinalized() {
			continue
		}

		var amount int64
		var desc string

		if b.BillingMonth == advanceRentBillMonth {
			// Bill M-1 contains advance rent for M (handled by prorate) — absorb utility only
			amount = absorbableUtilityTotal(b)
			if amount <= 0 {
				toAbsorb = append(toAbsorb, b) // still absorb, no line item
				continue
			}
			desc = fmt.Sprintf("ค่าน้ำ/ไฟค้าง %s", b.BillingMonth)
		} else {
			amount = b.TotalAmount
			if amount <= 0 {
				toAbsorb = append(toAbsorb, b)
				continue
			}
			desc = fmt.Sprintf("ยอดค้างเดือน %s", b.BillingMonth)
		}

		items = append(items, NewOutstandingBillLine(amount, desc, order))
		order++
		toAbsorb = append(toAbsorb, b)
	}

	return items, order, toAbsorb, nil
}

// restoreAbsorbedBills restores monthly bills that were voided by settlement absorption.
// Called when a settlement is voided (cancel or regeneration) so bills can be re-absorbed
// by a new settlement or collected normally if move-out is cancelled.
// Only touches bills where IsAbsorbedBySettlement() == true.
func (s *billingService) restoreAbsorbedBills(ctx context.Context, contractID uuid.UUID) error {
	bills, err := s.repo.FindAbsorbedByContractID(ctx, contractID)
	if err != nil {
		return fmt.Errorf("find absorbed bills: %w", err)
	}
	for i := range bills {
		b := &bills[i]
		if err := b.RestoreFromAbsorbed(); err != nil {
			continue // not absorbed — skip (defensive)
		}
		if err := s.repo.Update(ctx, b); err != nil {
			return fmt.Errorf("restore absorbed bill %s: %w", b.ID, err)
		}
	}
	return nil
}

// absorbableUtilityTotal sums line items from a bill that should be absorbed
// when the bill is the M-1 advance-rent bill (rent is handled separately by prorate).
//
// Type-safe: only known utility types are included. Unknown types are skipped
// to prevent silent incorrect billing.
func absorbableUtilityTotal(b *Bill) int64 {
	var total int64
	for _, li := range b.LineItems {
		switch li.LineType {
		case LineItemElectricity, LineItemWater:
			total += li.Amount
		case LineItemRoomRent, LineItemProrateRent:
			// Rent is recalculated as prorate in settlement — skip
		default:
			// CLEANING_FEE, KEY_SERVICE, PENALTY, OTHER, etc.
			// Do NOT absorb silently — these are not utility charges.
			// They will be lost from the voided bill. Admin can re-add as MANUAL items.
		}
	}
	return total
}

// --- Month helpers ---

func toMonth(t time.Time) string {
	return t.Format("2006-01")
}

func previousMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
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

// endOfMonth returns the last day of the month for the given date.
func endOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	lastDay := time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
	return time.Date(y, m, lastDay, 0, 0, 0, 0, t.Location())
}

// effectiveMoveOutDate returns the date used for deposit qualification.
// PRORATED uses the actual move-out date; FULL_MONTH_KEEP_DEPOSIT uses
// end-of-month so the tenant may reach MinMonths.
func effectiveMoveOutDate(moveOutDate time.Time, mode SettlementRentMode) time.Time {
	if mode == RentModeFullMonthKeepDeposit {
		return endOfMonth(moveOutDate)
	}
	return moveOutDate
}

// --- Move-out workflow ports ---

// GenerateSettlement creates a DRAFT settlement bill for the given contract
// and move-out date. Called by the move-out service within its transaction
// context — must NOT start its own transaction.
//
// Delegates to prepareSettlementPlan (read-only computation) then
// commitSettlementPlan (persistence). Same path as CreateSettlementBill.
func (s *billingService) GenerateSettlement(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, rentMode moveout.RentMode) (*moveout.SettlementBillResult, error) {
	opts := DefaultSettlementOptions()
	if rentMode != "" {
		opts.RentMode = SettlementRentMode(rentMode)
	}
	plan, err := s.prepareSettlementPlan(ctx, contractID, moveOutDate, opts)
	if err != nil {
		return nil, err
	}
	return s.commitSettlementPlan(ctx, plan)
}

// VoidSettlement marks a settlement bill as VOIDED with the given reason.
// Also restores any monthly bills that were absorbed by this settlement,
// so they can be re-absorbed by a new settlement or collected normally.
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
	if err := s.repo.Update(ctx, b); err != nil {
		return fmt.Errorf("void settlement: %w", err)
	}
	// Restore absorbed bills so they can be re-collected or re-absorbed
	return s.restoreAbsorbedBills(ctx, b.ContractID)
}

// PreviewSettlementForNotice satisfies moveout.BillingQuerier.
// Delegates to the existing PreviewSettlement and maps to the moveout result type.
func (s *billingService) PreviewSettlementForNotice(ctx context.Context, contractID uuid.UUID, rentMode moveout.RentMode) (*moveout.SettlementPreviewResult, error) {
	input := PreviewSettlementInput{ContractID: contractID}
	if rentMode != "" {
		input.RentMode = SettlementRentMode(rentMode)
	}

	preview, err := s.PreviewSettlement(ctx, input)
	if err != nil {
		return nil, err
	}

	plan := preview.Plan

	items := make([]moveout.SettlementPreviewLineItem, len(plan.Bill.LineItems))
	for i, li := range plan.Bill.LineItems {
		items[i] = moveout.SettlementPreviewLineItem{
			LineType:    string(li.LineType),
			Description: li.Description,
			Amount:      li.Amount,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			SortOrder:   li.SortOrder,
		}
	}

	absorbed := make([]moveout.SettlementPreviewAbsorbedBill, len(plan.BillsToAbsorb))
	for i, b := range plan.BillsToAbsorb {
		absorbed[i] = moveout.SettlementPreviewAbsorbedBill{
			BillID:       b.ID,
			BillingMonth: b.BillingMonth,
			TotalAmount:  b.TotalAmount,
		}
	}

	d := plan.Deposit
	var outcome string
	switch {
	case d.RefundAmount > 0:
		outcome = "REFUND"
	case d.AmountDue > 0:
		outcome = "PAY_MORE"
	default:
		outcome = "ZERO_BALANCE"
	}

	// Derive data quality state from the computed plan.
	dataState := moveout.DataStateComplete
	var warnings []string

	if len(plan.Bill.LineItems) == 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "ไม่มีรายการค่าใช้จ่าย — อาจยังไม่มีข้อมูลมิเตอร์หรือค่าเช่า")
	}

	allZero := len(plan.Bill.LineItems) > 0
	for _, li := range plan.Bill.LineItems {
		if li.Amount != 0 {
			allZero = false
			break
		}
	}
	if allZero && len(plan.Bill.LineItems) > 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "รายการค่าใช้จ่ายทั้งหมดเป็น ฿0 — อาจใช้ค่า default ในการคำนวณ")
	}

	// Only flag zero deposit when the contract actually has deposit but the
	// computed value is 0 — a contract with no deposit is business-valid.
	if plan.Bill.DepositAmount > 0 && d.OriginalAmount == 0 {
		dataState = moveout.DataStateIncomplete
		warnings = append(warnings, "สัญญามีเงินประกันแต่ไม่พบข้อมูลในการคำนวณ")
	}

	return &moveout.SettlementPreviewResult{
		BillingMonth:         plan.Bill.BillingMonth,
		ActualMoveOutDate:    preview.MoveOutDate,
		EffectiveMoveOutDate: preview.EffectiveMoveOutDate,
		RentMode:             string(preview.RentMode),
		RentPaid:             plan.Bill.RentPaid,
		MinMonths:            preview.MinMonths,
		DepositReturnable:    preview.Returnable,
		LineItems:            items,
		TotalAmount:          plan.Bill.TotalAmount,
		Deposit: moveout.SettlementPreviewDeposit{
			Original:  d.OriginalAmount,
			Forfeited: d.ForfeitedAmount,
			Applied:   d.AppliedAmount,
			Refund:    d.RefundAmount,
			Due:       d.AmountDue,
		},
		AbsorbedBills: absorbed,
		Outcome:       outcome,
		DataState:     dataState,
		Warnings:      warnings,
	}, nil
}
