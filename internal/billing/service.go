package billing

import (
	"context"
	"fmt"
	"time"

	"regexp"

	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/shared/database"
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
	GetByID(ctx context.Context, id uuid.UUID) (*Bill, error)
	CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest) (*Bill, error)
	CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest) (*Bill, error)
	FinalizeBill(ctx context.Context, id uuid.UUID) (*Bill, error)
	VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest) (*Bill, error)
	MarkPaid(ctx context.Context, id uuid.UUID) (*Bill, error)
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

func (s *billingService) GetByID(ctx context.Context, id uuid.UUID) (*Bill, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("get bill: %w", err)
	}
	return b, nil
}

// CreateMonthlyBill generates a DRAFT monthly bill.
// Monthly = ค่าห้องเดือนถัดไป (advance) + ค่าน้ำไฟเดือนนี้ (meter)
func (s *billingService) CreateMonthlyBill(ctx context.Context, req CreateMonthlyBillRequest) (*Bill, error) {
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

	// Build line items
	nextMonth := advanceMonth(req.BillingMonth)
	elecUnits := reading.ElectricityUsed()
	waterUnits := reading.WaterUsed()
	items := []BillLineItem{
		NewRoomRentLine(c.MonthlyRent, fmt.Sprintf("ค่าห้อง %s", nextMonth), 1),
		NewElectricityLine(elecUnits, c.ElectricityRatePerUnit,
			fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits), 2),
		NewWaterLine(waterUnits, c.WaterRatePerUnit,
			fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits), 3),
	}

	bill := Bill{
		ContractID:   contractID,
		BillingMonth: req.BillingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft,
		LineItems:    items,
	}
	bill.CalculateTotal()

	if err := s.repo.Create(ctx, &bill); err != nil {
		return nil, fmt.Errorf("create monthly bill: %w", err)
	}

	return s.repo.FindByID(ctx, bill.ID)
}

// CreateSettlementBill generates a DRAFT settlement bill for a move-out.
// Requires move-out notice to be COMPLETED (contract already ENDED, room VACANT).
// Settlement = pro-rate + EXIT meter + configurable fees + deposit netting
func (s *billingService) CreateSettlementBill(ctx context.Context, req CreateSettlementBillRequest) (*Bill, error) {
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

	moveOutDate := notice.ActualMoveOutDate
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

	// Pro-rate rent (cross-month only)
	items, order = s.addProrateRent(items, order, c, moveOutDate)

	// Electricity + Water from EXIT reading
	elecUnits := exitReading.ElectricityUsed()
	waterUnits := exitReading.WaterUsed()
	items = append(items,
		NewElectricityLine(elecUnits, c.ElectricityRatePerUnit,
			fmt.Sprintf("ค่าไฟฟ้า %d หน่วย (ย้ายออก)", elecUnits), order),
	)
	order++
	items = append(items,
		NewWaterLine(waterUnits, c.WaterRatePerUnit,
			fmt.Sprintf("ค่าน้ำ %d หน่วย (ย้ายออก)", waterUnits), order),
	)
	order++

	// Configurable fees from billing_configs
	items, order, err = s.addConfigFees(ctx, items, order, c.RoomID)
	if err != nil {
		return nil, err
	}

	// PREPAID_CREDIT from previously paid monthly bills
	items, order, err = s.addPrepaidCredit(ctx, items, order, contractID, billingMonth)
	if err != nil {
		return nil, err
	}
	_ = order

	bill := Bill{
		ContractID:    contractID,
		BillingMonth:  billingMonth,
		BillType:      BillTypeSettlement,
		Status:        BillStatusDraft,
		DepositAmount: c.DepositAmount,
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

	return s.repo.FindByID(ctx, bill.ID)
}

func (s *billingService) FinalizeBill(ctx context.Context, id uuid.UUID) (*Bill, error) {
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
	return b, nil
}

func (s *billingService) VoidBill(ctx context.Context, id uuid.UUID, req VoidBillRequest) (*Bill, error) {
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
	return b, nil
}

func (s *billingService) MarkPaid(ctx context.Context, id uuid.UUID) (*Bill, error) {
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
	return b, nil
}

// --- Private helpers ---

// addProrateRent adds PRORATE_RENT line if move-out crosses into a month not yet paid.
// Pro-rate = days used in move-out month × (monthly_rent / days_in_month)
func (s *billingService) addProrateRent(items []BillLineItem, order int, c *contract.Contract, moveOutDate time.Time) ([]BillLineItem, int) {
	day := moveOutDate.Day()
	daysInMonth := daysInMonth(moveOutDate)

	// Only pro-rate if partial month (not full month)
	if day >= daysInMonth {
		return items, order
	}

	desc := fmt.Sprintf("ค่าห้อง %d วัน (คิดตามสัดส่วน)", day)
	items = append(items, NewProrateRentLine(day, daysInMonth, c.MonthlyRent, desc, order))
	order++
	return items, order
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

// addPrepaidCredit checks for PAID monthly bills and adds PREPAID_CREDIT line.
//
// IMPORTANT: ตอนนี้ใช้ SumPaidByContractSince (sum bills.total_amount WHERE status=PAID)
// เป็น interim solution เนื่องจาก payments feature ยังไม่มี
// เมื่อสร้าง payments แล้ว ต้องเปลี่ยนเป็น sum(payments.amount) เพื่อรองรับ partial payment
func (s *billingService) addPrepaidCredit(ctx context.Context, items []BillLineItem, order int, contractID uuid.UUID, billingMonth string) ([]BillLineItem, int, error) {
	paidTotal, err := s.repo.SumPaidByContractSince(ctx, contractID, billingMonth)
	if err != nil {
		return nil, 0, fmt.Errorf("sum paid bills: %w", err)
	}

	if paidTotal > 0 {
		desc := "หักค่าที่จ่ายล่วงหน้า"
		items = append(items, NewPrepaidCreditLine(paidTotal, desc, order))
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
