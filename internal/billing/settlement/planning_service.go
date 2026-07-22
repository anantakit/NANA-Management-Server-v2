package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// feeLineTypes maps billing config fee types to bill line item types.
var feeLineTypes = map[billingconfig.FeeType]billing.LineItemType{
	billingconfig.FeeTypeCleaningFee: billing.LineItemCleaningFee,
	billingconfig.FeeTypeKeyService:  billing.LineItemKeyService,
}

// feeDescriptions maps billing config fee types to Thai descriptions.
var feeDescriptions = map[billingconfig.FeeType]string{
	billingconfig.FeeTypeCleaningFee: "ค่าทำความสะอาด",
	billingconfig.FeeTypeKeyService:  "ค่าบริการกุญแจ",
}

// settlementPlan holds computed settlement data before persistence.
// Used by both preview (read-only) and create (commit) paths.
type settlementPlan struct {
	Bill              billing.Bill           // DRAFT bill to create (line items + total computed)
	Deposit           DepositSettlementState // deposit breakdown
	BillsToVoid       []*billing.Bill        // monthly bills to void (REPLACED_BY_SETTLEMENT)
	BillsToAbsorb     []*billing.Bill        // unpaid bills to mark ABSORBED_BY_SETTLEMENT
	MinMonths         int                    // contract minimum stay (for preview display)
	DepositReturnable bool                   // whether deposit qualifies for return
}

