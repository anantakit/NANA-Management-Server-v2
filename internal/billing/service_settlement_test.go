package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/billingconfig"
	"nana/internal/contract"

	"github.com/google/uuid"
)

// --- Settlement test helpers ---

func earlyExitContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		StartDate:              time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MinMonths:              6, // eligible at July 1
		MonthlyRent:            500000,
		DepositAmount:          1000000, // 10,000 baht
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
}

func normalExitContract() *contract.Contract {
	return &contract.Contract{
		ID:                     uuid.New(),
		RoomID:                 uuid.New(),
		Status:                 contract.ContractStatusActive,
		StartDate:              time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		MinMonths:              6, // eligible at Dec 1
		MonthlyRent:            500000,
		DepositAmount:          1000000,
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
}

func unpaidBill(contractID uuid.UUID, billingMonth string, total int64) Bill {
	return Bill{
		ID:           uuid.New(),
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  total,
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 500000, SortOrder: 1},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: total - 500000 - 18000, SortOrder: 2},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 18000, SortOrder: 3},
		},
	}
}

func unpaidBillWithUtility(contractID uuid.UUID, billingMonth string, rent, elec, water int64) Bill {
	return Bill{
		ID:           uuid.New(),
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  rent + elec + water,
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: rent, SortOrder: 1},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: elec, SortOrder: 2},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: water, SortOrder: 3},
		},
	}
}

func settlementSetup(c *contract.Contract, moveOutDate time.Time, unpaidBills []Bill) (*mockBillingRepo, BillingService) {
	exitReading := testExitReading(c.RoomID, moveOutDate)

	repo := &mockBillingRepo{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			return unpaidBills, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)
	return repo, svc
}

// ============================================================
// Settlement Absorption Tests
// ============================================================

func TestGenerateSettlement_AbsorbsSingleUnpaidBill(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	// Unpaid bill from 2026-02 (older than M-1) — absorb entirely
	bill := unpaidBill(c.ID, "2026-02", 620000) // 6,200 baht
	repo, svc := settlementSetup(c, moveOut, []Bill{bill})

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := repo.createdBill
	// Find OUTSTANDING_BILL line item
	var found bool
	for _, li := range created.LineItems {
		if li.LineType == LineItemOutstandingBill {
			found = true
			if li.Amount != 620000 {
				t.Errorf("outstanding amount = %d, want 620000", li.Amount)
			}
		}
	}
	if !found {
		t.Error("expected OUTSTANDING_BILL line item")
	}
}

func TestGenerateSettlement_AbsorbsMultipleUnpaidBills(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bills := []Bill{
		unpaidBill(c.ID, "2026-01", 600000),
		unpaidBill(c.ID, "2026-02", 620000),
	}
	repo, svc := settlementSetup(c, moveOut, bills)

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var outstandingCount int
	var outstandingTotal int64
	for _, li := range repo.createdBill.LineItems {
		if li.LineType == LineItemOutstandingBill {
			outstandingCount++
			outstandingTotal += li.Amount
		}
	}
	if outstandingCount != 2 {
		t.Errorf("outstanding line items = %d, want 2", outstandingCount)
	}
	if outstandingTotal != 1220000 {
		t.Errorf("outstanding total = %d, want 1220000", outstandingTotal)
	}
}

func TestGenerateSettlement_DoesNotAbsorbPaidBills(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	// Only unpaid bills are returned by FindUnpaidMonthlyByContractID
	// A paid bill won't appear in the query results
	repo, svc := settlementSetup(c, moveOut, nil) // no unpaid bills

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, li := range repo.createdBill.LineItems {
		if li.LineType == LineItemOutstandingBill {
			t.Error("should not have OUTSTANDING_BILL when no unpaid bills exist")
		}
	}
}

func TestGenerateSettlement_MarksAbsorbedBillsVoid(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bill := unpaidBill(c.ID, "2026-02", 620000)
	repo, svc := settlementSetup(c, moveOut, []Bill{bill})

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check the absorbed bill was voided via MarkAbsorbedBySettlement
	var voidedAbsorbed bool
	for _, b := range repo.updatedBills {
		if b.ID == bill.ID && b.IsAbsorbedBySettlement() {
			voidedAbsorbed = true
		}
	}
	if !voidedAbsorbed {
		t.Error("absorbed bill should satisfy IsAbsorbedBySettlement()")
	}
}

