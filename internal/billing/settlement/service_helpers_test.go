package settlement

// Settlement-specific tests ported from billing root in W4 settlement
// extraction (2026-06-19). Tests embedded in the original
// billing/service_test.go that exercise SETTLEMENT logic — the rent
// adjustment table, void monthly bills, deposit pool, utility from EXIT
// meter, config fees, notice-vs-actual move-out date, regression guards,
// invariants, error paths, and the rent-mode / deposit-qualification
// helpers — live here.
//
// Mechanical transform notes:
//   - package billing → package settlement
//   - billing.* prefix applied to root-owned types/constants
//   - mockBillingRepo → mockBillStore, mockContractQuerier →
//     mockContractSource, etc. (per mocks_test.go)
//   - newSvc(...) → newSvcWithMocks(...) returning *Service
//   - svc.CreateSettlementBill / GenerateSettlement etc. call concrete
//     methods on *Service directly (no interface)
//
// The findLineByType helper lives in service_settlement_test.go alongside
// the absorption tests — reused here.

import (
	"context"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// --- Local helpers for the settlement-bill flow tests ---

// settlementOpts bundles inputs for runSettlement-driven tests. Mirrors
// the helper used in billing root's service_test.go pre-extraction.
type settlementOpts struct {
	contract *contract.Contract
	moveOut  time.Time
	bills    *mockBillStore
	configs  *mockBillingConfigSource
}

func newSettlementOpts() *settlementOpts {
	c := testContract()
	c.StartDate = time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	c.MinMonths = 6
	return &settlementOpts{
		contract: c,
		moveOut:  time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC),
		bills:    &mockBillStore{},
		// Settlement bills with partial-month proration require a
		// PRORATE_DAILY_RATE config to exist for the apartment. Mirror
		// the production default (฿100/day = 10000 satang) so each test
		// doesn't have to wire it up.
		configs: &mockBillingConfigSource{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}},
	}
}

func withMoveOut(y, m, d int) func(*settlementOpts) {
	return func(o *settlementOpts) {
		o.moveOut = time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	}
}

func withRentPaid(o *settlementOpts) {
	o.bills.hasPaidAdvanceRentFn = func(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
		return true, nil
	}
}

func withDeposit(amount int64) func(*settlementOpts) {
	return func(o *settlementOpts) { o.contract.DepositAmount = amount }
}

// runSettlement creates a settlement bill with the given overrides and
// returns the created bill.
func runSettlement(t *testing.T, opts ...func(*settlementOpts)) *billing.Bill {
	t.Helper()
	o := newSettlementOpts()
	for _, fn := range opts {
		fn(o)
	}
	o.bills.apartmentID = uuid.New()
	exitReading := testExitReading(o.contract.RoomID, o.moveOut)
	notice := completedNotice(o.contract.ID, o.moveOut)

	svc := newSvcWithMocks(o.bills,
		&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return o.contract, nil }},
		&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
		o.configs,
		&mockMoveOutSource{notice: notice},
	)

	_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
		ContractID: o.contract.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.bills.createdBill == nil {
		t.Fatal("no bill created")
	}
	return o.bills.createdBill
}

// lineItemTypes returns the set of line item types present in a slice.
func lineItemTypes(items []billing.BillLineItem) map[billing.LineItemType]bool {
	m := make(map[billing.LineItemType]bool)
	for _, li := range items {
		m[li.LineType] = true
	}
	return m
}

// ── Detect Paid Advance Rent Coverage ──
//
// ช่องโหว่ที่ใหญ่ที่สุดถ้าไม่มี test นี้:
// rent adjustment table inject rentPaid=true/false ตรง ๆ
// แต่ไม่ได้พิสูจน์ว่า service ส่ง moveOutMonth ไปหา repo ถูก
// และ branch ตาม boolean ถูก
//
// Service calls: repo.HasPaidAdvanceRentForMonth(ctx, contractID, moveOutMonth)
// Repo internally: checks PAID bill where billing_month = moveOutMonth - 1