// prepareSettlementPlan computes the full settlement without writing anything.
// All reads happen here; all writes happen in commitSettlementPlan.
func (s *Service) prepareSettlementPlan(ctx context.Context, contractID uuid.UUID, moveOutDate time.Time, opts billing.SettlementOptions) (*settlementPlan, error) {
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, billing.ErrContractNotFound
		}
		return nil, fmt.Errorf("find contract: %w", err)
	}

	// Fetch the exit reading BY TYPE (not "latest"): an Epic B recovery row that
	// coexists with this move-out can out-rank the exit reading temporally, so a
	// latest-lookup would return the recovery and spuriously report a missing
	// exit reading.
	exitReading, err := s.meters.FindExitByRoomID(ctx, c.RoomID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrExitReadingMissing
		}
		return nil, fmt.Errorf("find exit reading: %w", err)
	}

	billingMonth := billing.ToMonth(moveOutDate)

	// Duplicate guard: reject if a non-VOID settlement already exists.
	// Skipped for preview (read-only) which must work even when a draft exists.
	if !opts.SkipDuplicateGuard {
		existing, err := s.bills.FindByContractAndMonth(ctx, contractID, billingMonth, billing.BillTypeSettlement)
		if err == nil && !existing.IsVoid() {
			return nil, billing.ErrBillAlreadyExists
		}
		if err != nil && !database.IsNotFound(err) {
			return nil, fmt.Errorf("check existing settlement: %w", err)
		}
	}

	// Resolve apartment ID once — both addRentAdjustment (prorate rate
	// lookup) and addConfigFees (CLEANING_FEE/KEY_SERVICE lookup) need it.
	// The contract's room_id is non-nullable + the room must exist if
	// settlement reached this point, so a not-found here is an invariant
	// violation.
	apartmentID, err := s.bills.FindApartmentIDByRoomID(ctx, c.RoomID)
	if err != nil {
		return nil, fmt.Errorf("find apartment for room %s: %w", c.RoomID, err)
	}

	// Build line items
	var items []billing.BillLineItem
	order := 1

	var rentPaid bool
	items, order, rentPaid, err = s.addRentAdjustment(ctx, items, order, c, moveOutDate, opts.RentMode, apartmentID)
	if err != nil {
		return nil, err
	}

	// Replace Meter — settlement consumes the SAME CanonicalPeriodUsage as Monthly
	// (parity). A replacement earlier in the settlement period contributes its
	// old-meter tail; the exit reading is the new-meter segment. ONE result drives
	// the charge (Total) and the frozen breakdown (Segments).
	replacements, err := s.meters.FindReplacementAnchorsByRoomAndMonth(ctx, c.RoomID, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find replacement events: %w", err)
	}
	if cErr := billing.DetectRecoveryReplacementCollision(exitReading, replacements); cErr != nil {
		return nil, cErr // R-b guard: surface, never silently under-bill
	}
	elecUsage, waterUsage := meterreading.CanonicalPeriodUsage(exitReading, replacements)

	waterLine := billing.NewWaterLine(waterUsage.Total, c.WaterRatePerUnit,
		fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUsage.Total), order)
	billing.SetMeteredLineUsage(&waterLine, waterUsage)
	items = append(items, waterLine)
	order++
	elecLine := billing.NewElectricityLine(elecUsage.Total, c.ElectricityRatePerUnit,
		fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUsage.Total), order)
	billing.SetMeteredLineUsage(&elecLine, elecUsage)
	items = append(items, elecLine)
	order++

	// Q1.6 settlement recovery reconciliation (Epic B). Settlement OWNS the
	// move-out forward credit (DQ0): it discovers the contract's unreflected
	// over-records and emits one refund ADJUSTMENT line per (recovery, utility),
	// priced at the SOURCE bill's unit_price (HC6). This is the SINGLE recovery
	// emission path (INV-3) — the old EXIT-reading branch is gone; Regenerate /
	// Correct reflect only by re-running this builder. Discovery shares ONE
	// predicate with the finalize gate (INV-1), so exactly the pairs the gate
	// would block on are the pairs emitted here — resolving the D1 deadlock.
	items, order, err = s.addRecoveryRefunds(ctx, items, order, contractID)
	if err != nil {
		return nil, err
	}

	items, order, err = s.addConfigFees(ctx, items, order, apartmentID)
	if err != nil {
		return nil, err
	}

	// Identify outstanding bills to absorb (read-only, no writes)
	var billsToAbsorb []*billing.Bill
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

	bill := billing.Bill{
		ContractID:         contractID,
		BillingMonth:       billingMonth,
		BillType:           billing.BillTypeSettlement,
		Status:             billing.BillStatusDraft,
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
func (s *Service) commitSettlementPlan(ctx context.Context, plan *settlementPlan) (*moveout.SettlementBillResult, error) {
	// Void same-month monthly bills
	for _, b := range plan.BillsToVoid {
		if err := b.Void("REPLACED_BY_SETTLEMENT"); err != nil {
			continue
		}
		if err := s.bills.UpdateBill(ctx, b); err != nil {
			return nil, fmt.Errorf("void monthly bill %s: %w", b.ID, err)
		}
	}

	// Mark absorbed bills
	for _, b := range plan.BillsToAbsorb {
		if err := b.MarkAbsorbedBySettlement(); err != nil {
			continue
		}
		if err := s.bills.UpdateBill(ctx, b); err != nil {
			return nil, fmt.Errorf("mark absorbed bill %s: %w", b.ID, err)
		}
	}

	// Create the settlement bill
	if err := s.bills.CreateBill(ctx, &plan.Bill); err != nil {
		return nil, fmt.Errorf("create settlement bill: %w", err)
	}

	return toSettlementResult(plan.Bill.ID, plan.Deposit), nil
}

// --- Line-item builders ---

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
func (s *Service) addRentAdjustment(ctx context.Context, items []billing.BillLineItem, order int, c *contract.Contract, moveOutDate time.Time, rentMode billing.SettlementRentMode, apartmentID uuid.UUID) ([]billing.BillLineItem, int, bool, error) {
	billingMonth := billing.ToMonth(moveOutDate)

	hasPaid, err := s.bills.HasPaidAdvanceRentForMonth(ctx, c.ID, billingMonth)
	if err != nil {
		return nil, 0, false, fmt.Errorf("check advance rent: %w", err)
	}

	if hasPaid {
		// Advance rent already paid — no charge, no refund
		// ธุรกิจขายห้องเป็นรายเดือน จึงไม่คืนส่วนที่ไม่ได้อยู่
		return items, order, true, nil
	}

	// Full-month mode: charge full rent regardless of move-out day
	if rentMode == billing.RentModeFullMonthKeepDeposit {
		desc := fmt.Sprintf("ค่าห้องเดือน %s", billingMonth)
		items = append(items, billing.NewRoomRentLine(c.MonthlyRent, desc, order))
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
	items = append(items, billing.NewProrateRentLine(usedDays, rate, desc, order))
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
func (s *Service) findProrateRate(ctx context.Context, apartmentID uuid.UUID) (rate int64, found bool, err error) {
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

// addRecoveryRefunds appends one refund ADJUSTMENT line per unreflected
// (recovery, utility) over-record for the contract — the Epic B settlement
// recovery resolver (DQ0: settlement owns the move-out forward credit). It is
// the SINGLE settlement recovery emission path (INV-3); Regenerate/Correct
// reflect only by re-running prepareSettlementPlan, never by emitting lines
// themselves.
//
// Discovery shares ONE predicate with the finalize gate (INV-1), so exactly the
// pairs the gate would block on are emitted here — this is what clears the D1
// deadlock. Pricing reuses the monthly source-rate lookup
// (FindRecoverySourceRefundRates → SOURCE bill unit_price, HC6): settlement
// never re-prices at the current contract rate. Emission is surgical per-pair
// (only the discovered utilities), so a utility already reflected elsewhere is
// not re-credited. The lines are Source=MANUAL/LineType=ADJUSTMENT, so the
// Regenerate/Correct carry-over (EditableManualItems) excludes them and they are
// re-derived fresh each rebuild → idempotent, no double-count. DQ4: the domain
// keeps one line per (recovery, utility); the presentation layer aggregates.
func (s *Service) addRecoveryRefunds(ctx context.Context, items []billing.BillLineItem, order int, contractID uuid.UUID) ([]billing.BillLineItem, int, error) {
	pairs, err := s.bills.FindUnreflectedOverRecordsByContractID(ctx, contractID)
	if err != nil {
		return nil, 0, fmt.Errorf("find unreflected over-records: %w", err)
	}
	if len(pairs) == 0 {
		return items, order, nil
	}

	// Group utilities by recovery so the reading + source rates load once per
	// recovery, in a stable order (map iteration order is non-deterministic).
	utilsByRecovery := map[uuid.UUID][]billing.AdjustmentUtility{}
	var recoveryOrder []uuid.UUID
	for _, p := range pairs {
		if _, seen := utilsByRecovery[p.RecoveryID]; !seen {
			recoveryOrder = append(recoveryOrder, p.RecoveryID)
		}
		utilsByRecovery[p.RecoveryID] = append(utilsByRecovery[p.RecoveryID], p.Utility)
	}

	for _, recoveryID := range recoveryOrder {
		reading, err := s.meters.FindByIDSimple(ctx, recoveryID)
		if err != nil {
			return nil, 0, fmt.Errorf("load recovery reading %s: %w", recoveryID, err)
		}
		// The gate predicate guarantees a source-billed recovery, but guard the
		// two derived facts anyway so a race that soft-deletes the source between
		// discovery and load degrades to "no refund" instead of a nil deref.
		if reading.RecoverySourceReadingID == nil {
			continue
		}
		rates, err := s.bills.FindRecoverySourceRefundRates(ctx, *reading.RecoverySourceReadingID, contractID)
		if err != nil {
			return nil, 0, fmt.Errorf("find source refund rates for recovery %s: %w", recoveryID, err)
		}
		if rates == nil {
			continue // source month no longer billed (S0) — nothing to refund
		}
		for _, u := range utilsByRecovery[recoveryID] {
			rate := rates.Electricity
			if u == billing.AdjustmentUtilityWater {
				rate = rates.Water
			}
			res, _, err := billing.ResolveCorrection(reading, u, rate)
			if err != nil || res.Amount == 0 {
				continue
			}
			refundLine, err := billing.BuildRecoveryAdjustmentLine(uuid.Nil, res, order)
			if err != nil {
				return nil, 0, err
			}
			items = append(items, refundLine)
			order++
		}
	}
	return items, order, nil
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
func (s *Service) addConfigFees(ctx context.Context, items []billing.BillLineItem, order int, apartmentID uuid.UUID) ([]billing.BillLineItem, int, error) {
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
		items = append(items, billing.NewFeeLine(lt, cfg.DefaultAmount, desc, order))
		order++
	}
	return items, order, nil
}

// findBillsToVoid identifies non-paid monthly bills that should be voided when
// a settlement replaces them. Read-only — actual voiding happens in commitSettlementPlan.
func (s *Service) findBillsToVoid(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]*billing.Bill, error) {
	bills, err := s.bills.FindNonVoidedByContractAndMonth(ctx, contractID, billingMonth)
	if err != nil {
		return nil, fmt.Errorf("find existing bills: %w", err)
	}

	var toVoid []*billing.Bill
	for i := range bills {
		b := &bills[i]
		if b.BillType != billing.BillTypeMonthly {
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
func (s *Service) prepareAbsorption(ctx context.Context, items []billing.BillLineItem, order int, contractID uuid.UUID, settlementMonth string) ([]billing.BillLineItem, int, []*billing.Bill, error) {
	unpaid, err := s.bills.FindUnpaidMonthlyByContractID(ctx, contractID)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("find unpaid bills: %w", err)
	}

	advanceRentBillMonth := billing.PreviousMonth(settlementMonth)
	var toAbsorb []*billing.Bill

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

		items = append(items, billing.NewOutstandingBillLine(amount, desc, order))
		order++
		toAbsorb = append(toAbsorb, b)
	}

	return items, order, toAbsorb, nil
}

// restoreAbsorbedBills restores monthly bills that were voided by settlement absorption.
// Called when a settlement is voided (cancel or regeneration) so bills can be re-absorbed
// by a new settlement or collected normally if move-out is cancelled.
// Only touches bills where IsAbsorbedBySettlement() == true.
func (s *Service) restoreAbsorbedBills(ctx context.Context, contractID uuid.UUID) error {
	bills, err := s.bills.FindAbsorbedByContractID(ctx, contractID)
	if err != nil {
		return fmt.Errorf("find absorbed bills: %w", err)
	}
	for i := range bills {
		b := &bills[i]
		if err := b.RestoreFromAbsorbed(); err != nil {
			continue // not absorbed — skip (defensive)
		}
		if err := s.bills.UpdateBill(ctx, b); err != nil {
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
func absorbableUtilityTotal(b *billing.Bill) int64 {
	var total int64
	for _, li := range b.LineItems {
		switch li.LineType {
		case billing.LineItemElectricity, billing.LineItemWater:
			total += li.Amount
		case billing.LineItemRoomRent, billing.LineItemProrateRent:
			// Rent is recalculated as prorate in settlement — skip
		default:
			// CLEANING_FEE, KEY_SERVICE, PENALTY, OTHER, etc.
			// Do NOT absorb silently — these are not utility charges.
			// They will be lost from the voided bill. Admin can re-add as MANUAL items.
		}
	}
	return total
}