// ============================================================
// Deposit Pool Tests
// ============================================================

func TestGenerateSettlement_DepositAppliedWhenSufficient(t *testing.T) {
	c := normalExitContract() // deposit = 10,000, normal exit (returnable)
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := settlementSetup(c, moveOut, nil)

	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Deposit (10,000) should exceed small utility charges → refund
	if result.NetAmount >= 0 {
		t.Errorf("NetAmount = %d, expected negative (refund) for normal exit with large deposit", result.NetAmount)
	}
	if result.DepositUsed <= 0 {
		t.Error("expected deposit to be applied")
	}
}

func TestGenerateSettlement_DepositInsufficientRequiresPayment(t *testing.T) {
	c := normalExitContract()
	c.DepositAmount = 10000 // only 100 baht — way less than charges
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	bills := []Bill{unpaidBill(c.ID, "2026-02", 620000)} // large outstanding
	_, svc := settlementSetup(c, moveOut, bills)

	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NetAmount <= 0 {
		t.Errorf("NetAmount = %d, expected positive (tenant pays) when deposit < charges", result.NetAmount)
	}
}

func TestGenerateSettlement_DepositExceedsReturnsRefund(t *testing.T) {
	c := normalExitContract()
	c.DepositAmount = 5000000 // 50,000 baht — way more than charges
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := settlementSetup(c, moveOut, nil)

	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NetAmount >= 0 {
		t.Errorf("NetAmount = %d, expected negative (refund) for returnable deposit exceeding charges", result.NetAmount)
	}
}

func TestGenerateSettlement_EarlyExitDepositForfeited(t *testing.T) {
	c := earlyExitContract() // start=Jan 1, minMonths=6, deposit=10,000
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC) // 3.5 months = early exit
	repo, svc := settlementSetup(c, moveOut, nil)

	result, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := repo.createdBill

	// Deposit should be full amount (recorded for display)
	if created.DepositAmount != c.DepositAmount {
		t.Errorf("DepositAmount = %d, want %d (full deposit recorded)", created.DepositAmount, c.DepositAmount)
	}

	// DepositForfeited flag must be persisted on the bill
	if !created.DepositForfeited {
		t.Error("DepositForfeited = false, want true (early exit)")
	}

	// DepositBalance = -TotalAmount when forfeited (deposit not applied)
	if created.DepositBalance != -created.TotalAmount {
		t.Errorf("DepositBalance = %d, want %d (forfeited: -TotalAmount)", created.DepositBalance, -created.TotalAmount)
	}

	// Early exit: deposit forfeited → not applied → tenant pays full charges
	if result.NetAmount != created.TotalAmount {
		t.Errorf("NetAmount = %d, want %d (deposit forfeited, tenant pays full charges)", result.NetAmount, created.TotalAmount)
	}
}

// ============================================================
// Single Document Test
// ============================================================

func TestGenerateSettlement_IsSingleDocument(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bills := []Bill{
		unpaidBill(c.ID, "2026-01", 600000),
		unpaidBill(c.ID, "2026-02", 620000),
	}
	repo, svc := settlementSetup(c, moveOut, bills)

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := repo.createdBill
	if !created.IsSettlement() {
		t.Error("expected SETTLEMENT bill type")
	}
	if !created.IsDraft() {
		t.Error("expected DRAFT status")
	}

	// Should have: rent (prorate or none) + water + electricity + 2 outstanding
	hasWater := false
	hasElec := false
	outstandingCount := 0
	for _, li := range created.LineItems {
		switch li.LineType {
		case LineItemWater:
			hasWater = true
		case LineItemElectricity:
			hasElec = true
		case LineItemOutstandingBill:
			outstandingCount++
		}
	}
	if !hasWater || !hasElec {
		t.Error("settlement must include utility charges")
	}
	if outstandingCount != 2 {
		t.Errorf("settlement should absorb 2 outstanding bills, got %d", outstandingCount)
	}

	// Total should include everything
	if created.TotalAmount <= 0 {
		t.Error("total should be positive (has charges)")
	}
}

// ============================================================
// M-1 Bill Special Case (advance rent bill)
// ============================================================

