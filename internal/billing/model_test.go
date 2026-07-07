package billing

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// --- ComputedSnapshot ---

func validSnapshot() ComputedSnapshot {
	return ComputedSnapshot{
		Version: ComputedSnapshotVersion,
		LineItems: []ComputedLineItem{
			{Type: LineItemRoomRent, Description: "ค่าห้อง", Amount: 500000, SortOrder: 1},
			{Type: LineItemElectricity, Description: "ค่าไฟ", Amount: 30000, Quantity: 50, UnitPrice: 600, SortOrder: 2},
		},
		TotalAmount: 530000,
		ComputedAt:  time.Now(),
	}
}

func TestComputedSnapshot_Validate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		s := validSnapshot()
		if err := s.Validate(); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
	t.Run("unsupported version", func(t *testing.T) {
		s := validSnapshot()
		s.Version = 2
		if err := s.Validate(); !errors.Is(err, ErrSnapshotUnsupportedVersion) {
			t.Fatalf("expected ErrSnapshotUnsupportedVersion, got %v", err)
		}
	})
	t.Run("empty line items", func(t *testing.T) {
		s := validSnapshot()
		s.LineItems = nil
		if err := s.Validate(); !errors.Is(err, ErrSnapshotNoLineItems) {
			t.Fatalf("expected ErrSnapshotNoLineItems, got %v", err)
		}
	})
	t.Run("negative total", func(t *testing.T) {
		s := validSnapshot()
		s.TotalAmount = -1
		if err := s.Validate(); !errors.Is(err, ErrSnapshotNegativeTotal) {
			t.Fatalf("expected ErrSnapshotNegativeTotal, got %v", err)
		}
	})
}

func TestComputedSnapshot_ToLineItems(t *testing.T) {
	s := validSnapshot()
	billID := uuid.New()
	items := s.ToLineItems(billID)
	if len(items) != len(s.LineItems) {
		t.Fatalf("expected %d items, got %d", len(s.LineItems), len(items))
	}
	for i, it := range items {
		if it.BillID != billID {
			t.Errorf("item %d: BillID = %v, want %v", i, it.BillID, billID)
		}
		if it.LineType != s.LineItems[i].Type {
			t.Errorf("item %d: LineType = %v, want %v", i, it.LineType, s.LineItems[i].Type)
		}
		if it.Amount != s.LineItems[i].Amount {
			t.Errorf("item %d: Amount = %v, want %v", i, it.Amount, s.LineItems[i].Amount)
		}
	}
}

// --- BillGenerationBatch commit helpers ---

func TestBillGenerationBatch_CanCommit(t *testing.T) {
	committed := CommitStatusCommitted
	partial := CommitStatusPartiallyCommitted
	failed := CommitStatusFailed
	tests := []struct {
		name    string
		status  *CommitStatus
		wantErr error
	}{
		{"nil ok", nil, nil},
		{"already committed", &committed, ErrBatchAlreadyCommitted},
		{"partial retryable", &partial, nil},
		{"failed retryable", &failed, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BillGenerationBatch{CommitStatus: tt.status}
			err := b.CanCommit()
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestBillGenerationBatch_MarkCommitResult(t *testing.T) {
	t.Run("all success → COMMITTED", func(t *testing.T) {
		b := &BillGenerationBatch{}
		b.MarkCommitResult(5, 0, 0)
		if b.CommitStatus == nil || *b.CommitStatus != CommitStatusCommitted {
			t.Fatalf("expected COMMITTED, got %v", b.CommitStatus)
		}
		if b.CommittedAt == nil {
			t.Fatalf("expected CommittedAt set")
		}
	})
	t.Run("mixed → PARTIALLY_COMMITTED", func(t *testing.T) {
		b := &BillGenerationBatch{}
		b.MarkCommitResult(3, 2, 0)
		if b.CommitStatus == nil || *b.CommitStatus != CommitStatusPartiallyCommitted {
			t.Fatalf("expected PARTIALLY_COMMITTED, got %v", b.CommitStatus)
		}
		if b.CommittedAt == nil {
			t.Fatalf("expected CommittedAt set")
		}
	})
	t.Run("all failed → FAILED", func(t *testing.T) {
		b := &BillGenerationBatch{}
		b.MarkCommitResult(0, 3, 0)
		if b.CommitStatus == nil || *b.CommitStatus != CommitStatusFailed {
			t.Fatalf("expected FAILED, got %v", b.CommitStatus)
		}
		if b.CommittedAt != nil {
			t.Fatalf("expected CommittedAt nil on FAILED")
		}
	})
	t.Run("pending > 0 + success > 0 → PARTIALLY_COMMITTED", func(t *testing.T) {
		// Loop broke mid-flight via an infra error after some bills landed.
		// Pre-B3 this left CommitStatus = nil and the FE wedged on the
		// pre-commit toolbar (no signal that progress happened or that
		// retry was needed). Now the dual-status model carries the partial
		// state honestly so the FE retry path activates.
		b := &BillGenerationBatch{}
		b.MarkCommitResult(1, 0, 2)
		if b.CommitStatus == nil || *b.CommitStatus != CommitStatusPartiallyCommitted {
			t.Fatalf("expected PARTIALLY_COMMITTED, got %v", b.CommitStatus)
		}
		if b.CommittedAt == nil {
			t.Fatalf("expected CommittedAt set on partial progress")
		}
	})
	t.Run("pending > 0 + success == 0 → FAILED", func(t *testing.T) {
		// Loop broke on the very first item — nothing committed. Pre-B3
		// this left CommitStatus = nil. Now FAILED so canRetry surfaces
		// the retry CTA on the FE instead of silently sitting on pre-commit.
		b := &BillGenerationBatch{}
		b.MarkCommitResult(0, 0, 3)
		if b.CommitStatus == nil || *b.CommitStatus != CommitStatusFailed {
			t.Fatalf("expected FAILED, got %v", b.CommitStatus)
		}
		if b.CommittedAt != nil {
			t.Fatalf("expected CommittedAt nil when nothing committed")
		}
	})
}

// --- Status checks ---

func TestBill_StatusChecks(t *testing.T) {
	tests := []struct {
		name        string
		status      BillStatus
		isDraft     bool
		isFinalized bool
		isPaid      bool
		isVoid      bool
	}{
		{"draft", BillStatusDraft, true, false, false, false},
		{"finalized", BillStatusFinalized, false, true, false, false},
		{"paid", BillStatusPaid, false, false, true, false},
		{"void", BillStatusVoid, false, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bill{Status: tt.status}
			if b.IsDraft() != tt.isDraft {
				t.Errorf("IsDraft() = %v, want %v", b.IsDraft(), tt.isDraft)
			}
			if b.IsFinalized() != tt.isFinalized {
				t.Errorf("IsFinalized() = %v, want %v", b.IsFinalized(), tt.isFinalized)
			}
			if b.IsPaid() != tt.isPaid {
				t.Errorf("IsPaid() = %v, want %v", b.IsPaid(), tt.isPaid)
			}
			if b.IsVoid() != tt.isVoid {
				t.Errorf("IsVoid() = %v, want %v", b.IsVoid(), tt.isVoid)
			}
		})
	}
}

func TestBill_TypeChecks(t *testing.T) {
	monthly := &Bill{BillType: BillTypeMonthly}
	if !monthly.IsMonthly() || monthly.IsSettlement() {
		t.Error("expected monthly")
	}
	settlement := &Bill{BillType: BillTypeSettlement}
	if !settlement.IsSettlement() || settlement.IsMonthly() {
		t.Error("expected settlement")
	}
}

// --- State transitions ---

func TestBill_Finalize(t *testing.T) {
	t.Run("draft with items — success", func(t *testing.T) {
		b := &Bill{
			Status:    BillStatusDraft,
			LineItems: []BillLineItem{{Amount: 100}},
		}
		if err := b.Finalize(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Status != BillStatusFinalized {
			t.Fatalf("expected FINALIZED, got %s", b.Status)
		}
	})

	t.Run("draft without items — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft}
		if err := b.Finalize(); err != ErrNoLineItems {
			t.Fatalf("expected ErrNoLineItems, got %v", err)
		}
	})

	t.Run("finalized — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusFinalized}
		if err := b.Finalize(); err != ErrNotDraft {
			t.Fatalf("expected ErrNotDraft, got %v", err)
		}
	})

	t.Run("paid — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusPaid}
		if err := b.Finalize(); err != ErrNotDraft {
			t.Fatalf("expected ErrNotDraft, got %v", err)
		}
	})
}

