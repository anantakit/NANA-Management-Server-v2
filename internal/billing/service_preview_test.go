package billing

import (
	"context"
	"testing"
	"time"

	"nana/internal/contract"

	"github.com/google/uuid"
)

// ============================================================
// Settlement Preview Tests
// ============================================================

func previewSetup(c *contract.Contract, moveOutDate time.Time, unpaidBills []Bill) (*mockBillingRepo, BillingService) {
	exitReading := testExitReading(c.RoomID, moveOutDate)
	notice := completedNotice(c.ID, moveOutDate)

	repo := &mockBillingRepo{
		findUnpaidMonthlyFn: func(_ context.Context, _ uuid.UUID) ([]Bill, error) {
			return unpaidBills, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{contract: c},
		&mockMeterQuerier{reading: exitReading},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{notice: notice},
	)
	return repo, svc
}

func TestPreviewSettlement_HappyPath_Prorated(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := previewSetup(c, moveOut, nil)

	preview, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if preview.RentMode != RentModeProrated {
		t.Errorf("RentMode = %s, want PRORATED", preview.RentMode)
	}
	if preview.MoveOutDate != moveOut {
		t.Errorf("MoveOutDate = %v, want %v", preview.MoveOutDate, moveOut)
	}
	if !preview.Returnable {
		t.Error("expected deposit returnable for normal exit")
	}
	if preview.MinMonths != 6 {
		t.Errorf("MinMonths = %d, want 6", preview.MinMonths)
	}
	if len(preview.Plan.Bill.LineItems) < 2 {
		t.Errorf("expected at least 2 line items, got %d", len(preview.Plan.Bill.LineItems))
	}

	d := preview.Plan.Deposit
	if d.OriginalAmount != c.DepositAmount {
		t.Errorf("deposit original = %d, want %d", d.OriginalAmount, c.DepositAmount)
	}
	if d.ForfeitedAmount != 0 {
		t.Errorf("deposit forfeited = %d, want 0 for returnable", d.ForfeitedAmount)
	}
}

func TestPreviewSettlement_FullMonthKeepDeposit(t *testing.T) {
	c := earlyExitContract() // start Jan 1 2026, min 6 months
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := previewSetup(c, moveOut, nil)

	preview, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
		RentMode:   RentModeFullMonthKeepDeposit,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if preview.RentMode != RentModeFullMonthKeepDeposit {
		t.Errorf("RentMode = %s, want FULL_MONTH_KEEP_DEPOSIT", preview.RentMode)
	}

	// Full-month mode should produce ROOM_RENT (not PRORATE_RENT)
	var hasFullRent bool
	for _, li := range preview.Plan.Bill.LineItems {
		if li.LineType == LineItemRoomRent {
			hasFullRent = true
			if li.Amount != c.MonthlyRent {
				t.Errorf("rent amount = %d, want %d (full month)", li.Amount, c.MonthlyRent)
			}
		}
	}
	if !hasFullRent {
		t.Error("expected ROOM_RENT line item for FULL_MONTH mode")
	}
}

func TestPreviewSettlement_DoesNotPersist(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bill := unpaidBill(c.ID, "2026-02", 620000)
	repo, svc := previewSetup(c, moveOut, []Bill{bill})

	_, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.createdBill != nil {
		t.Error("preview must not create a bill")
	}
	if len(repo.updatedBills) > 0 {
		t.Errorf("preview must not update any bills, got %d updates", len(repo.updatedBills))
	}
}

func TestPreviewSettlement_IncludesAbsorbedBills(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bill := unpaidBill(c.ID, "2026-02", 620000)
	_, svc := previewSetup(c, moveOut, []Bill{bill})

	preview, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(preview.Plan.BillsToAbsorb) != 1 {
		t.Fatalf("expected 1 absorbed bill, got %d", len(preview.Plan.BillsToAbsorb))
	}
	if preview.Plan.BillsToAbsorb[0].ID != bill.ID {
		t.Error("absorbed bill ID mismatch")
	}
}

func TestPreviewSettlement_MinMonthsThreshold(t *testing.T) {
	// Early exit: start Jan 1 2026, min 6 months → eligible Jul 1
	// Move out Apr 14 < Jul 1 → not returnable
	c := earlyExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := previewSetup(c, moveOut, nil)

	preview, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if preview.Returnable {
		t.Error("expected deposit NOT returnable for early exit")
	}
	d := preview.Plan.Deposit
	if d.ForfeitedAmount == 0 && d.RefundAmount > 0 {
		t.Error("early exit should not produce a refund")
	}
}

func TestPreviewSettlement_ZeroBalanceOutcome(t *testing.T) {
	// Set deposit exactly equal to expected charges so net = 0
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	_, svc := previewSetup(c, moveOut, nil)

	preview, err := svc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Manually set deposit to exactly match charges to verify ZERO_BALANCE
	// We test the outcome derivation directly via the DTO converter
	// by crafting a SettlementPreview where deposit covers charges exactly.
	zeroPlan := *preview.Plan
	charges := zeroPlan.Bill.TotalAmount
	zeroPlan.Deposit = computeDepositSettlement(charges, charges, true)

	zeroPreview := &SettlementPreview{
		Plan:                 &zeroPlan,
		MinMonths:            preview.MinMonths,
		Returnable:           true,
		MoveOutDate:          preview.MoveOutDate,
		EffectiveMoveOutDate: preview.EffectiveMoveOutDate,
		RentMode:             preview.RentMode,
	}

	resp := ToSettlementPreviewResponse(zeroPreview)
	if resp.Outcome != OutcomeZeroBalance {
		t.Errorf("outcome = %s, want ZERO_BALANCE", resp.Outcome)
	}
	if resp.Deposit.Refund != 0 {
		t.Errorf("deposit refund = %f, want 0", resp.Deposit.Refund)
	}
	if resp.Deposit.Due != 0 {
		t.Errorf("deposit due = %f, want 0", resp.Deposit.Due)
	}
}

// TestPreviewSettlement_ParityWithCreate verifies that preview and create
// produce semantically identical results for the same inputs.
// Asserts: line items, deposit breakdown, absorbed bills, effective move-out date, rent mode.
func TestPreviewSettlement_ParityWithCreate(t *testing.T) {
	c := normalExitContract()
	moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	bill := unpaidBill(c.ID, "2026-02", 620000)

	// --- Preview ---
	_, previewSvc := previewSetup(c, moveOut, []Bill{bill})
	preview, err := previewSvc.PreviewSettlement(context.Background(), PreviewSettlementInput{
		ContractID: c.ID,
	})
	if err != nil {
		t.Fatalf("preview error: %v", err)
	}

	// --- Create (via GenerateSettlement — same prepareSettlementPlan path) ---
	_, createSvc := settlementSetup(c, moveOut, []Bill{bill})
	_, err = createSvc.GenerateSettlement(context.Background(), c.ID, moveOut)
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	created := extractMockRepo(createSvc).createdBill

	// 1. Line items: count, types, amounts
	previewItems := preview.Plan.Bill.LineItems
	createItems := created.LineItems
	if len(previewItems) != len(createItems) {
		t.Fatalf("line item count: preview=%d, create=%d", len(previewItems), len(createItems))
	}
	for i := range previewItems {
		if previewItems[i].LineType != createItems[i].LineType {
			t.Errorf("line[%d] type: preview=%s, create=%s", i, previewItems[i].LineType, createItems[i].LineType)
		}
		if previewItems[i].Amount != createItems[i].Amount {
			t.Errorf("line[%d] amount: preview=%d, create=%d", i, previewItems[i].Amount, createItems[i].Amount)
		}
	}

	// 2. Deposit breakdown (recompute from created bill to compare)
	pd := preview.Plan.Deposit
	returnable := isDepositReturnable(c.StartDate, effectiveMoveOutDate(moveOut, RentModeProrated), c.MinMonths)
	cd := computeDepositSettlement(created.DepositAmount, created.TotalAmount, returnable)
	if pd.OriginalAmount != cd.OriginalAmount {
		t.Errorf("deposit original: preview=%d, create=%d", pd.OriginalAmount, cd.OriginalAmount)
	}
	if pd.AppliedAmount != cd.AppliedAmount {
		t.Errorf("deposit applied: preview=%d, create=%d", pd.AppliedAmount, cd.AppliedAmount)
	}
	if pd.RefundAmount != cd.RefundAmount {
		t.Errorf("deposit refund: preview=%d, create=%d", pd.RefundAmount, cd.RefundAmount)
	}
	if pd.AmountDue != cd.AmountDue {
		t.Errorf("deposit due: preview=%d, create=%d", pd.AmountDue, cd.AmountDue)
	}
	if pd.ForfeitedAmount != cd.ForfeitedAmount {
		t.Errorf("deposit forfeited: preview=%d, create=%d", pd.ForfeitedAmount, cd.ForfeitedAmount)
	}

	// 3. Absorbed bills
	if len(preview.Plan.BillsToAbsorb) != 1 {
		t.Fatalf("preview absorbed count = %d, want 1", len(preview.Plan.BillsToAbsorb))
	}

	// 4. Effective move-out date
	effPreview := effectiveMoveOutDate(preview.MoveOutDate, preview.RentMode)
	effCreate := effectiveMoveOutDate(moveOut, RentModeProrated)
	if !effPreview.Equal(effCreate) {
		t.Errorf("effective move-out: preview=%v, create=%v", effPreview, effCreate)
	}

	// 5. Rent mode
	if preview.RentMode != RentModeProrated {
		t.Errorf("rent mode: preview=%s, want PRORATED", preview.RentMode)
	}

	// 6. Total amount
	if preview.Plan.Bill.TotalAmount != created.TotalAmount {
		t.Errorf("total: preview=%d, create=%d", preview.Plan.Bill.TotalAmount, created.TotalAmount)
	}
}

// extractMockRepo recovers the mock repo from the service for test assertions.
func extractMockRepo(svc BillingService) *mockBillingRepo {
	return svc.(*billingService).repo.(*mockBillingRepo)
}