func TestGenerateSettlement_AbsorbsOnlyUtilityFromAdvanceRentBill(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	// Bill "2026-03" is M-1 (advance rent for April) — only utility should be absorbed
	bill := unpaidBillWithUtility(c.ID, "2026-03", 500000, 120000, 18000) // rent=5000, elec=1200, water=180

	repo, svc := settlementSetup(c, moveOut, []Bill{bill})

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var outstandingAmount int64
	for _, li := range repo.createdBill.LineItems {
		if li.LineType == LineItemOutstandingBill {
			outstandingAmount += li.Amount
		}
	}

	// Should absorb only utility (120000 + 18000 = 138000), NOT rent (500000)
	expectedUtility := int64(120000 + 18000)
	if outstandingAmount != expectedUtility {
		t.Errorf("outstanding amount = %d, want %d (utility only from M-1 bill)", outstandingAmount, expectedUtility)
	}
}

// ============================================================
// VoidSettlement restores absorbed bills
// ============================================================

func TestVoidSettlement_RestoresAbsorbedBills(t *testing.T) {
	settlementID := uuid.New()
	contractID := uuid.New()

	// Build an absorbed bill using the model method
	absorbedBill := Bill{
		ID:         uuid.New(),
		ContractID: contractID,
		BillingMonth: "2026-02",
		BillType:   BillTypeMonthly,
		Status:     BillStatusFinalized,
		LineItems:  []BillLineItem{{LineType: LineItemWater, Amount: 1000, SortOrder: 1}},
	}
	_ = absorbedBill.MarkAbsorbedBySettlement()

	if !absorbedBill.IsAbsorbedBySettlement() {
		t.Fatal("precondition: bill should be absorbed")
	}

	repo := &mockBillingRepo{
		findByIDFn: func(_ context.Context, id uuid.UUID) (*Bill, error) {
			return &Bill{
				ID:         settlementID,
				ContractID: contractID,
				BillType:   BillTypeSettlement,
				Status:     BillStatusDraft,
				LineItems:  []BillLineItem{{LineType: LineItemWater, Amount: 1000, SortOrder: 1}},
			}, nil
		},
		findAbsorbedFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			return []Bill{absorbedBill}, nil
		},
	}
	svc := newSvc(repo, &mockContractQuerier{}, &mockMeterQuerier{}, &mockConfigQuerier{}, &mockMoveOutQuerier{})

	err := svc.VoidSettlement(context.Background(), settlementID, "MOVE_OUT_CANCELLED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check: absorbed bill restored via RestoreFromAbsorbed
	var restoredCount int
	for _, b := range repo.updatedBills {
		if b.ID == absorbedBill.ID && b.Status == BillStatusFinalized && b.VoidReason == nil {
			restoredCount++
		}
	}
	if restoredCount != 1 {
		t.Errorf("restored absorbed bills = %d, want 1", restoredCount)
	}
}

// ============================================================
// Absorbed bill model helpers
// ============================================================

func TestBill_AbsorptionLifecycle(t *testing.T) {
	t.Run("mark + check + restore", func(t *testing.T) {
		b := Bill{Status: BillStatusFinalized, LineItems: []BillLineItem{{Amount: 100, SortOrder: 1}}}

		if b.IsAbsorbedBySettlement() {
			t.Error("should not be absorbed initially")
		}

		if err := b.MarkAbsorbedBySettlement(); err != nil {
			t.Fatalf("MarkAbsorbedBySettlement: %v", err)
		}
		if !b.IsAbsorbedBySettlement() {
			t.Error("should be absorbed after mark")
		}
		if !b.IsVoid() {
			t.Error("should be VOID after mark")
		}

		if err := b.RestoreFromAbsorbed(); err != nil {
			t.Fatalf("RestoreFromAbsorbed: %v", err)
		}
		if b.IsAbsorbedBySettlement() {
			t.Error("should not be absorbed after restore")
		}
		if b.Status != BillStatusFinalized {
			t.Errorf("status = %s, want FINALIZED", b.Status)
		}
		if b.VoidReason != nil {
			t.Error("VoidReason should be nil after restore")
		}
	})

	t.Run("restore rejects non-absorbed void bill", func(t *testing.T) {
		b := Bill{Status: BillStatusFinalized, LineItems: []BillLineItem{{Amount: 100, SortOrder: 1}}}
		_ = b.Void("REGENERATED")

		if err := b.RestoreFromAbsorbed(); err == nil {
			t.Error("RestoreFromAbsorbed should reject bill voided for other reason")
		}
	})

	t.Run("restore rejects non-void bill", func(t *testing.T) {
		b := Bill{Status: BillStatusFinalized}
		if err := b.RestoreFromAbsorbed(); err == nil {
			t.Error("RestoreFromAbsorbed should reject non-void bill")
		}
	})
}