func TestBill_Void(t *testing.T) {
	t.Run("draft — success", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft}
		if err := b.Void("ยกเลิก"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Status != BillStatusVoid {
			t.Fatalf("expected VOID, got %s", b.Status)
		}
		if b.VoidReason == nil || *b.VoidReason != "ยกเลิก" {
			t.Fatal("void reason not set")
		}
	})

	t.Run("finalized — success", func(t *testing.T) {
		b := &Bill{Status: BillStatusFinalized}
		if err := b.Void("เปลี่ยนบิล"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Status != BillStatusVoid {
			t.Fatalf("expected VOID, got %s", b.Status)
		}
	})

	t.Run("paid — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusPaid}
		if err := b.Void("test"); err != ErrAlreadyPaid {
			t.Fatalf("expected ErrAlreadyPaid, got %v", err)
		}
	})

	t.Run("already void — idempotent no-op", func(t *testing.T) {
		reason := "old reason"
		b := &Bill{Status: BillStatusVoid, VoidReason: &reason}
		if err := b.Void("new reason"); err != nil {
			t.Fatalf("expected nil (idempotent), got %v", err)
		}
		// Must not overwrite existing reason
		if *b.VoidReason != "old reason" {
			t.Fatalf("void reason should not change, got %q", *b.VoidReason)
		}
	})

	t.Run("empty reason — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft}
		if err := b.Void(""); err != ErrVoidReasonEmpty {
			t.Fatalf("expected ErrVoidReasonEmpty, got %v", err)
		}
	})
}

func TestBill_MarkPaid(t *testing.T) {
	t.Run("finalized — success", func(t *testing.T) {
		b := &Bill{Status: BillStatusFinalized}
		if err := b.MarkPaid(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.Status != BillStatusPaid {
			t.Fatalf("expected PAID, got %s", b.Status)
		}
	})

	t.Run("draft — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft}
		if err := b.MarkPaid(); err != ErrNotFinalized {
			t.Fatalf("expected ErrNotFinalized, got %v", err)
		}
	})

	t.Run("paid — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusPaid}
		if err := b.MarkPaid(); err != ErrNotFinalized {
			t.Fatalf("expected ErrNotFinalized, got %v", err)
		}
	})
}

// TestBill_FinalizedAt covers the audit invariant for the finalized_at
// timestamp: set by Finalize, never cleared by subsequent transitions.
//
// This is the kind of invariant that fails silently — a future refactor that
// resets the timestamp on MarkPaid would still pass status-machine tests but
// destroy the AR aging signal the FE depends on.
func TestBill_FinalizedAt(t *testing.T) {
	t.Run("Finalize sets FinalizedAt to ~now", func(t *testing.T) {
		b := &Bill{
			Status:    BillStatusDraft,
			LineItems: []BillLineItem{{Amount: 100}},
		}
		before := time.Now()
		if err := b.Finalize(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after := time.Now()
		if b.FinalizedAt == nil {
			t.Fatal("FinalizedAt was not set")
		}
		if b.FinalizedAt.Before(before) || b.FinalizedAt.After(after) {
			t.Fatalf("FinalizedAt %v outside [%v, %v]", b.FinalizedAt, before, after)
		}
	})

	t.Run("Finalize on invalid bill leaves FinalizedAt nil", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft} // no line items
		_ = b.Finalize()                    // returns ErrNoLineItems
		if b.FinalizedAt != nil {
			t.Fatalf("FinalizedAt set despite Finalize() failure: %v", b.FinalizedAt)
		}
	})

	t.Run("MarkPaid does not clear FinalizedAt", func(t *testing.T) {
		stamp := time.Now().Add(-2 * time.Hour)
		b := &Bill{Status: BillStatusFinalized, FinalizedAt: &stamp}
		if err := b.MarkPaid(); err != nil {
			t.Fatalf("MarkPaid: %v", err)
		}
		if b.FinalizedAt == nil || !b.FinalizedAt.Equal(stamp) {
			t.Fatalf("FinalizedAt mutated by MarkPaid: %v (want %v)", b.FinalizedAt, stamp)
		}
	})

	t.Run("Void does not clear FinalizedAt", func(t *testing.T) {
		stamp := time.Now().Add(-2 * time.Hour)
		b := &Bill{Status: BillStatusFinalized, FinalizedAt: &stamp}
		if err := b.Void("ทดสอบ"); err != nil {
			t.Fatalf("Void: %v", err)
		}
		if b.FinalizedAt == nil || !b.FinalizedAt.Equal(stamp) {
			t.Fatalf("FinalizedAt mutated by Void: %v (want %v)", b.FinalizedAt, stamp)
		}
	})

	t.Run("DRAFT bill has nil FinalizedAt", func(t *testing.T) {
		b := &Bill{Status: BillStatusDraft, LineItems: []BillLineItem{{Amount: 100}}}
		if b.FinalizedAt != nil {
			t.Fatalf("DRAFT bill should have nil FinalizedAt, got %v", b.FinalizedAt)
		}
	})
}