func TestSettlement_DetectRentCoverage(t *testing.T) {
	tests := []struct {
		name        string
		moveOut     [3]int
		repoReturn  bool
		wantMonth   string // month service passes to repo
		wantPaid    bool
		wantProrate bool
	}{
		// A1 — previous month bill is PAID → detect true
		{"prev_month_paid", [3]int{2026, 4, 14}, true, "2026-04", true, false},
		// A2 — previous month bill NOT PAID → detect false
		{"prev_month_not_paid", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// A3 — other month paid, previous not → detect false (repo returns false)
		{"other_month_paid_prev_not", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// A4 — no previous month bill at all → detect false
		{"no_prev_month_bill", [3]int{2026, 4, 14}, false, "2026-04", false, true},
		// Boundary: January move-out → repo receives "2026-01", checks Dec 2025
		{"jan_moveout_checks_prev_year", [3]int{2026, 1, 15}, true, "2026-01", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedMonth string
			o := newSettlementOpts()
			o.moveOut = time.Date(tt.moveOut[0], time.Month(tt.moveOut[1]), tt.moveOut[2], 0, 0, 0, 0, time.UTC)
			o.bills.hasPaidAdvanceRentFn = func(_ context.Context, _ uuid.UUID, month string) (bool, error) {
				capturedMonth = month
				return tt.repoReturn, nil
			}
			o.bills.apartmentID = uuid.New()
			exitReading := testExitReading(o.contract.RoomID, o.moveOut)
			notice := completedNotice(o.contract.ID, o.moveOut)
			svc := newSvcWithMocks(o.bills,
				&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return o.contract, nil }},
				&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
				o.configs,
				&mockMoveOutSource{notice: notice},
			)

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			bill := o.bills.createdBill

			// Verify: service passes moveOutMonth to repo (repo does M-1 internally)
			if capturedMonth != tt.wantMonth {
				t.Errorf("month passed to repo = %q, want %q", capturedMonth, tt.wantMonth)
			}
			if bill.RentPaid != tt.wantPaid {
				t.Errorf("RentPaid = %v, want %v", bill.RentPaid, tt.wantPaid)
			}
			hasProrate := findLineByType(bill.LineItems, billing.LineItemProrateRent).Amount > 0
			if hasProrate != tt.wantProrate {
				t.Errorf("hasProrate = %v, want %v", hasProrate, tt.wantProrate)
			}
		})
	}
}

// ── Rent Adjustment (TC01–04, TC16–17) ──

func TestSettlement_RentAdjustment(t *testing.T) {
	// Flat per-day rate from PRORATE_DAILY_RATE config (mock default).
	// 10000 satang = ฿100/day. Amount is exact: rate × days.
	const dailyRate = int64(10000)

	tests := []struct {
		name        string
		moveOut     [3]int // y, m, d
		rentPaid    bool
		wantProrate bool
		wantAmount  int64
		wantDays    int
	}{
		// TC01+11+12+22 — paid advance rent → 0 (no refund, no charge, no double)
		{"paid_mid_month", [3]int{2026, 4, 14}, true, false, 0, 0},
		// TC02+20 — not paid, mid-month → 14 days × rate
		{"not_paid_mid_month", [3]int{2026, 4, 14}, false, true, dailyRate * 14, 14},
		// TC03 — not paid, last day of 30-day month → rent = full month (day >= daysInMonth)
		{"not_paid_last_day_30", [3]int{2026, 4, 30}, false, false, 0, 0},
		// TC04 — not paid, first day (inclusive) → 1 day × rate
		{"not_paid_first_day", [3]int{2026, 4, 1}, false, true, dailyRate * 1, 1},
		// TC16 — leap year Feb 29 = last day → rent = full month
		{"leap_feb_29_full_month", [3]int{2024, 2, 29}, false, false, 0, 0},
		// TC17 — 31-day month, mid-month → 15 days × rate
		{"31day_month_mid", [3]int{2026, 5, 15}, false, true, dailyRate * 15, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []func(*settlementOpts){
				withMoveOut(tt.moveOut[0], tt.moveOut[1], tt.moveOut[2]),
			}
			if tt.rentPaid {
				opts = append(opts, withRentPaid)
			}
			bill := runSettlement(t, opts...)

			// RentPaid flag
			if bill.RentPaid != tt.rentPaid {
				t.Errorf("RentPaid = %v, want %v", bill.RentPaid, tt.rentPaid)
			}

			prorate := findLineByType(bill.LineItems, billing.LineItemProrateRent)
			hasProrate := prorate.LineType == billing.LineItemProrateRent && prorate.Amount > 0

			if hasProrate != tt.wantProrate {
				t.Errorf("hasProrate = %v, want %v", hasProrate, tt.wantProrate)
			}
			if tt.wantProrate {
				if prorate.Amount != tt.wantAmount {
					t.Errorf("prorate amount = %d, want %d", prorate.Amount, tt.wantAmount)
				}
				if prorate.Quantity != tt.wantDays {
					t.Errorf("prorate days = %d, want %d", prorate.Quantity, tt.wantDays)
				}
			}

			// Invariant: PREPAID_CREDIT must never appear (regression TC23)
			if lineItemTypes(bill.LineItems)[billing.LineItemPrepaidCredit] {
				t.Error("PREPAID_CREDIT must never appear — old logic removed")
			}
			// Invariant: no negative rent line (regression TC11)
			for _, li := range bill.LineItems {
				if (li.LineType == billing.LineItemProrateRent || li.LineType == billing.LineItemRoomRent) && li.Amount < 0 {
					t.Errorf("negative rent line: %s = %d", li.LineType, li.Amount)
				}
			}
		})
	}
}