// ============================================================
// Idempotency: generate → void → generate again
// ============================================================

func TestSettlement_GenerateVoidGenerateIdempotent(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOut)

	// Two outstanding bills that will be absorbed
	jan := unpaidBill(c.ID, "2026-01", 600000)
	feb := unpaidBill(c.ID, "2026-02", 620000)

	// Track bill states across cycles
	billStates := map[uuid.UUID]*Bill{
		jan.ID: &jan,
		feb.ID: &feb,
	}

	repo := &mockBillingRepo{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			// Return only FINALIZED bills (simulating real DB filter)
			var result []Bill
			for _, b := range billStates {
				if b.IsFinalized() {
					result = append(result, *b)
				}
			}
			return result, nil
		},
		findAbsorbedFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			var result []Bill
			for _, b := range billStates {
				if b.IsAbsorbedBySettlement() {
					result = append(result, *b)
				}
			}
			return result, nil
		},
		updateFn: func(_ context.Context, bill *Bill) error {
			// Track state changes in our map
			if st, ok := billStates[bill.ID]; ok {
				st.Status = bill.Status
				st.VoidReason = bill.VoidReason
			}
			return nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)

	// --- Cycle 1: Generate ---
	result1, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("generate 1: %v", err)
	}

	// Both bills should be absorbed
	for _, b := range billStates {
		if !b.IsAbsorbedBySettlement() {
			t.Errorf("after generate 1: bill %s should be absorbed", b.BillingMonth)
		}
	}

	created1 := repo.createdBill
	outstanding1 := countLineItemsByType(created1, LineItemOutstandingBill)
	if outstanding1 != 2 {
		t.Errorf("generate 1: outstanding items = %d, want 2", outstanding1)
	}

	// --- Cycle 2: Void settlement ---
	// We need findByIDFn to return the settlement for voiding
	repo.findByIDFn = func(_ context.Context, id uuid.UUID) (*Bill, error) {
		if id == result1.BillID {
			return created1, nil
		}
		return nil, errors.New("not found")
	}

	err = svc.VoidSettlement(context.Background(), result1.BillID, "MOVE_OUT_CANCELLED")
	if err != nil {
		t.Fatalf("void: %v", err)
	}

	// Both bills should be restored to FINALIZED
	for _, b := range billStates {
		if !b.IsFinalized() {
			t.Errorf("after void: bill %s should be FINALIZED, got %s", b.BillingMonth, b.Status)
		}
	}

	// --- Cycle 3: Generate again ---
	repo.createdBill = nil
	repo.findByIDFn = nil

	_, err = svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("generate 2: %v", err)
	}

	// Both bills absorbed again
	for _, b := range billStates {
		if !b.IsAbsorbedBySettlement() {
			t.Errorf("after generate 2: bill %s should be absorbed", b.BillingMonth)
		}
	}

	created2 := repo.createdBill
	outstanding2 := countLineItemsByType(created2, LineItemOutstandingBill)
	if outstanding2 != 2 {
		t.Errorf("generate 2: outstanding items = %d, want 2 (no duplicates)", outstanding2)
	}
}

func countLineItemsByType(b *Bill, lt LineItemType) int {
	n := 0
	for _, li := range b.LineItems {
		if li.LineType == lt {
			n++
		}
	}
	return n
}

// ============================================================
// Absorption guard: DRAFT bills are not absorbed
// ============================================================

func TestGenerateSettlement_SkipsDraftBills(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	draftBill := Bill{
		ID:           uuid.New(),
		ContractID:   c.ID,
		BillingMonth: "2026-02",
		BillType:     BillTypeMonthly,
		Status:       BillStatusDraft, // not finalized
		TotalAmount:  620000,
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 500000, SortOrder: 1},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 102000, SortOrder: 2},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 18000, SortOrder: 3},
		},
	}

	repo, svc := settlementSetup(c, moveOut, []Bill{draftBill})

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, li := range repo.createdBill.LineItems {
		if li.LineType == LineItemOutstandingBill {
			t.Error("DRAFT bill should NOT be absorbed into settlement")
		}
	}
}