// TestBill_PaidAmount_OutstandingAmount locks the AR-lite formula:
//   - PAID  → paid = total, outstanding = 0
//   - DRAFT, FINALIZED → paid = 0, outstanding = total
//   - VOID  → paid = 0, outstanding = 0 (excluded from AR)
//
// These projections assume atomic 1-bill-1-payment. If a future refactor
// silently changes the rule (e.g. DRAFT becomes "0 outstanding because not
// finalized yet"), the FE summary cards will diverge from per-row totals.
func TestBill_PaidAmount_OutstandingAmount(t *testing.T) {
	cases := []struct {
		name            string
		status          BillStatus
		total           int64
		wantPaid        int64
		wantOutstanding int64
	}{
		{"DRAFT", BillStatusDraft, 1500, 0, 1500},
		{"FINALIZED", BillStatusFinalized, 2500, 0, 2500},
		{"PAID", BillStatusPaid, 3500, 3500, 0},
		{"VOID", BillStatusVoid, 4500, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bill{Status: tc.status, TotalAmount: tc.total}
			if got := b.PaidAmount(); got != tc.wantPaid {
				t.Errorf("PaidAmount = %d, want %d", got, tc.wantPaid)
			}
			if got := b.OutstandingAmount(); got != tc.wantOutstanding {
				t.Errorf("OutstandingAmount = %d, want %d", got, tc.wantOutstanding)
			}
		})
	}
}

// --- Calculations ---

func TestBill_CalculateTotal(t *testing.T) {
	t.Run("monthly bill", func(t *testing.T) {
		b := &Bill{
			BillType: BillTypeMonthly,
			LineItems: []BillLineItem{
				{Amount: 500000}, // 5,000 baht room rent
				{Amount: 120000}, // 1,200 baht electricity
				{Amount: 30000},  // 300 baht water
			},
		}
		b.CalculateTotal()
		if b.TotalAmount != 650000 {
			t.Fatalf("expected 650000, got %d", b.TotalAmount)
		}
	})

	t.Run("settlement bill with deposit", func(t *testing.T) {
		b := &Bill{
			BillType:      BillTypeSettlement,
			DepositAmount: 1000000, // 10,000 baht deposit
			LineItems: []BillLineItem{
				{Amount: 120000},  // electricity
				{Amount: 30000},   // water
				{Amount: 50000},   // cleaning fee
				{Amount: -500000}, // prepaid credit (negative)
			},
		}
		b.CalculateTotal()

		// effective_total = 120000 + 30000 + 50000 - 500000 = -300000
		if b.TotalAmount != -300000 {
			t.Fatalf("expected TotalAmount -300000, got %d", b.TotalAmount)
		}
		// deposit_balance = 1000000 - (-300000) = 1300000
		if b.DepositBalance != 1300000 {
			t.Fatalf("expected DepositBalance 1300000, got %d", b.DepositBalance)
		}
	})

	t.Run("settlement bill with forfeited deposit", func(t *testing.T) {
		b := &Bill{
			BillType:         BillTypeSettlement,
			DepositAmount:    300000, // 3,000 baht deposit
			DepositForfeited: true,
			LineItems: []BillLineItem{
				{Amount: 120000}, // electricity
				{Amount: 30000},  // water
				{Amount: 50000},  // cleaning fee
			},
		}
		b.CalculateTotal()

		if b.TotalAmount != 200000 {
			t.Fatalf("expected TotalAmount 200000, got %d", b.TotalAmount)
		}
		// Forfeited: deposit NOT applied → DepositBalance = -TotalAmount
		if b.DepositBalance != -200000 {
			t.Fatalf("expected DepositBalance -200000 (forfeited), got %d", b.DepositBalance)
		}
	})
}

func TestBill_ChargesTotalAndCreditsTotal(t *testing.T) {
	b := &Bill{
		LineItems: []BillLineItem{
			{Amount: 500000},
			{Amount: 120000},
			{Amount: -200000},
			{Amount: 30000},
		},
	}

	if b.ChargesTotal() != 650000 {
		t.Fatalf("expected charges 650000, got %d", b.ChargesTotal())
	}
	if b.CreditsTotal() != 200000 {
		t.Fatalf("expected credits 200000, got %d", b.CreditsTotal())
	}
}

// TestBill_EditableManualItems locks the Q1.6 discriminator that both the draft
// audit diff and the settlement void+recreate carry-over depend on: a recovery
// refund (Source=MANUAL, LineType=ADJUSTMENT) is system-owned and must NOT be
// treated as a hand-editable manual item — otherwise it is spuriously audited as
// removed, or double-emitted when a settlement is recreated.
func TestBill_EditableManualItems(t *testing.T) {
	b := &Bill{
		LineItems: []BillLineItem{
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 120000}, // AUTO — excluded
			{LineType: LineItemOther, Source: LineItemSourceManual, Amount: 30000},        // editable manual
			{LineType: LineItemAdjustment, Source: LineItemSourceManual, Amount: -24000}, // recovery refund — excluded
		},
	}

	// ManualItems() still returns BOTH manual rows (unchanged semantics).
	if got := len(b.ManualItems()); got != 2 {
		t.Fatalf("ManualItems() = %d, want 2 (fee + recovery adjustment)", got)
	}

	// EditableManualItems() drops the recovery ADJUSTMENT.
	editable := b.EditableManualItems()
	if len(editable) != 1 {
		t.Fatalf("EditableManualItems() = %d, want 1 (fee only)", len(editable))
	}
	if editable[0].LineType != LineItemOther {
		t.Errorf("EditableManualItems() kept %s, want OTHER", editable[0].LineType)
	}

	// Item-level predicate.
	adj := &BillLineItem{LineType: LineItemAdjustment, Source: LineItemSourceManual}
	if adj.IsEditableManual() {
		t.Error("recovery ADJUSTMENT must not be editable-manual")
	}
	fee := &BillLineItem{LineType: LineItemOther, Source: LineItemSourceManual}
	if !fee.IsEditableManual() {
		t.Error("manual OTHER must be editable-manual")
	}
}

// --- Line item factories ---

func TestNewRoomRentLine(t *testing.T) {
	line := NewRoomRentLine(500000, "ค่าห้อง 2026-05", 1)
	if line.LineType != LineItemRoomRent {
		t.Fatalf("expected ROOM_RENT, got %s", line.LineType)
	}
	if line.Amount != 500000 {
		t.Fatalf("expected 500000, got %d", line.Amount)
	}
	if line.SortOrder != 1 {
		t.Fatalf("expected sort 1, got %d", line.SortOrder)
	}
}

