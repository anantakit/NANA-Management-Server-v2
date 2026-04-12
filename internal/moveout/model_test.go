package moveout

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMoveOutNotice_ValidateDates(t *testing.T) {
	tests := []struct {
		name      string
		notice    time.Time
		moveOut   time.Time
		wantErr   bool
		errTarget error
	}{
		{
			name:    "move out after notice — valid",
			notice:  time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
			moveOut: time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "move out same day as notice — valid",
			notice:  time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
			moveOut: time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "move out before notice — invalid",
			notice:    time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
			moveOut:   time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC),
			wantErr:   true,
			errTarget: ErrDateOrderInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MoveOutNotice{
				NoticeDate:           tt.notice,
				ScheduledMoveOutDate: tt.moveOut,
			}
			err := m.ValidateDates()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errTarget != nil && err != tt.errTarget {
					t.Fatalf("expected %v, got %v", tt.errTarget, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// allStatuses returns every MoveOutStatus for table-driven guard tests.
func allStatuses() []MoveOutStatus {
	return []MoveOutStatus{
		MoveOutStatusPendingMeter,
		MoveOutStatusPendingSettlement,
		MoveOutStatusPendingPayment,
		MoveOutStatusReadyToClose,
		MoveOutStatusCompleted,
		MoveOutStatusCancelled,
	}
}

func TestMoveOutNotice_StatusChecks(t *testing.T) {
	checks := []struct {
		status MoveOutStatus
		method string
		fn     func(*MoveOutNotice) bool
	}{
		{MoveOutStatusPendingMeter, "IsPendingMeter", (*MoveOutNotice).IsPendingMeter},
		{MoveOutStatusPendingSettlement, "IsPendingSettlement", (*MoveOutNotice).IsPendingSettlement},
		{MoveOutStatusPendingPayment, "IsPendingPayment", (*MoveOutNotice).IsPendingPayment},
		{MoveOutStatusReadyToClose, "IsReadyToClose", (*MoveOutNotice).IsReadyToClose},
		{MoveOutStatusCompleted, "IsCompleted", (*MoveOutNotice).IsCompleted},
		{MoveOutStatusCancelled, "IsCancelled", (*MoveOutNotice).IsCancelled},
	}

	for _, c := range checks {
		for _, s := range allStatuses() {
			name := c.method + "_when_" + string(s)
			t.Run(name, func(t *testing.T) {
				m := &MoveOutNotice{Status: s}
				got := c.fn(m)
				want := s == c.status
				if got != want {
					t.Errorf("%s on %s = %v, want %v", c.method, s, got, want)
				}
			})
		}
	}
}

func TestMoveOutNotice_IsTerminal(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			want := s == MoveOutStatusCompleted || s == MoveOutStatusCancelled
			if got := m.IsTerminal(); got != want {
				t.Errorf("IsTerminal(%s) = %v, want %v", s, got, want)
			}
		})
	}
}

func TestMoveOutNotice_CanCancel(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanCancel()
			wantOK := s == MoveOutStatusPendingMeter || s == MoveOutStatusPendingSettlement
			if wantOK && err != nil {
				t.Errorf("CanCancel(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanCancel(%s) expected error, got nil", s)
			}
			if !wantOK && err != nil && err != ErrCannotCancel {
				t.Errorf("CanCancel(%s) = %v, want ErrCannotCancel", s, err)
			}
		})
	}
}

func TestMoveOutNotice_CanAdvanceToSettlement(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanAdvanceToSettlement()
			wantOK := s == MoveOutStatusPendingMeter
			if wantOK && err != nil {
				t.Errorf("CanAdvanceToSettlement(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanAdvanceToSettlement(%s) expected error, got nil", s)
			}
		})
	}
}

func TestMoveOutNotice_CanRecordSettlement(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanRecordSettlement()
			wantOK := s == MoveOutStatusPendingSettlement
			if wantOK && err != nil {
				t.Errorf("CanRecordSettlement(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanRecordSettlement(%s) expected error, got nil", s)
			}
		})
	}
}