// ============================================================
// Transaction rollback test
// ============================================================

func TestGenerateSettlement_ReturnsErrorOnCreateFailure(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOut)

	repo := &mockBillingRepo{
		createFn: func(_ context.Context, _ *Bill) error {
			return errors.New("db connection lost")
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)

	_, err := svc.GenerateSettlement(context.Background(), c.ID, moveOut, "")
	if err == nil {
		t.Fatal("expected error when create fails")
	}
}

// ============================================================
// Settlement Rent Mode Tests
// ============================================================

// prepareWithMode is a test helper that runs prepareSettlementPlan with options.
func prepareWithMode(t *testing.T, c *contract.Contract, moveOut time.Time, unpaidBills []Bill, mode SettlementRentMode) *settlementPlan {
	t.Helper()
	exitReading := testExitReading(c.RoomID, moveOut)

	repo := &mockBillingRepo{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			return unpaidBills, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)

	plan, err := svc.(*billingService).prepareSettlementPlan(
		context.Background(), c.ID, moveOut,
		SettlementOptions{RentMode: mode},
	)
	if err != nil {
		t.Fatalf("prepareSettlementPlan: %v", err)
	}
	return plan
}

func TestRentMode_ProratedChargesProrate(t *testing.T) {
	c := normalExitContract() // start June 1 2025, minMonths=6 → eligible Dec 1
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	plan := prepareWithMode(t, c, moveOut, nil, RentModeProrated)

	prorate := findLineByType(plan.Bill.LineItems, LineItemProrateRent)
	if prorate.Amount <= 0 {
		t.Fatal("PRORATED should produce a PRORATE_RENT line")
	}
	if prorate.Quantity != 14 {
		t.Errorf("prorate days = %d, want 14", prorate.Quantity)
	}

	roomRent := findLineByType(plan.Bill.LineItems, LineItemRoomRent)
	if roomRent.Amount != 0 {
		t.Error("PRORATED should not produce a ROOM_RENT line")
	}
}

func TestRentMode_FullMonthChargesFullRent(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	plan := prepareWithMode(t, c, moveOut, nil, RentModeFullMonthKeepDeposit)

	roomRent := findLineByType(plan.Bill.LineItems, LineItemRoomRent)
	if roomRent.Amount != c.MonthlyRent {
		t.Errorf("ROOM_RENT = %d, want %d (full month)", roomRent.Amount, c.MonthlyRent)
	}

	prorate := findLineByType(plan.Bill.LineItems, LineItemProrateRent)
	if prorate.Amount != 0 {
		t.Error("FULL_MONTH should not produce a PRORATE_RENT line")
	}
}

func TestRentMode_FullMonthRentPaidNoCharge(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOut)

	repo := &mockBillingRepo{
		hasPaidAdvanceRentFn: func(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
			return true, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)

	plan, err := svc.(*billingService).prepareSettlementPlan(
		context.Background(), c.ID, moveOut,
		SettlementOptions{RentMode: RentModeFullMonthKeepDeposit},
	)
	if err != nil {
		t.Fatalf("prepareSettlementPlan: %v", err)
	}

	// Rent already paid — no rent line regardless of mode
	roomRent := findLineByType(plan.Bill.LineItems, LineItemRoomRent)
	prorate := findLineByType(plan.Bill.LineItems, LineItemProrateRent)
	if roomRent.Amount != 0 || prorate.Amount != 0 {
		t.Error("rent paid → no rent charge expected in any mode")
	}
	if !plan.Bill.RentPaid {
		t.Error("RentPaid flag should be true")
	}
}