// ── Void Monthly Bills (TC05–07, TC15, TC25) ──

func TestSettlement_VoidMonthlyBills(t *testing.T) {
	tests := []struct {
		name          string
		existingBills []billing.Bill
		wantVoidIDs   []int // indices into existingBills that should be voided
		wantKeepIDs   []int // indices that must NOT be voided
	}{
		{
			"TC05_void_draft",
			[]billing.Bill{{ID: uuid.New(), BillType: billing.BillTypeMonthly, Status: billing.BillStatusDraft}},
			[]int{0}, nil,
		},
		{
			"TC06_void_finalized",
			[]billing.Bill{{ID: uuid.New(), BillType: billing.BillTypeMonthly, Status: billing.BillStatusFinalized}},
			[]int{0}, nil,
		},
		{
			"TC07_no_existing_bill",
			nil, nil, nil,
		},
		{
			"TC15_never_void_paid",
			[]billing.Bill{{ID: uuid.New(), BillType: billing.BillTypeMonthly, Status: billing.BillStatusPaid, TotalAmount: 638000}},
			nil, []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills := tt.existingBills // capture

			o := newSettlementOpts()
			if len(bills) > 0 {
				o.bills.findNonVoidedByContractMonthFn = func(_ context.Context, _ uuid.UUID, _ string) ([]billing.Bill, error) {
					return bills, nil
				}
			}
			o.bills.apartmentID = uuid.New()
			exitReading := testExitReading(o.contract.RoomID, o.moveOut)
			notice := completedNotice(o.contract.ID, o.moveOut)
			svc := newSvcWithMocks(o.bills,
				&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return o.contract, nil }},
				&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
				o.configs,
				&mockMoveOutSource{notice: notice},
			)

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			voidedIDs := map[uuid.UUID]bool{}
			for _, ub := range o.bills.updatedBills {
				if ub.Status == billing.BillStatusVoid {
					voidedIDs[ub.ID] = true
					if ub.VoidReason == nil || *ub.VoidReason != "REPLACED_BY_SETTLEMENT" {
						t.Errorf("bill %s: void_reason should be REPLACED_BY_SETTLEMENT", ub.ID)
					}
				}
			}

			for _, idx := range tt.wantVoidIDs {
				if !voidedIDs[bills[idx].ID] {
					t.Errorf("bill[%d] (status=%s) should be voided", idx, bills[idx].Status)
				}
			}
			for _, idx := range tt.wantKeepIDs {
				if voidedIDs[bills[idx].ID] {
					t.Errorf("bill[%d] (status=%s) must NOT be voided", idx, bills[idx].Status)
				}
			}

			if len(bills) == 0 && len(o.bills.updatedBills) != 0 {
				t.Errorf("no existing bills → expected no void calls, got %d", len(o.bills.updatedBills))
			}
		})
	}
}

// ── Net Amount / Deposit (TC09–TC10) ──