func TestNewElectricityLine(t *testing.T) {
	line := NewElectricityLine(150, 800, "ค่าไฟ 150 หน่วย", 2)
	if line.LineType != LineItemElectricity {
		t.Fatalf("expected ELECTRICITY, got %s", line.LineType)
	}
	if line.Amount != 120000 {
		t.Fatalf("expected 120000, got %d", line.Amount)
	}
	if line.Quantity != 150 {
		t.Fatalf("expected qty 150, got %d", line.Quantity)
	}
	if line.UnitPrice != 800 {
		t.Fatalf("expected unit price 800, got %d", line.UnitPrice)
	}
}

func TestNewWaterLine(t *testing.T) {
	line := NewWaterLine(10, 1800, "ค่าน้ำ 10 หน่วย", 3)
	if line.Amount != 18000 {
		t.Fatalf("expected 18000, got %d", line.Amount)
	}
}

func TestNewProrateRentLine(t *testing.T) {
	// 15 days at ฿100/day flat rate (10000 satang) → 150000 satang = ฿1500.
	// Flat rate × days is exact — no rounding ambiguity.
	line := NewProrateRentLine(15, 10000, "15 วัน × ฿100/วัน", 1)
	if line.LineType != LineItemProrateRent {
		t.Fatalf("expected PRORATE_RENT, got %s", line.LineType)
	}
	if line.Amount != 150000 {
		t.Fatalf("expected 150000, got %d", line.Amount)
	}
	if line.Quantity != 15 {
		t.Fatalf("expected qty 15, got %d", line.Quantity)
	}
	if line.UnitPrice != 10000 {
		t.Fatalf("expected unit price 10000 (฿100/day), got %d", line.UnitPrice)
	}
}

func TestNewPrepaidCreditLine(t *testing.T) {
	line := NewPrepaidCreditLine(500000, "หักค่าห้องที่จ่ายล่วงหน้า", 10)
	if line.LineType != LineItemPrepaidCredit {
		t.Fatalf("expected PREPAID_CREDIT, got %s", line.LineType)
	}
	if line.Amount != -500000 {
		t.Fatalf("expected -500000, got %d", line.Amount)
	}
}

func TestNewFeeLine(t *testing.T) {
	line := NewFeeLine(LineItemCleaningFee, 50000, "ค่าทำความสะอาด", 5)
	if line.LineType != LineItemCleaningFee {
		t.Fatalf("expected CLEANING_FEE, got %s", line.LineType)
	}
	if line.Amount != 50000 {
		t.Fatalf("expected 50000, got %d", line.Amount)
	}
}

// --- Override + DepositBreakdown ---

func TestCalculateTotal_WithOverrides(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 200000,
		Overrides: OverrideMap{
			"ELECTRICITY": 80000, // override from 120000 to 80000
		},
		LineItems: []BillLineItem{
			{LineType: LineItemProrateRent, Source: LineItemSourceAuto, Amount: 150000},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 120000},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 30000},
		},
	}
	b.CalculateTotal()

	// 150000 + 80000(override) + 30000 = 260000
	if b.TotalAmount != 260000 {
		t.Fatalf("TotalAmount = %d, want 260000", b.TotalAmount)
	}
}

// MONTHLY drafts also accept overrides (Phase 1 editable bills). Total must
// reflect the override, otherwise `bills.total_amount` drifts from the sum
// of effective line item amounts that the DTO mapper surfaces — admin sees
// breakdown ≠ total, batch summary stays stale, and finalize ships the
// wrong amount.
func TestCalculateTotal_OverrideAppliedForMonthly(t *testing.T) {
	b := &Bill{
		BillType: BillTypeMonthly,
		Overrides: OverrideMap{
			"ROOM_RENT": 350000, // override 250000 → 350000 (+100000 satang)
		},
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 250000},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 120000},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 30000},
		},
	}
	b.CalculateTotal()

	// override(350000) + 120000 + 30000 = 500000
	if b.TotalAmount != 500000 {
		t.Fatalf("TotalAmount = %d, want 500000 (monthly override applied)", b.TotalAmount)
	}
}

// Locks the DTO ↔ model invariant: bill.total_amount must equal the sum of
// the effective amounts that toLineItemResponse surfaces (override substituted
// for AUTO items). This is the exact invariant the in-context BatchItemDrawer
// preview depends on after an edit; failing it makes the live preview total
// drift from line item totals visible to the admin.
func TestCalculateTotal_MonthlyTotalEqualsEffectiveLineItemSum(t *testing.T) {
	b := &Bill{
		BillType: BillTypeMonthly,
		Overrides: OverrideMap{
			"ROOM_RENT": 999, // override below original
		},
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 250000},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 120000},
			{LineType: LineItemCleaningFee, Source: LineItemSourceManual, Amount: 50000},
		},
	}
	b.CalculateTotal()

	// Effective: 999 (overridden) + 120000 + 50000 (MANUAL untouched) = 170999
	var effectiveSum int64
	for _, li := range b.LineItems {
		if li.IsAuto() {
			if v, ok := b.Overrides[li.OverrideKey()]; ok {
				effectiveSum += v
				continue
			}
		}
		effectiveSum += li.Amount
	}
	if b.TotalAmount != effectiveSum {
		t.Fatalf("TotalAmount = %d, want effectiveSum = %d", b.TotalAmount, effectiveSum)
	}
}

func TestCalculateTotal_OverrideIgnoredForManualItems(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 0,
		Overrides: OverrideMap{
			"CLEANING_FEE": 1, // should NOT apply to MANUAL items
		},
		LineItems: []BillLineItem{
			{LineType: LineItemCleaningFee, Source: LineItemSourceManual, Amount: 50000},
		},
	}
	b.CalculateTotal()

	if b.TotalAmount != 50000 {
		t.Fatalf("TotalAmount = %d, want 50000 (override ignored for MANUAL)", b.TotalAmount)
	}
}

func TestDepositBreakdown_Full_Returnable(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 500000, // 5000 baht
		TotalAmount:   300000, // 3000 baht
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 300000 {
		t.Errorf("Applied = %d, want 300000", bd.AppliedAmount)
	}
	if bd.RefundAmount != 200000 {
		t.Errorf("Refund = %d, want 200000", bd.RefundAmount)
	}
	if bd.WithheldAmount != 0 {
		t.Errorf("Withheld = %d, want 0", bd.WithheldAmount)
	}
	if bd.AmountDue != 0 {
		t.Errorf("Due = %d, want 0", bd.AmountDue)
	}
}

func TestDepositBreakdown_Full_Returnable_ChargesExceedDeposit(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 200000, // 2000 baht
		TotalAmount:   350000, // 3500 baht
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 200000 {
		t.Errorf("Applied = %d, want 200000", bd.AppliedAmount)
	}
	if bd.RefundAmount != 0 {
		t.Errorf("Refund = %d, want 0", bd.RefundAmount)
	}
	if bd.AmountDue != 150000 {
		t.Errorf("Due = %d, want 150000", bd.AmountDue)
	}
}