func TestRentMode_FullMonthReachesMinMonths(t *testing.T) {
	// Contract: start Jan 15, minMonths=6 → eligible at July 15
	// Mid-month start so FULL_MONTH can cross the threshold.
	c := earlyExitContract()
	c.StartDate = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	// Move-out July 10 — PRORATED: July 10 < July 15 → forfeited
	//                     FULL_MONTH: effective July 31 >= July 15 → returnable
	moveOut := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

	prorated := prepareWithMode(t, c, moveOut, nil, RentModeProrated)
	fullMonth := prepareWithMode(t, c, moveOut, nil, RentModeFullMonthKeepDeposit)

	// PRORATED: deposit forfeited (ForfeitedAmount > 0)
	if prorated.Deposit.ForfeitedAmount == 0 {
		t.Error("PRORATED July 10: deposit should be forfeited (< 6 months)")
	}
	if prorated.Deposit.RefundAmount > 0 {
		t.Error("PRORATED July 10: should have no refund")
	}

	// FULL_MONTH: deposit returnable (not forfeited)
	if fullMonth.Deposit.ForfeitedAmount > 0 {
		t.Error("FULL_MONTH July 10: deposit should NOT be forfeited (effective = July 31 >= July 15)")
	}
}

func TestRentMode_FullMonthStillBelowMinMonths(t *testing.T) {
	// Contract: start Jan 1, minMonths=6
	c := earlyExitContract()
	// Move-out March 10 — even with end-of-month (March 31), only 3 months < 6
	moveOut := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	plan := prepareWithMode(t, c, moveOut, nil, RentModeFullMonthKeepDeposit)

	if plan.Deposit.ForfeitedAmount == 0 {
		t.Error("FULL_MONTH March 10: deposit should still be forfeited (3 months < 6)")
	}
}

func TestRentMode_PersistedOnBill(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)

	prorated := prepareWithMode(t, c, moveOut, nil, RentModeProrated)
	if prorated.Bill.SettlementRentMode != RentModeProrated {
		t.Errorf("bill.SettlementRentMode = %q, want PRORATED", prorated.Bill.SettlementRentMode)
	}

	fullMonth := prepareWithMode(t, c, moveOut, nil, RentModeFullMonthKeepDeposit)
	if fullMonth.Bill.SettlementRentMode != RentModeFullMonthKeepDeposit {
		t.Errorf("bill.SettlementRentMode = %q, want FULL_MONTH_KEEP_DEPOSIT", fullMonth.Bill.SettlementRentMode)
	}
}

func TestRegenerateSettlement_PreservesRentMode(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOut)

	// First generate with FULL_MONTH_KEEP_DEPOSIT
	existingBill := &Bill{
		ID:                 uuid.New(),
		ContractID:         c.ID,
		BillingMonth:       "2026-04",
		BillType:           BillTypeSettlement,
		Status:             BillStatusDraft,
		SettlementRentMode: RentModeFullMonthKeepDeposit,
		DepositAmount:      c.DepositAmount,
		LineItems:          []BillLineItem{{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: c.MonthlyRent, SortOrder: 1}},
	}
	existingBill.CalculateTotal()

	var createdBill *Bill
	repo := &mockBillingRepo{
		findByIDFn: func(_ context.Context, id uuid.UUID) (*Bill, error) {
			if createdBill != nil && id == createdBill.ID {
				return createdBill, nil
			}
			return existingBill, nil
		},
		createFn: func(_ context.Context, bill *Bill) error {
			createdBill = bill
			return nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
	)

	_, err := svc.RegenerateSettlement(context.Background(), existingBill.ID, c.ID, moveOut, "")
	if err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}

	if createdBill == nil {
		t.Fatal("no bill created")
	}
	if createdBill.SettlementRentMode != RentModeFullMonthKeepDeposit {
		t.Errorf("regenerated bill.SettlementRentMode = %q, want FULL_MONTH_KEEP_DEPOSIT", createdBill.SettlementRentMode)
	}

	// Verify it charged full rent (not prorated)
	roomRent := findLineByType(createdBill.LineItems, LineItemRoomRent)
	if roomRent.Amount != c.MonthlyRent {
		t.Errorf("regenerated ROOM_RENT = %d, want %d (full month preserved)", roomRent.Amount, c.MonthlyRent)
	}
}