func TestSettlement_NetAmount(t *testing.T) {
	// Compute exact charges for "rent paid + default exit meter" to craft zero case.
	// EXIT: electricity 50×800=40000, water 5×1800=9000 → total utility = 49000
	const utilityTotal = int64(50*800 + 5*1800) // 49000

	tests := []struct {
		name              string
		deposit           int64
		rentPaid          bool
		wantPositiveTotal bool // total > 0
		wantRefund        bool // depositBalance > 0
		wantZero          bool // depositBalance == 0
	}{
		{"deposit_covers_all", 10000000, true, true, true, false},    // 100k >> utility → refund
		{"tenant_pays_extra", 0, false, true, false, false},          // no deposit → tenant owes
		{"exact_zero_balance", utilityTotal, true, true, false, true}, // deposit = charges → net 0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []func(*settlementOpts){withDeposit(tt.deposit)}
			if tt.rentPaid {
				opts = append(opts, withRentPaid)
			}
			bill := runSettlement(t, opts...)

			if tt.wantPositiveTotal && bill.TotalAmount <= 0 {
				t.Errorf("total = %d, want > 0", bill.TotalAmount)
			}
			if tt.wantZero {
				if bill.DepositBalance != 0 {
					t.Errorf("deposit_balance = %d, want exactly 0", bill.DepositBalance)
				}
			} else if tt.wantRefund {
				if bill.DepositBalance <= 0 {
					t.Errorf("deposit_balance = %d, want > 0 (refund)", bill.DepositBalance)
				}
			} else {
				if bill.DepositBalance > 0 {
					t.Errorf("deposit_balance = %d, want <= 0 (tenant owes)", bill.DepositBalance)
				}
			}
		})
	}
}

// ── Utility from EXIT Meter (TC08, TC14, TC24) ──

func TestSettlement_UtilityFromExitMeter(t *testing.T) {
	bill := runSettlement(t)

	// EXIT reading from fixture: electricity 100→150 = 50 units, water 50→55 = 5 units
	// Rate: 800 satang/elec unit, 1800 satang/water unit
	tests := []struct {
		name       string
		lineType   billing.LineItemType
		wantQty    int
		wantAmount int64
	}{
		{"TC08_electricity", billing.LineItemElectricity, 50, 50 * 800},
		{"TC08_water", billing.LineItemWater, 5, 5 * 1800},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			li := findLineByType(bill.LineItems, tt.lineType)
			if li.Quantity != tt.wantQty {
				t.Errorf("quantity = %d, want %d", li.Quantity, tt.wantQty)
			}
			if li.Amount != tt.wantAmount {
				t.Errorf("amount = %d, want %d", li.Amount, tt.wantAmount)
			}
		})
	}

	// TC14+TC24 — exactly 1 of each, no monthly values leaked
	elecCount, waterCount := 0, 0
	for _, li := range bill.LineItems {
		switch li.LineType {
		case billing.LineItemElectricity:
			elecCount++
		case billing.LineItemWater:
			waterCount++
		}
	}
	if elecCount != 1 {
		t.Errorf("TC24: expected 1 electricity line, got %d", elecCount)
	}
	if waterCount != 1 {
		t.Errorf("TC24: expected 1 water line, got %d", waterCount)
	}
}

// ── Config Fees ──

func TestSettlement_ConfigFees(t *testing.T) {
	tests := []struct {
		name       string
		lineType   billing.LineItemType
		wantAmount int64
	}{
		{"adds_cleaning_fee", billing.LineItemCleaningFee, 50000},
		{"adds_key_service_fee", billing.LineItemKeyService, 20000},
	}

	bill := runSettlement(t, func(o *settlementOpts) {
		o.configs = &mockBillingConfigSource{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeCleaningFee, DefaultAmount: 50000, IsActive: true},
			{FeeType: billingconfig.FeeTypeKeyService, DefaultAmount: 20000, IsActive: true},
			// PRORATE_DAILY_RATE required for the partial-month rent line
			// (default move-out is mid-month).
			{FeeType: billingconfig.FeeTypeProrateDailyRate, DefaultAmount: 10000, IsActive: true},
		}}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			li := findLineByType(bill.LineItems, tt.lineType)
			if li.Amount != tt.wantAmount {
				t.Errorf("%s = %d, want %d", tt.lineType, li.Amount, tt.wantAmount)
			}
		})
	}
}

// ── Notice Date Must Be Ignored (TC13) ──
// ล็อกว่า notice date ไม่ affect calculation เลย ไม่ว่าจะก่อนหรือหลัง move-out