func TestDepositBreakdown_Full_Forfeited(t *testing.T) {
	b := &Bill{
		BillType:         BillTypeSettlement,
		DepositAmount:    300000,
		DepositForfeited: true,
		TotalAmount:      200000,
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 0 {
		t.Errorf("Applied = %d, want 0 (forfeited)", bd.AppliedAmount)
	}
	if bd.WithheldAmount != 300000 {
		t.Errorf("Withheld = %d, want 300000", bd.WithheldAmount)
	}
	if bd.AmountDue != 200000 {
		t.Errorf("Due = %d, want 200000", bd.AmountDue)
	}
	if bd.RefundAmount != 0 {
		t.Errorf("Refund = %d, want 0", bd.RefundAmount)
	}
}

func TestDepositBreakdown_None(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 500000,
		DepositApp:    DepositAppNone,
		TotalAmount:   300000,
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 0 {
		t.Errorf("Applied = %d, want 0", bd.AppliedAmount)
	}
	if bd.WithheldAmount != 500000 {
		t.Errorf("Withheld = %d, want 500000", bd.WithheldAmount)
	}
	if bd.AmountDue != 300000 {
		t.Errorf("Due = %d, want 300000", bd.AmountDue)
	}
	if bd.RefundAmount != 0 {
		t.Errorf("Refund = %d, want 0", bd.RefundAmount)
	}
}

func TestDepositBreakdown_Custom(t *testing.T) {
	b := &Bill{
		BillType:             BillTypeSettlement,
		DepositAmount:        500000,
		DepositApp:           DepositAppCustom,
		CustomDepositApplied: 200000, // apply 2000 of 5000 deposit
		TotalAmount:          300000,
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 200000 {
		t.Errorf("Applied = %d, want 200000", bd.AppliedAmount)
	}
	if bd.RefundAmount != 300000 {
		t.Errorf("Refund = %d, want 300000 (500000 - 200000)", bd.RefundAmount)
	}
	if bd.AmountDue != 100000 {
		t.Errorf("Due = %d, want 100000 (300000 - 200000)", bd.AmountDue)
	}
}

func TestDepositBreakdown_Custom_ClampedToDeposit(t *testing.T) {
	b := &Bill{
		BillType:             BillTypeSettlement,
		DepositAmount:        200000,
		DepositApp:           DepositAppCustom,
		CustomDepositApplied: 500000, // exceeds deposit → clamped to 200000
		TotalAmount:          300000,
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 200000 {
		t.Errorf("Applied = %d, want 200000 (clamped to deposit)", bd.AppliedAmount)
	}
	if bd.AmountDue != 100000 {
		t.Errorf("Due = %d, want 100000", bd.AmountDue)
	}
}

func TestDepositBreakdown_Custom_ClampedToCharges(t *testing.T) {
	b := &Bill{
		BillType:             BillTypeSettlement,
		DepositAmount:        500000,
		DepositApp:           DepositAppCustom,
		CustomDepositApplied: 400000, // exceeds charges → clamped to 100000
		TotalAmount:          100000,
	}
	bd := b.DepositBreakdown()

	if bd.AppliedAmount != 100000 {
		t.Errorf("Applied = %d, want 100000 (clamped to charges)", bd.AppliedAmount)
	}
	if bd.RefundAmount != 400000 {
		t.Errorf("Refund = %d, want 400000", bd.RefundAmount)
	}
	if bd.AmountDue != 0 {
		t.Errorf("Due = %d, want 0", bd.AmountDue)
	}
}

func TestPruneStaleOverrides(t *testing.T) {
	b := &Bill{
		BillType: BillTypeSettlement,
		Overrides: OverrideMap{
			"ELECTRICITY":      80000,
			"PRORATE_RENT":     150000,
			"OUTSTANDING_BILL": 99999, // stale — no matching AUTO item
		},
		LineItems: []BillLineItem{
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 120000},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 30000},
		},
	}
	b.PruneStaleOverrides()

	if len(b.Overrides) != 1 {
		t.Fatalf("expected 1 override remaining, got %d", len(b.Overrides))
	}
	if _, ok := b.Overrides["ELECTRICITY"]; !ok {
		t.Error("expected ELECTRICITY override to survive")
	}
	if _, ok := b.Overrides["PRORATE_RENT"]; ok {
		t.Error("expected PRORATE_RENT override to be pruned (no matching AUTO item)")
	}
}

func TestValidateOverrides_RejectsNonOverrideable(t *testing.T) {
	b := &Bill{
		Overrides: OverrideMap{
			"OUTSTANDING_BILL": 100000,
		},
	}
	if err := b.ValidateOverrides(); err == nil {
		t.Fatal("expected error for non-overrideable type")
	}
}

func TestValidateOverrides_RejectsNegativeAmount(t *testing.T) {
	b := &Bill{
		Overrides: OverrideMap{
			"ELECTRICITY": -50000,
		},
	}
	if err := b.ValidateOverrides(); err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestValidateOverrides_AcceptsValid(t *testing.T) {
	b := &Bill{
		Overrides: OverrideMap{
			"ELECTRICITY": 80000,
			"WATER":       15000,
		},
	}
	if err := b.ValidateOverrides(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOverrideKey(t *testing.T) {
	li := BillLineItem{LineType: LineItemElectricity}
	if li.OverrideKey() != "ELECTRICITY" {
		t.Fatalf("expected ELECTRICITY, got %s", li.OverrideKey())
	}
}

// --- Checklist edge cases ---

// #1: deposit > charges → refund, net direction correct, DepositBalance positive
func TestChecklist_DepositExceedsCharges_FullRefund(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 500000, // 5000 baht
		LineItems: []BillLineItem{
			{LineType: LineItemProrateRent, Source: LineItemSourceAuto, Amount: 150000},
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 30000},
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 20000},
		},
	}
	b.CalculateTotal()

	// total = 200000 (2000 baht)
	if b.TotalAmount != 200000 {
		t.Fatalf("TotalAmount = %d, want 200000", b.TotalAmount)
	}

	bd := b.DepositBreakdown()
	// applied = min(500000, 200000) = 200000
	if bd.AppliedAmount != 200000 {
		t.Errorf("Applied = %d, want 200000", bd.AppliedAmount)
	}
	// refund = 500000 - 200000 = 300000 (3000 baht)
	if bd.RefundAmount != 300000 {
		t.Errorf("Refund = %d, want 300000", bd.RefundAmount)
	}
	if bd.AmountDue != 0 {
		t.Errorf("Due = %d, want 0", bd.AmountDue)
	}
	// DepositBalance: positive = refund
	if b.DepositBalance != 300000 {
		t.Errorf("DepositBalance = %d, want 300000 (positive = refund)", b.DepositBalance)
	}
}

// #3: NONE mode end-to-end with CalculateTotal
func TestChecklist_NoneMode_EndToEnd(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 500000, // 5000 baht
		DepositApp:    DepositAppNone,
		LineItems: []BillLineItem{
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 200000},
		},
	}
	b.CalculateTotal()

	bd := b.DepositBreakdown()
	if bd.AppliedAmount != 0 {
		t.Errorf("Applied = %d, want 0", bd.AppliedAmount)
	}
	if bd.RefundAmount != 0 {
		t.Errorf("Refund = %d, want 0", bd.RefundAmount)
	}
	if bd.WithheldAmount != 500000 {
		t.Errorf("Withheld = %d, want 500000", bd.WithheldAmount)
	}
	if bd.AmountDue != 200000 {
		t.Errorf("Due = %d, want 200000", bd.AmountDue)
	}
	// DepositBalance: -200000 (tenant owes full charges)
	if b.DepositBalance != -200000 {
		t.Errorf("DepositBalance = %d, want -200000", b.DepositBalance)
	}
}

