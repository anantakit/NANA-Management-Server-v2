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
	t.Run("pending > 0 → no change", func(t *testing.T) {
		b := &BillGenerationBatch{}
		b.MarkCommitResult(1, 0, 2)
		if b.CommitStatus != nil {
			t.Fatalf("expected nil, got %v", b.CommitStatus)
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

	t.Run("already void — error", func(t *testing.T) {
		b := &Bill{Status: BillStatusVoid}
		if err := b.Void("test"); err != ErrAlreadyVoided {
			t.Fatalf("expected ErrAlreadyVoided, got %v", err)
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

// --- Calculations ---

func TestBill_CalculateTotal(t *testing.T) {
	t.Run("monthly bill", func(t *testing.T) {
		b := &Bill{
			BillType: BillTypeMonthly,
			LineItems: []BillLineItem{
				{Amount: 500000},  // 5,000 baht room rent
				{Amount: 120000},  // 1,200 baht electricity
				{Amount: 30000},   // 300 baht water
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
	// 15 days in a 30-day month, monthly rent = 6000 baht (600000 satang)
	line := NewProrateRentLine(15, 30, 600000, "ค่าห้อง 15 วัน", 1)
	if line.LineType != LineItemProrateRent {
		t.Fatalf("expected PRORATE_RENT, got %s", line.LineType)
	}
	if line.Amount != 300000 {
		t.Fatalf("expected 300000, got %d", line.Amount)
	}
	if line.Quantity != 15 {
		t.Fatalf("expected qty 15, got %d", line.Quantity)
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