func TestMoveOutNotice_CanRecordPayment(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanRecordPayment()
			wantOK := s == MoveOutStatusPendingPayment
			if wantOK && err != nil {
				t.Errorf("CanRecordPayment(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanRecordPayment(%s) expected error, got nil", s)
			}
		})
	}
}

func TestMoveOutNotice_CanClose(t *testing.T) {
	billID := uuid.New()
	outcome := PaymentOutcomePaidExtra

	cases := []struct {
		name    string
		status  MoveOutStatus
		billID  *uuid.UUID
		outcome *PaymentOutcome
		wantErr error
	}{
		// Happy path
		{"ready with bill+outcome", MoveOutStatusReadyToClose, &billID, &outcome, nil},
		// Missing prerequisites
		{"ready without bill", MoveOutStatusReadyToClose, nil, &outcome, ErrMissingSettlementBill},
		{"ready without outcome", MoveOutStatusReadyToClose, &billID, nil, ErrMissingPaymentOutcome},
		{"ready without both", MoveOutStatusReadyToClose, nil, nil, ErrMissingSettlementBill},
	}

	// Wrong status × all non-READY_TO_CLOSE statuses
	for _, s := range allStatuses() {
		if s == MoveOutStatusReadyToClose {
			continue
		}
		cases = append(cases, struct {
			name    string
			status  MoveOutStatus
			billID  *uuid.UUID
			outcome *PaymentOutcome
			wantErr error
		}{
			name:    "wrong status " + string(s),
			status:  s,
			billID:  &billID,
			outcome: &outcome,
			wantErr: ErrCannotClose,
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &MoveOutNotice{
				Status:           tc.status,
				SettlementBillID: tc.billID,
				PaymentOutcome:   tc.outcome,
			}
			err := m.CanClose()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
			} else {
				if err != tc.wantErr {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

// --- Transition tests ---

func TestMoveOutNotice_AdvanceToSettlement(t *testing.T) {
	t.Run("PENDING_METER → PENDING_SETTLEMENT", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingMeter}
		if err := m.AdvanceToSettlement(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusPendingSettlement {
			t.Fatalf("expected PENDING_SETTLEMENT, got %s", m.Status)
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.AdvanceToSettlement(); err != ErrNotPendingMeter {
			t.Fatalf("expected ErrNotPendingMeter, got %v", err)
		}
	})
}

func TestMoveOutNotice_RecordSettlement(t *testing.T) {
	billID := uuid.New()

	t.Run("PENDING_SETTLEMENT → PENDING_PAYMENT", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.RecordSettlement(billID, 150000); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusPendingPayment {
			t.Fatalf("expected PENDING_PAYMENT, got %s", m.Status)
		}
		if m.SettlementBillID == nil || *m.SettlementBillID != billID {
			t.Fatal("settlement_bill_id not set")
		}
		if m.NetAmount == nil || *m.NetAmount != 150000 {
			t.Fatalf("net_amount = %v, want 150000", m.NetAmount)
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingMeter}
		if err := m.RecordSettlement(billID, 0); err != ErrCannotRecordSettlement {
			t.Fatalf("expected ErrCannotRecordSettlement, got %v", err)
		}
	})
}

func TestMoveOutNotice_RecordPayment(t *testing.T) {
	t.Run("PENDING_PAYMENT → READY_TO_CLOSE", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingPayment}
		if err := m.RecordPayment(PaymentOutcomeRefunded, "คืนเงิน 500 บาท"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("expected READY_TO_CLOSE, got %s", m.Status)
		}
		if m.PaymentOutcome == nil || *m.PaymentOutcome != PaymentOutcomeRefunded {
			t.Fatal("payment_outcome not set")
		}
		if m.PaymentNote != "คืนเงิน 500 บาท" {
			t.Fatalf("payment_note = %q, want 'คืนเงิน 500 บาท'", m.PaymentNote)
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusReadyToClose}
		if err := m.RecordPayment(PaymentOutcomePaidExtra, ""); err != ErrCannotRecordPayment {
			t.Fatalf("expected ErrCannotRecordPayment, got %v", err)
		}
	})
}

func TestMoveOutNotice_Close(t *testing.T) {
	now := time.Now()
	billID := uuid.New()
	outcome := PaymentOutcomeZeroBalance

	t.Run("READY_TO_CLOSE → COMPLETED", func(t *testing.T) {
		m := &MoveOutNotice{
			Status:           MoveOutStatusReadyToClose,
			SettlementBillID: &billID,
			PaymentOutcome:   &outcome,
		}
		if err := m.Close(now); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("expected COMPLETED, got %s", m.Status)
		}
		if m.ClosedAt == nil {
			t.Fatal("closed_at not set")
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		m := &MoveOutNotice{
			Status:           MoveOutStatusPendingPayment,
			SettlementBillID: &billID,
			PaymentOutcome:   &outcome,
		}
		if err := m.Close(now); err != ErrCannotClose {
			t.Fatalf("expected ErrCannotClose, got %v", err)
		}
	})
}

func TestMoveOutNotice_Cancel(t *testing.T) {
	t.Run("PENDING_METER — success", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingMeter}
		if err := m.Cancel(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCancelled {
			t.Fatalf("expected CANCELLED, got %s", m.Status)
		}
	})

	t.Run("PENDING_SETTLEMENT — success", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.Cancel(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCancelled {
			t.Fatalf("expected CANCELLED, got %s", m.Status)
		}
	})

	// All non-cancellable statuses should reject
	for _, s := range []MoveOutStatus{
		MoveOutStatusPendingPayment,
		MoveOutStatusReadyToClose,
		MoveOutStatusCompleted,
		MoveOutStatusCancelled,
	} {
		t.Run("cancel "+string(s)+" — error", func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			if err := m.Cancel(); err != ErrCannotCancel {
				t.Fatalf("expected ErrCannotCancel, got %v", err)
			}
		})
	}
}