func TestSettlement_UsesActualMoveOutDate(t *testing.T) {
	tests := []struct {
		name       string
		noticeDate time.Time
	}{
		// notice BEFORE move-out
		{"notice_before_moveout", time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)},
		// notice AFTER move-out (e.g. backdated entry)
		{"notice_after_moveout", time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newSettlementOpts() // moveOut = April 14
			o.bills.apartmentID = uuid.New()
			exitReading := testExitReading(o.contract.RoomID, o.moveOut)
			notice := completedNotice(o.contract.ID, o.moveOut)
			notice.NoticeDate = tt.noticeDate

			svc := newSvcWithMocks(o.bills,
				&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return o.contract, nil }},
				&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) { return exitReading, nil }},
				o.configs,
				&mockMoveOutSource{notice: notice},
			)

			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: o.contract.ID.String(),
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			bill := o.bills.createdBill

			if bill.BillingMonth != "2026-04" {
				t.Errorf("billing_month = %s, want 2026-04 (from move-out, NOT notice)", bill.BillingMonth)
			}
			prorate := findLineByType(bill.LineItems, billing.LineItemProrateRent)
			if prorate.Quantity != 14 {
				t.Errorf("prorate days = %d, want 14 (from move-out date)", prorate.Quantity)
			}
		})
	}
}

// ── Regression: No PREPAID_CREDIT Even With Old Mock (TC23) ──

func TestSettlement_Regression_NoPrepaidCredit(t *testing.T) {
	// Old pipeline used SumPaidByContractSince — that method no longer
	// exists on BillStore. The regression risk it guarded (PREPAID_CREDIT
	// line slipping back into the bill) is preserved by simply running
	// the canonical settlement path and asserting no PREPAID_CREDIT line.
	bill := runSettlement(t)

	if lineItemTypes(bill.LineItems)[billing.LineItemPrepaidCredit] {
		t.Fatal("REGRESSION: PREPAID_CREDIT must not exist — old logic removed")
	}
}

// ── Invariants — "ผลลัพธ์สุดท้ายต้องไม่มีสิ่งที่ไม่ควรเกิด" ──
// Run settlement ทั้ง 2 path (paid / not-paid) แล้วเช็ค structural invariants
// กัน: future refactor พลาด, dev ใหม่ใส่ logic แปลก, silent data corruption

func TestSettlement_Invariants(t *testing.T) {
	tests := []struct {
		name string
		opts []func(*settlementOpts)
	}{
		{"rent_paid", []func(*settlementOpts){withRentPaid}},
		{"rent_not_paid", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bill := runSettlement(t, tt.opts...)

			// 1. ห้ามมี PREPAID_CREDIT (old logic ลบแล้ว)
			for _, li := range bill.LineItems {
				if li.LineType == billing.LineItemPrepaidCredit {
					t.Error("PREPAID_CREDIT must never appear")
				}
			}

			// 2. ห้ามมี rent line ที่ amount < 0 (ไม่มี rent refund)
			for _, li := range bill.LineItems {
				if (li.LineType == billing.LineItemProrateRent || li.LineType == billing.LineItemRoomRent) && li.Amount < 0 {
					t.Errorf("negative rent line: %s = %d", li.LineType, li.Amount)
				}
			}

			// 3. ELECTRICITY / WATER ต้องมีอย่างละ 1 บรรทัดเท่านั้น
			typeCounts := map[billing.LineItemType]int{}
			for _, li := range bill.LineItems {
				if li.Source == billing.LineItemSourceAuto {
					typeCounts[li.LineType]++
				}
			}
			if typeCounts[billing.LineItemElectricity] != 1 {
				t.Errorf("electricity lines = %d, want exactly 1", typeCounts[billing.LineItemElectricity])
			}
			if typeCounts[billing.LineItemWater] != 1 {
				t.Errorf("water lines = %d, want exactly 1", typeCounts[billing.LineItemWater])
			}

			// 4. AUTO line type ห้ามซ้ำ (1 type = 1 line)
			for lt, count := range typeCounts {
				if count > 1 {
					t.Errorf("duplicate AUTO line type %s: count = %d", lt, count)
				}
			}

			// 5. TotalAmount == sum(line items)
			var sum int64
			for _, li := range bill.LineItems {
				sum += li.Amount
			}
			if bill.TotalAmount != sum {
				t.Errorf("TotalAmount = %d, sum(lines) = %d — mismatch", bill.TotalAmount, sum)
			}

			// 6. DepositBalance consistency
			// Forfeited: -TotalAmount (deposit not applied)
			// Returnable: DepositAmount - TotalAmount
			var wantBalance int64
			if bill.DepositForfeited {
				wantBalance = -bill.TotalAmount
			} else {
				wantBalance = bill.DepositAmount - bill.TotalAmount
			}
			if bill.DepositBalance != wantBalance {
				t.Errorf("DepositBalance = %d, want %d (forfeited=%v deposit=%d total=%d)",
					bill.DepositBalance, wantBalance, bill.DepositForfeited, bill.DepositAmount, bill.TotalAmount)
			}
		})
	}
}