// TestRegenerateSettlement_IsIdempotent locks in the property that running
// regenerate twice on the same inputs produces byte-equivalent line items +
// totals. The smoke suite (`smoke:numeric` D17) surfaces drift between a
// handcrafted seed baseline and the canonical regen output (e.g. seed misses
// KEY_SERVICE from the config-fee injector). That drift is seed-side, not a
// regen bug — this test pins the contract from inside the planner so a future
// regression can't hide behind a stale seed.
//
// Setup uses the full production-default fee configs (PRORATE_DAILY_RATE +
// CLEANING_FEE + KEY_SERVICE) so every fee-injection path runs.
func TestRegenerateSettlement_IsIdempotent(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	exitReading := testExitReading(c.RoomID, moveOut)

	configs := []billingconfig.BillingConfig{
		{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		{FeeType: billingconfig.FeeTypeCleaningFee, DefaultAmount: 30000, IsActive: true},
		{FeeType: billingconfig.FeeTypeKeyService, DefaultAmount: 5000, IsActive: true},
	}

	// Initial draft intentionally handcrafted without KEY_SERVICE to mirror
	// the smoke-seed shape that caused the original drift report.
	initialBill := &Bill{
		ID:                 uuid.New(),
		ContractID:         c.ID,
		BillingMonth:       "2026-04",
		BillType:           BillTypeSettlement,
		Status:             BillStatusDraft,
		SettlementRentMode: RentModeProrated,
		DepositAmount:      c.DepositAmount,
		LineItems:          []BillLineItem{{LineType: LineItemProrateRent, Source: LineItemSourceAuto, Amount: 100000, SortOrder: 1}},
	}
	initialBill.CalculateTotal()

	currentBill := initialBill
	repo := &mockBillingRepo{
		findByIDFn: func(_ context.Context, id uuid.UUID) (*Bill, error) {
			if id == currentBill.ID {
				return currentBill, nil
			}
			return initialBill, nil
		},
		createFn: func(_ context.Context, bill *Bill) error {
			if bill.ID == uuid.Nil {
				bill.ID = uuid.New()
			}
			currentBill = bill
			return nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{configs: configs},
		&mockMoveOutQuerier{},
	)

	// Regen 1 — runs canonical planner, should include KEY_SERVICE.
	if _, err := svc.RegenerateSettlement(context.Background(), initialBill.ID, c.ID, moveOut, ""); err != nil {
		t.Fatalf("regen 1: %v", err)
	}
	regen1Total := currentBill.TotalAmount
	regen1Items := append([]BillLineItem(nil), currentBill.LineItems...)
	regen1ID := currentBill.ID

	// Regen 2 — feeds regen1's bill ID, must produce same output.
	if _, err := svc.RegenerateSettlement(context.Background(), regen1ID, c.ID, moveOut, ""); err != nil {
		t.Fatalf("regen 2: %v", err)
	}

	if currentBill.TotalAmount != regen1Total {
		t.Errorf("regen idempotency broken: regen1.TotalAmount=%d, regen2.TotalAmount=%d (delta=%d)",
			regen1Total, currentBill.TotalAmount, currentBill.TotalAmount-regen1Total)
	}

	if len(currentBill.LineItems) != len(regen1Items) {
		t.Fatalf("line-item count drift: regen1=%d, regen2=%d", len(regen1Items), len(currentBill.LineItems))
	}
	// Match by line_type + amount (ignore IDs / SortOrder shuffle).
	byType := make(map[LineItemType]int64, len(regen1Items))
	for _, li := range regen1Items {
		byType[li.LineType] += li.Amount
	}
	for _, li := range currentBill.LineItems {
		want, ok := byType[li.LineType]
		if !ok {
			t.Errorf("regen2 has unexpected line type %q", li.LineType)
			continue
		}
		byType[li.LineType] -= li.Amount
		if byType[li.LineType] == 0 {
			delete(byType, li.LineType)
		}
		_ = want
	}
	for lt, leftover := range byType {
		t.Errorf("line type %q amount drift between regens (delta=%d)", lt, leftover)
	}

	// Sanity: the canonical regen output must include KEY_SERVICE — proves
	// the planner ran the config-fee injector. If this disappears the test
	// stops asserting the property the smoke drift was about.
	var foundKey bool
	for _, li := range currentBill.LineItems {
		if li.LineType == LineItemKeyService {
			foundKey = true
			if li.Amount != 5000 {
				t.Errorf("KEY_SERVICE amount = %d, want 5000", li.Amount)
			}
		}
	}
	if !foundKey {
		t.Error("expected KEY_SERVICE line from addConfigFees")
	}
}