// --- Urgency tests (unchanged) ---

func TestComputeUrgency(t *testing.T) {
	today := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		scheduled time.Time
		wantBkt   Urgency
		wantDays  int
	}{
		{"overdue 5 days", today.AddDate(0, 0, -5), UrgencyOverdue, -5},
		{"today", today, UrgencyToday, 0},
		{"soon +1", today.AddDate(0, 0, 1), UrgencySoon, 1},
		{"soon +7", today.AddDate(0, 0, 7), UrgencySoon, 7},
		{"normal +8", today.AddDate(0, 0, 8), UrgencyNormal, 8},
		{
			"non-zero time component still bucketed by date",
			time.Date(2026, 4, 6, 23, 59, 59, 0, time.UTC),
			UrgencyToday,
			0,
		},
	}

	t.Run("cross-day boundary by 2 seconds", func(t *testing.T) {
		boundaryToday := time.Date(2026, 4, 6, 23, 59, 59, 0, time.UTC)
		boundaryNext := time.Date(2026, 4, 7, 0, 0, 1, 0, time.UTC)
		if got := DaysUntil(boundaryNext, boundaryToday); got != 1 {
			t.Errorf("DaysUntil cross-boundary = %d, want 1", got)
		}
		if got := ComputeUrgency(boundaryNext, boundaryToday); got != UrgencySoon {
			t.Errorf("ComputeUrgency cross-boundary = %q, want SOON", got)
		}
	})

	t.Run("DST spring forward still 1 day", func(t *testing.T) {
		la, err := time.LoadLocation("America/Los_Angeles")
		if err != nil {
			t.Skip("tz data unavailable:", err)
		}
		dstToday := time.Date(2026, 3, 7, 12, 0, 0, 0, la)
		dstNext := time.Date(2026, 3, 8, 12, 0, 0, 0, la)
		if got := DaysUntil(dstNext, dstToday); got != 1 {
			t.Errorf("DaysUntil across DST = %d, want 1", got)
		}
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeUrgency(tc.scheduled, today); got != tc.wantBkt {
				t.Errorf("ComputeUrgency = %q, want %q", got, tc.wantBkt)
			}
			if got := DaysUntil(tc.scheduled, today); got != tc.wantDays {
				t.Errorf("DaysUntil = %d, want %d", got, tc.wantDays)
			}
		})
	}
}