// ── Error Cases (TC19 + guards) ──

func TestSettlement_Errors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() *Service
		wantErr error
	}{
		{
			"TC19_missing_exit_meter",
			func() *Service {
				c := testContract()
				moveOut := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
				return newSvcWithMocks(&mockBillStore{},
					&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
					&mockMeterReadingSource{},
					&mockBillingConfigSource{},
					&mockMoveOutSource{notice: completedNotice(c.ID, moveOut)})
			},
			ErrExitReadingMissing,
		},
		{
			"rejects_no_actual_date",
			func() *Service {
				c := testContract()
				return newSvcWithMocks(&mockBillStore{},
					&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
					&mockMeterReadingSource{},
					&mockBillingConfigSource{},
					&mockMoveOutSource{notice: &moveout.MoveOutNotice{
						ID: uuid.New(), ContractID: c.ID,
						Status:               moveout.MoveOutStatusPendingMeter,
						ScheduledMoveOutDate: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
					}})
			},
			ErrActualDateRequired,
		},
		{
			"no_move_out_notice",
			func() *Service {
				c := testContract()
				return newSvcWithMocks(&mockBillStore{},
					&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
					&mockMeterReadingSource{},
					&mockBillingConfigSource{},
					&mockMoveOutSource{})
			},
			ErrMoveOutNotFound,
		},
		{
			"latest_reading_is_monthly_not_exit",
			func() *Service {
				c := testContract()
				moveOut := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
				return newSvcWithMocks(&mockBillStore{},
					&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
					&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) {
						return testMonthlyReading(c.RoomID, "2026-03"), nil
					}},
					&mockBillingConfigSource{},
					&mockMoveOutSource{notice: completedNotice(c.ID, moveOut)})
			},
			ErrExitReadingMissing,
		},
		{
			"duplicate_settlement_guard",
			func() *Service {
				c := testContract()
				moveOut := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
				return newSvcWithMocks(
					&mockBillStore{findByContractAndMonthFn: func(_ context.Context, _ uuid.UUID, _ string, bt billing.BillType) (*billing.Bill, error) {
						if bt == billing.BillTypeSettlement {
							return &billing.Bill{ID: uuid.New()}, nil
						}
						return nil, gorm.ErrRecordNotFound
					}},
					&mockContractSource{findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) { return c, nil }},
					&mockMeterReadingSource{findLatestFn: func(_ context.Context, _ uuid.UUID) (*meterreading.MeterReading, error) {
						return testExitReading(c.RoomID, moveOut), nil
					}},
					&mockBillingConfigSource{},
					&mockMoveOutSource{notice: completedNotice(c.ID, moveOut)})
			},
			billing.ErrBillAlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := tt.setup()
			_, err := svc.CreateSettlementBill(context.Background(), CreateSettlementBillRequest{
				ContractID: uuid.New().String(),
			}, nil)
			if err != tt.wantErr {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// ============================================================
// Day arithmetic + rent mode helpers
// ============================================================

func TestDaysInMonth(t *testing.T) {
	tests := []struct {
		date     time.Time
		expected int
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 31},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 28},
		{time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), 29},
		{time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 30},
	}
	for _, tt := range tests {
		if got := daysInMonth(tt.date); got != tt.expected {
			t.Errorf("daysInMonth(%v) = %d, want %d", tt.date, got, tt.expected)
		}
	}
}