// #5: Manual + Override coexist — override replaces AUTO amount, MANUAL adds separately
func TestChecklist_ManualAndOverrideCoexist(t *testing.T) {
	b := &Bill{
		BillType:      BillTypeSettlement,
		DepositAmount: 0,
		Overrides: OverrideMap{
			"ELECTRICITY": 80000, // override AUTO from 100000 to 80000
		},
		LineItems: []BillLineItem{
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 100000},
			{LineType: LineItemPenalty, Source: LineItemSourceManual, Amount: 20000},
		},
	}
	b.CalculateTotal()

	// total = 80000 (override) + 20000 (manual) = 100000
	if b.TotalAmount != 100000 {
		t.Fatalf("TotalAmount = %d, want 100000 (override 80000 + manual 20000)", b.TotalAmount)
	}
}

// #2: Override survives regeneration (PruneStaleOverrides keeps matching keys)
func TestChecklist_OverrideSurvivesRegenerate(t *testing.T) {
	// Simulate: old bill had override for ELECTRICITY, new bill still has ELECTRICITY AUTO item
	b := &Bill{
		BillType: BillTypeSettlement,
		Overrides: OverrideMap{
			"ELECTRICITY": 80000,
			"WATER":       15000,
		},
		LineItems: []BillLineItem{
			// After regen: ELECTRICITY still exists (maybe different base amount), WATER still exists
			{LineType: LineItemElectricity, Source: LineItemSourceAuto, Amount: 110000}, // new base
			{LineType: LineItemWater, Source: LineItemSourceAuto, Amount: 25000},        // new base
		},
	}
	b.PruneStaleOverrides()

	// Both overrides should survive
	if len(b.Overrides) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(b.Overrides))
	}

	b.CalculateTotal()

	// total = 80000 (override) + 15000 (override) = 95000
	// NOT 110000 + 25000 = 135000
	if b.TotalAmount != 95000 {
		t.Fatalf("TotalAmount = %d, want 95000 (overrides applied to new base)", b.TotalAmount)
	}
}

// new action fails closed (it stays off is_edited until explicitly opted in).
func TestBillAuditAction_IsEditEvent(t *testing.T) {
	cases := []struct {
		action BillAuditAction
		want   bool
	}{
		// Edit events
		{AuditUpdateOverride, true},
		{AuditAddManualItem, true},
		{AuditRemoveManualItem, true},
		{AuditUpdateNote, true},

		// Lifecycle events — must not count
		{AuditCreateDraft, false},
		{AuditFinalize, false},
		{AuditVoid, false},
		{AuditSupersede, false},
		{AuditCreateFromCorrection, false},

		// Unknown / future action — must default to false (fail-closed)
		{BillAuditAction("UNKNOWN_FUTURE_ACTION"), false},
		{BillAuditAction(""), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			if got := tc.action.IsEditEvent(); got != tc.want {
				t.Errorf("IsEditEvent(%q) = %v, want %v", tc.action, got, tc.want)
			}
		})
	}
}

// --- Correction (void+recreate) lifecycle ---
//
// CanCorrect is stricter than CanVoid: only FINALIZED MONTHLY bills that are
// not already superseded are eligible. Each guard fires its own typed
// sentinel so callers (service/handler) can route to the right HTTP status
// without string-matching.

func finalizedMonthly() Bill {
	return Bill{
		ID:        uuid.New(),
		Status:    BillStatusFinalized,
		BillType:  BillTypeMonthly,
		LineItems: []BillLineItem{{LineType: LineItemRoomRent, Amount: 500000}},
	}
}