func TestEndOfMonth(t *testing.T) {
	tests := []struct {
		date time.Time
		want time.Time
	}{
		{time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got := endOfMonth(tt.date)
		if !got.Equal(tt.want) {
			t.Errorf("endOfMonth(%s) = %s, want %s", tt.date.Format("2006-01-02"), got.Format("2006-01-02"), tt.want.Format("2006-01-02"))
		}
	}
}

func TestEffectiveMoveOutDate(t *testing.T) {
	mid := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	eom := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	if got := effectiveMoveOutDate(mid, billing.RentModeProrated); !got.Equal(mid) {
		t.Errorf("PRORATED: got %s, want %s", got.Format("2006-01-02"), mid.Format("2006-01-02"))
	}
	if got := effectiveMoveOutDate(mid, billing.RentModeFullMonthKeepDeposit); !got.Equal(eom) {
		t.Errorf("FULL_MONTH: got %s, want %s", got.Format("2006-01-02"), eom.Format("2006-01-02"))
	}
}

// ============================================================
// Deposit qualification with rent mode
// ============================================================

func TestDepositQualification_RentMode(t *testing.T) {
	// Contract: start Jan 15, minMonths = 6 → eligible at July 15
	// Using mid-month start so FULL_MONTH can cross the eligibility threshold.
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	minMonths := 6

	tests := []struct {
		name    string
		moveOut time.Time
		mode    billing.SettlementRentMode
		wantRet bool
	}{
		// July 10 — PRORATED: July 10 < July 15 → forfeited
		{"jul10_prorated", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), billing.RentModeProrated, false},
		// July 10 — FULL_MONTH: effective = July 31 >= July 15 → returnable
		{"jul10_full_month", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), billing.RentModeFullMonthKeepDeposit, true},
		// March 10 — FULL_MONTH: effective = March 31, ~2.5 months < 6 → still forfeited
		{"march_full_month_still_short", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), billing.RentModeFullMonthKeepDeposit, false},
		// July 15 — PRORATED: exactly 6 months → returnable
		{"jul15_prorated", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), billing.RentModeProrated, true},
		// July 14 — PRORATED: 1 day short → forfeited
		{"jul14_prorated", time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), billing.RentModeProrated, false},
		// July 14 — FULL_MONTH: effective = July 31 >= July 15 → returnable
		{"jul14_full_month", time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), billing.RentModeFullMonthKeepDeposit, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qualifyDate := effectiveMoveOutDate(tt.moveOut, tt.mode)
			got := isDepositReturnable(start, qualifyDate, minMonths)
			if got != tt.wantRet {
				t.Errorf("returnable = %v, want %v (qualifyDate = %s)", got, tt.wantRet, qualifyDate.Format("2006-01-02"))
			}
		})
	}
}

// --- Deposit eligibility ---

func TestIsDepositReturnable(t *testing.T) {
	start := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		moveOut   time.Time
		minMonths int
		want      bool
	}{
		// Exact boundary: start=Jan 20, min=6 → must reach Jul 20
		{"exact day — eligible", time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), 6, true},
		{"one day before — not eligible", time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), 6, false},
		{"one day after — eligible", time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC), 6, true},

		// Well before / well after
		{"3 months — not eligible", time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), 6, false},
		{"12 months — eligible", time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 6, true},

		// minMonths = 0 → always eligible
		{"zero min — same day eligible", start, 0, true},

		// Same day = not eligible (0 months stayed, need 6)
		{"same day — not eligible", start, 6, false},

		// End-of-month tested separately below (different start date)

		// moveOut before start = not eligible (data anomaly guard)
		{"moveOut before start — not eligible", time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), 6, false},

		// Cross-year
		{"cross year — eligible", time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 12, true},
		{"cross year — not eligible", time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC), 12, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDepositReturnable(start, tt.moveOut, tt.minMonths)
			if got != tt.want {
				t.Errorf("isDepositReturnable(%s, %s, %d) = %v, want %v",
					start.Format("2006-01-02"), tt.moveOut.Format("2006-01-02"), tt.minMonths, got, tt.want)
			}
		})
	}
}

func TestIsDepositReturnable_EndOfMonth(t *testing.T) {
	// Jan 31 + 1 month = Feb 28 (clamped, not Mar 3)
	t.Run("Jan31+1m: Feb 27 — not eligible", func(t *testing.T) {
		start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
	t.Run("Jan31+1m: Feb 28 — eligible", func(t *testing.T) {
		start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	// Aug 31 + 1 month = Sep 30
	t.Run("Aug31+1m: Sep 29 — not eligible", func(t *testing.T) {
		start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
	t.Run("Aug31+1m: Sep 30 — eligible", func(t *testing.T) {
		start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	// Leap year: Jan 31 + 1 month = Feb 29 in 2024
	t.Run("Jan31+1m leap year: Feb 29 — eligible", func(t *testing.T) {
		start := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
		if !isDepositReturnable(start, time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected true")
		}
	})
	t.Run("Jan31+1m leap year: Feb 28 — not eligible", func(t *testing.T) {
		start := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
		if isDepositReturnable(start, time.Date(2024, 2, 28, 0, 0, 0, 0, time.UTC), 1) {
			t.Error("expected false")
		}
	})
}

func TestComputeDepositSettlement(t *testing.T) {
	t.Run("returnable — deposit exceeds charges → refund", func(t *testing.T) {
		ds := computeDepositSettlement(1000000, 600000, true) // 10k deposit, 6k charges
		if ds.OriginalAmount != 1000000 {
			t.Errorf("OriginalAmount = %d, want 1000000", ds.OriginalAmount)
		}
		if ds.ForfeitedAmount != 0 {
			t.Errorf("ForfeitedAmount = %d, want 0", ds.ForfeitedAmount)
		}
		if ds.AppliedAmount != 600000 {
			t.Errorf("AppliedAmount = %d, want 600000", ds.AppliedAmount)
		}
		if ds.RefundAmount != 400000 {
			t.Errorf("RefundAmount = %d, want 400000", ds.RefundAmount)
		}
		if ds.AmountDue != 0 {
			t.Errorf("AmountDue = %d, want 0", ds.AmountDue)
		}
	})

	t.Run("returnable — charges exceed deposit → tenant pays", func(t *testing.T) {
		ds := computeDepositSettlement(300000, 800000, true) // 3k deposit, 8k charges
		if ds.AppliedAmount != 300000 {
			t.Errorf("AppliedAmount = %d, want 300000", ds.AppliedAmount)
		}
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0", ds.RefundAmount)
		}
		if ds.AmountDue != 500000 {
			t.Errorf("AmountDue = %d, want 500000", ds.AmountDue)
		}
	})

	t.Run("early exit — deposit forfeited, not applied to charges", func(t *testing.T) {
		ds := computeDepositSettlement(1000000, 400000, false) // 10k deposit, 4k charges
		if ds.AvailableToApply != 0 {
			t.Errorf("AvailableToApply = %d, want 0 (forfeited deposit not available)", ds.AvailableToApply)
		}
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited deposit not applied)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 1000000 {
			t.Errorf("ForfeitedAmount = %d, want 1000000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0", ds.RefundAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("early exit — charges exceed deposit, tenant pays full charges", func(t *testing.T) {
		ds := computeDepositSettlement(300000, 800000, false) // 3k deposit, 8k charges
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited deposit not applied)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 300000 {
			t.Errorf("ForfeitedAmount = %d, want 300000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 800000 {
			t.Errorf("AmountDue = %d, want 800000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("early exit — zero deposit, tenant pays full charges", func(t *testing.T) {
		ds := computeDepositSettlement(0, 400000, false)
		if ds.ForfeitedAmount != 0 {
			t.Errorf("ForfeitedAmount = %d, want 0 (nothing to forfeit)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000", ds.AmountDue)
		}
	})

	t.Run("early exit — charges exactly equal deposit", func(t *testing.T) {
		ds := computeDepositSettlement(500000, 500000, false) // 5k deposit = 5k charges
		if ds.AppliedAmount != 0 {
			t.Errorf("AppliedAmount = %d, want 0 (forfeited)", ds.AppliedAmount)
		}
		if ds.ForfeitedAmount != 500000 {
			t.Errorf("ForfeitedAmount = %d, want 500000 (entire deposit forfeited)", ds.ForfeitedAmount)
		}
		if ds.AmountDue != 500000 {
			t.Errorf("AmountDue = %d, want 500000 (tenant pays full charges)", ds.AmountDue)
		}
	})

	t.Run("returnable — zero deposit", func(t *testing.T) {
		ds := computeDepositSettlement(0, 400000, true)
		if ds.RefundAmount != 0 {
			t.Errorf("RefundAmount = %d, want 0 (no deposit to refund)", ds.RefundAmount)
		}
		if ds.AmountDue != 400000 {
			t.Errorf("AmountDue = %d, want 400000", ds.AmountDue)
		}
	})
}