func TestBill_CanCorrect(t *testing.T) {
	t.Run("FINALIZED monthly is eligible", func(t *testing.T) {
		b := finalizedMonthly()
		if err := b.CanCorrect(); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
	t.Run("DRAFT rejected → use edit flow", func(t *testing.T) {
		b := finalizedMonthly()
		b.Status = BillStatusDraft
		if err := b.CanCorrect(); !errors.Is(err, ErrNotFinalized) {
			t.Fatalf("expected ErrNotFinalized, got %v", err)
		}
	})
	t.Run("PAID rejected (blocked in v1)", func(t *testing.T) {
		b := finalizedMonthly()
		b.Status = BillStatusPaid
		if err := b.CanCorrect(); !errors.Is(err, ErrAlreadyPaid) {
			t.Fatalf("expected ErrAlreadyPaid, got %v", err)
		}
	})
	t.Run("VOID rejected (idempotent guard)", func(t *testing.T) {
		b := finalizedMonthly()
		b.Status = BillStatusVoid
		if err := b.CanCorrect(); !errors.Is(err, ErrAlreadyVoided) {
			t.Fatalf("expected ErrAlreadyVoided, got %v", err)
		}
	})
	t.Run("already superseded rejected", func(t *testing.T) {
		b := finalizedMonthly()
		next := uuid.New()
		b.SupersededByBillID = &next
		if err := b.CanCorrect(); !errors.Is(err, ErrAlreadySuperseded) {
			t.Fatalf("expected ErrAlreadySuperseded, got %v", err)
		}
	})
	t.Run("SETTLEMENT now allowed (domain is type-agnostic since Phase 2.1E-A)", func(t *testing.T) {
		// Settlement correction routing is enforced at the service-layer
		// dispatcher (CorrectBill rejects SETTLEMENT to push callers to
		// the move-out endpoint), NOT in the domain. CanCorrect models
		// the document-replacement invariant only: a FINALIZED bill,
		// regardless of type, can be replaced via void+recreate. See
		// CanCorrect doc + project_settlement_correction_design_lock.
		b := finalizedMonthly()
		b.BillType = BillTypeSettlement
		if err := b.CanCorrect(); err != nil {
			t.Fatalf("expected nil (SETTLEMENT now eligible at domain level), got %v", err)
		}
	})
}

func TestBill_MarkSupersededByCorrection(t *testing.T) {
	t.Run("happy path sets status, reason, link", func(t *testing.T) {
		b := finalizedMonthly()
		newID := uuid.New()
		if err := b.MarkSupersededByCorrection(newID); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if !b.IsVoid() {
			t.Errorf("status = %v, want VOID", b.Status)
		}
		if b.VoidReason == nil || *b.VoidReason != voidReasonCorrection {
			t.Errorf("void_reason = %v, want CORRECTION", b.VoidReason)
		}
		if b.SupersededByBillID == nil || *b.SupersededByBillID != newID {
			t.Errorf("superseded_by_bill_id = %v, want %v", b.SupersededByBillID, newID)
		}
		if !b.IsSupersededByCorrection() {
			t.Errorf("IsSupersededByCorrection = false, want true")
		}
	})
	t.Run("rejects nil new bill id", func(t *testing.T) {
		b := finalizedMonthly()
		if err := b.MarkSupersededByCorrection(uuid.Nil); err == nil {
			t.Fatalf("expected error on nil new bill id")
		}
	})
	t.Run("rejects self-reference", func(t *testing.T) {
		b := finalizedMonthly()
		if err := b.MarkSupersededByCorrection(b.ID); err == nil {
			t.Fatalf("expected error on self-reference")
		}
	})
	t.Run("rejects when CanCorrect fails (PAID)", func(t *testing.T) {
		b := finalizedMonthly()
		b.Status = BillStatusPaid
		if err := b.MarkSupersededByCorrection(uuid.New()); !errors.Is(err, ErrAlreadyPaid) {
			t.Fatalf("expected ErrAlreadyPaid, got %v", err)
		}
	})
}

// ============================================================
// IsDeliverable (Group 4 domain)
// ============================================================

func TestBill_IsDeliverable(t *testing.T) {
	// IsDeliverable checks type+status only — payment destination is not a gate.
	t.Run("finalized monthly with null destination is deliverable", func(t *testing.T) {
		b := finalizedMonthly()
		b.PaymentBankName = nil
		if !b.IsDeliverable() {
			t.Error("IsDeliverable should return true — payment destination is not a gate")
		}
	})
	t.Run("finalized monthly with destination is deliverable", func(t *testing.T) {
		b := finalizedMonthly()
		bn, an, acn := "SCB", "123456789", "นานา รีซอร์ท"
		b.PaymentBankName = &bn
		b.PaymentAccountNumber = &an
		b.PaymentAccountName = &acn
		if !b.IsDeliverable() {
			t.Error("expected IsDeliverable to return true")
		}
	})
	t.Run("settlement bill never deliverable", func(t *testing.T) {
		b := Bill{Status: BillStatusFinalized, BillType: BillTypeSettlement}
		if b.IsDeliverable() {
			t.Error("settlement bill must not pass IsDeliverable")
		}
	})
	t.Run("draft monthly never deliverable", func(t *testing.T) {
		b := Bill{Status: BillStatusDraft, BillType: BillTypeMonthly}
		if b.IsDeliverable() {
			t.Error("draft bill must not pass IsDeliverable")
		}
	})
	t.Run("paid monthly never deliverable", func(t *testing.T) {
		b := Bill{Status: BillStatusPaid, BillType: BillTypeMonthly}
		if b.IsDeliverable() {
			t.Error("paid bill must not pass IsDeliverable")
		}
	})
	t.Run("void monthly never deliverable", func(t *testing.T) {
		b := Bill{Status: BillStatusVoid, BillType: BillTypeMonthly}
		if b.IsDeliverable() {
			t.Error("void bill must not pass IsDeliverable")
		}
	})
}

// --- ValidateAdjustment — Reading Recovery doctrine (Phase 4 D-class) ---
//
// DOCTRINE: feedback_reading_recovery_doctrine.md (locked 2026-06-22).
// DESIGN:   /Users/anantakit/.claude/plans/hidden-waddling-marble.md.
//
// Five tests, locking:
//   - NonAdjustment → nil (no-op; covers every non-ADJUSTMENT row).
//   - SourceMustBeManual → ErrAdjustmentSourceMustBeManual (Source != MANUAL).
//   - RecoveryReadingRequired → ErrAdjustmentRecoveryReadingRequired (FK nil).
//   - ReasonCodeEdgeCases → required / invalid / valid (table-driven).
//   - NoteEdgeCases → required / whitespace / too-short / exactly-10 / valid
//     (table-driven, covers both ErrAdjustmentNoteRequired and
//     ErrAdjustmentNoteTooShort).

func TestBillLineItem_ValidateAdjustment_NonAdjustment_ReturnsNil(t *testing.T) {
	li := &BillLineItem{
		LineType: LineItemElectricity,
		Source:   LineItemSourceAuto,
		// Adjustment fields all nil — must not trip.
	}
	if err := li.ValidateAdjustment(); err != nil {
		t.Errorf("ValidateAdjustment() on ELECTRICITY = %v, want nil", err)
	}

	// ROOM_RENT with stray adjustment fields populated is still nil
	// — non-ADJUSTMENT short-circuit fires before any other check. The
	// DB CHECK (bill_line_items_adjustment_fk_only_for_type etc.) is the
	// layer that rejects cross-contaminated rows.
	badID := uuid.New()
	reason := AdjustmentReasonMeterRecovery
	note := "stray adjustment fields"
	li2 := &BillLineItem{
		LineType:                    LineItemRoomRent,
		Source:                      LineItemSourceAuto,
		AdjustmentRecoveryReadingID: &badID,
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &note,
	}
	if err := li2.ValidateAdjustment(); err != nil {
		t.Errorf("ValidateAdjustment() on non-ADJUSTMENT with stray fields = %v, want nil (DB layer guards cross-contamination)", err)
	}
}

func TestBillLineItem_ValidateAdjustment_SourceMustBeManual(t *testing.T) {
	recID := uuid.New()
	reason := AdjustmentReasonMeterRecovery
	note := "valid 10-char note"
	li := &BillLineItem{
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceAuto, // wrong
		AdjustmentRecoveryReadingID: &recID,
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &note,
	}
	if err := li.ValidateAdjustment(); err != ErrAdjustmentSourceMustBeManual {
		t.Errorf("ValidateAdjustment() = %v, want ErrAdjustmentSourceMustBeManual", err)
	}
}

func TestBillLineItem_ValidateAdjustment_RecoveryReadingRequired(t *testing.T) {
	reason := AdjustmentReasonMeterRecovery
	note := "valid 10-char note"
	li := &BillLineItem{
		LineType:                    LineItemAdjustment,
		Source:                      LineItemSourceManual,
		AdjustmentRecoveryReadingID: nil, // missing
		AdjustmentReasonCode:        &reason,
		AdjustmentNote:              &note,
	}
	if err := li.ValidateAdjustment(); err != ErrAdjustmentRecoveryReadingRequired {
		t.Errorf("ValidateAdjustment() = %v, want ErrAdjustmentRecoveryReadingRequired", err)
	}
}

func TestBillLineItem_ValidateAdjustment_ReasonCodeEdgeCases(t *testing.T) {
	recID := uuid.New()
	note := "valid 10-char note"
	bogus := AdjustmentReasonCode("BOGUS_CODE")
	valid := AdjustmentReasonMeterRecovery

	cases := []struct {
		name    string
		reason  *AdjustmentReasonCode
		wantErr error
	}{
		{"nil reason", nil, ErrAdjustmentReasonCodeRequired},
		{"bogus code", &bogus, ErrAdjustmentReasonCodeInvalid},
		{"valid METER_RECOVERY", &valid, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			li := &BillLineItem{
				LineType:                    LineItemAdjustment,
				Source:                      LineItemSourceManual,
				Amount:                      -100, // negative so METER_RECOVERY passes the refund-only invariant
				AdjustmentRecoveryReadingID: &recID,
				AdjustmentUtility:           utilPtr(AdjustmentUtilityElectricity),
				AdjustmentReasonCode:        tc.reason,
				AdjustmentNote:              &note,
			}
			if got := li.ValidateAdjustment(); got != tc.wantErr {
				t.Errorf("ValidateAdjustment() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

func TestBillLineItem_ValidateAdjustment_NoteEdgeCases(t *testing.T) {
	recID := uuid.New()
	refund := AdjustmentReasonMeterRecovery
	waived := AdjustmentReasonMeterRecoveryWaived
	strPtr := func(s string) *string { return &s }

	// Q1.5 note rule: a refund (ACCEPT) is deterministic + self-explaining, so
	// its note is OPTIONAL; a waive (declining a known over-charge) REQUIRES a
	// reason. When present on either, a note must be ≥10 chars.
	cases := []struct {
		name    string
		reason  AdjustmentReasonCode
		amount  int64
		note    *string
		wantErr error
	}{
		{"refund nil note → ok", refund, -100, nil, nil},
		{"refund empty note → ok", refund, -100, strPtr(""), nil},
		{"refund whitespace-only → ok", refund, -100, strPtr("\n\t  "), nil},
		{"refund 9 chars → too short", refund, -100, strPtr("123456789"), ErrAdjustmentNoteTooShort},
		{"refund exactly 10 → ok", refund, -100, strPtr("1234567890"), nil},
		{"waive nil note → required", waived, 0, nil, ErrAdjustmentNoteRequired},
		{"waive empty note → required", waived, 0, strPtr(""), ErrAdjustmentNoteRequired},
		{"waive whitespace-only → required", waived, 0, strPtr("   "), ErrAdjustmentNoteRequired},
		{"waive 9 chars → too short", waived, 0, strPtr("123456789"), ErrAdjustmentNoteTooShort},
		{"waive exactly 10 → ok", waived, 0, strPtr("1234567890"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := tc.reason
			li := &BillLineItem{
				LineType:                    LineItemAdjustment,
				Source:                      LineItemSourceManual,
				Amount:                      tc.amount,
				AdjustmentRecoveryReadingID: &recID,
				AdjustmentUtility:           utilPtr(AdjustmentUtilityElectricity),
				AdjustmentReasonCode:        &reason,
				AdjustmentNote:              tc.note,
			}
			if got := li.ValidateAdjustment(); got != tc.wantErr {
				t.Errorf("ValidateAdjustment() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// Q1.5 Over-Record — ADJUSTMENT requires a valid utility (electricity/water
// resolve independently). Missing → ErrAdjustmentUtilityRequired; bogus →
// ErrAdjustmentUtilityInvalid. The utility check runs after the amount check.
func TestBillLineItem_ValidateAdjustment_UtilityRequired(t *testing.T) {
	recID := uuid.New()
	reason := AdjustmentReasonMeterRecovery
	note := "คืนยอดที่เก็บเกิน 10+ chars"
	bogus := AdjustmentUtility("GAS")
	base := func(u *AdjustmentUtility) *BillLineItem {
		return &BillLineItem{
			LineType: LineItemAdjustment, Source: LineItemSourceManual, Amount: -100,
			AdjustmentRecoveryReadingID: &recID, AdjustmentUtility: u,
			AdjustmentReasonCode: &reason, AdjustmentNote: &note,
		}
	}
	if got := base(nil).ValidateAdjustment(); got != ErrAdjustmentUtilityRequired {
		t.Errorf("nil utility = %v, want ErrAdjustmentUtilityRequired", got)
	}
	if got := base(&bogus).ValidateAdjustment(); got != ErrAdjustmentUtilityInvalid {
		t.Errorf("bogus utility = %v, want ErrAdjustmentUtilityInvalid", got)
	}
	if got := base(utilPtr(AdjustmentUtilityWater)).ValidateAdjustment(); got != nil {
		t.Errorf("valid utility = %v, want nil", got)
	}
}

// Q1.5 Over-Record — amount invariant by reason: METER_RECOVERY is refund-only
// (amount < 0); waive (METER_RECOVERY_WAIVED) must be zero. Zero or positive
// (charge) under METER_RECOVERY is rejected. The amount check lives inside the
// reason switch, before the note check.
func TestBillLineItem_ValidateAdjustment_AmountByReason(t *testing.T) {
	recID := uuid.New()
	note := "valid 10-char note"
	recovery := AdjustmentReasonMeterRecovery
	waived := AdjustmentReasonMeterRecoveryWaived

	cases := []struct {
		name    string
		reason  AdjustmentReasonCode
		amount  int64
		wantErr error
	}{
		{"refund negative ok", recovery, -15000, nil},
		{"charge positive rejected (refund-only)", recovery, 15000, ErrAdjustmentRefundMustBeNegative},
		{"recovery zero rejected", recovery, 0, ErrAdjustmentRefundMustBeNegative},
		{"waived zero ok", waived, 0, nil},
		{"waived positive rejected", waived, 100, ErrAdjustmentWaivedMustBeZero},
		{"waived negative rejected", waived, -1, ErrAdjustmentWaivedMustBeZero},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := tc.reason
			li := &BillLineItem{
				LineType:                    LineItemAdjustment,
				Source:                      LineItemSourceManual,
				Amount:                      tc.amount,
				AdjustmentRecoveryReadingID: &recID,
				AdjustmentUtility:           utilPtr(AdjustmentUtilityElectricity),
				AdjustmentReasonCode:        &reason,
				AdjustmentNote:              &note,
			}
			if got := li.ValidateAdjustment(); got != tc.wantErr {
				t.Errorf("ValidateAdjustment() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}
