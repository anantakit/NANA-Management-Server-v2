package moveout

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"nana/internal/domain"
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
	// Phase-2 broadens further: COMPLETED + nil outcome accepted (post-close
	// back-fill). COMPLETED + outcome rejected — settled-and-closed is
	// terminal for payment edits.
	outcome := PaymentOutcomePaidExtra
	for _, s := range allStatuses() {
		t.Run(string(s)+"_with_nil_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanRecordPayment()
			wantOK := s == MoveOutStatusPendingPayment ||
				s == MoveOutStatusReadyToClose ||
				s == MoveOutStatusCompleted
			if wantOK && err != nil {
				t.Errorf("CanRecordPayment(%s, nil) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanRecordPayment(%s, nil) expected error, got nil", s)
			}
		})
		t.Run(string(s)+"_with_set_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s, PaymentOutcome: &outcome}
			err := m.CanRecordPayment()
			// Set outcome: PENDING_PAYMENT and READY_TO_CLOSE accept (correction);
			// COMPLETED rejects (terminal once settled).
			wantOK := s == MoveOutStatusPendingPayment || s == MoveOutStatusReadyToClose
			if wantOK && err != nil {
				t.Errorf("CanRecordPayment(%s, set) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanRecordPayment(%s, set) expected error, got nil", s)
			}
		})
	}
}

func TestMoveOutNotice_CanCloseWithUnsettled(t *testing.T) {
	billID := uuid.New()
	outcome := PaymentOutcomePaidExtra

	cases := []struct {
		name    string
		status  MoveOutStatus
		billID  *uuid.UUID
		outcome *PaymentOutcome
		wantErr error
	}{
		// Happy paths
		{"pending_payment + nil + bill", MoveOutStatusPendingPayment, &billID, nil, nil},
		{"ready_to_close + nil + bill", MoveOutStatusReadyToClose, &billID, nil, nil},
		// Idempotent — already completed; bill may or may not be present (not checked)
		{"completed + nil + bill", MoveOutStatusCompleted, &billID, nil, nil},
		{"completed + nil + no bill", MoveOutStatusCompleted, nil, nil, nil},
		// Missing bill — pre-completion only
		{"pending_payment + nil + no bill", MoveOutStatusPendingPayment, nil, nil, ErrMissingSettlementBill},
		{"ready_to_close + nil + no bill", MoveOutStatusReadyToClose, nil, nil, ErrMissingSettlementBill},
		// Settled — must use regular CloseMoveOut path
		{"ready_to_close + outcome", MoveOutStatusReadyToClose, &billID, &outcome, ErrCannotCloseWithUnsettled},
		{"completed + outcome", MoveOutStatusCompleted, &billID, &outcome, ErrCannotCloseWithUnsettled},
		// Wrong status
		{"pending_meter rejects", MoveOutStatusPendingMeter, &billID, nil, ErrCannotCloseWithUnsettled},
		{"pending_settlement rejects", MoveOutStatusPendingSettlement, &billID, nil, ErrCannotCloseWithUnsettled},
		{"cancelled rejects", MoveOutStatusCancelled, &billID, nil, ErrCannotCloseWithUnsettled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &MoveOutNotice{
				Status:           tc.status,
				SettlementBillID: tc.billID,
				PaymentOutcome:   tc.outcome,
			}
			err := m.CanCloseWithUnsettled()
			if tc.wantErr == nil && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if tc.wantErr != nil && err != tc.wantErr {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestMoveOutNotice_CloseWithUnsettled(t *testing.T) {
	now := time.Now()
	billID := uuid.New()

	t.Run("PENDING_PAYMENT → COMPLETED, outcome stays nil", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingPayment, SettlementBillID: &billID}
		if err := m.CloseWithUnsettled(now); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("status: got %s, want COMPLETED", m.Status)
		}
		if m.PaymentOutcome != nil {
			t.Fatalf("payment_outcome must stay nil, got %v", *m.PaymentOutcome)
		}
		if m.ClosedAt == nil {
			t.Fatal("closed_at must be set")
		}
	})

	t.Run("READY_TO_CLOSE + nil → COMPLETED, outcome stays nil", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusReadyToClose, SettlementBillID: &billID}
		if err := m.CloseWithUnsettled(now); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("status: got %s, want COMPLETED", m.Status)
		}
		if m.PaymentOutcome != nil {
			t.Fatal("payment_outcome must stay nil")
		}
	})

	// Idempotency: re-call against COMPLETED + nil must NOT mutate ClosedAt
	// (the original close timestamp is preserved).
	t.Run("idempotent on COMPLETED + nil — no mutation", func(t *testing.T) {
		original := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
		m := &MoveOutNotice{
			Status:   MoveOutStatusCompleted,
			ClosedAt: &original,
		}
		later := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
		if err := m.CloseWithUnsettled(later); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("status changed: got %s", m.Status)
		}
		if m.ClosedAt == nil || !m.ClosedAt.Equal(original) {
			t.Fatalf("closed_at must stay at %v on idempotent re-call, got %v", original, m.ClosedAt)
		}
	})

	t.Run("settled — rejects", func(t *testing.T) {
		outcome := PaymentOutcomePaidExtra
		m := &MoveOutNotice{
			Status:           MoveOutStatusReadyToClose,
			SettlementBillID: &billID,
			PaymentOutcome:   &outcome,
		}
		if err := m.CloseWithUnsettled(now); err != ErrCannotCloseWithUnsettled {
			t.Fatalf("expected ErrCannotCloseWithUnsettled, got %v", err)
		}
	})
}

func TestMoveOutNotice_CanSkipPayment(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanSkipPayment()
			wantOK := s == MoveOutStatusPendingPayment
			if wantOK && err != nil {
				t.Errorf("CanSkipPayment(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanSkipPayment(%s) expected error, got nil", s)
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

func TestMoveOutNotice_AttachDraft(t *testing.T) {
	billID := uuid.New()

	t.Run("PENDING_SETTLEMENT — attaches draft, stays PENDING_SETTLEMENT", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.AttachDraft(billID, 150000); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusPendingSettlement {
			t.Fatalf("expected PENDING_SETTLEMENT (no change), got %s", m.Status)
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
		if err := m.AttachDraft(billID, 0); err != ErrCannotRecordSettlement {
			t.Fatalf("expected ErrCannotRecordSettlement, got %v", err)
		}
	})
}

func TestMoveOutNotice_AdvanceToPayment(t *testing.T) {
	billID := uuid.New()

	t.Run("PENDING_SETTLEMENT with bill → PENDING_PAYMENT", func(t *testing.T) {
		m := &MoveOutNotice{
			Status:           MoveOutStatusPendingSettlement,
			SettlementBillID: &billID,
		}
		if err := m.AdvanceToPayment(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusPendingPayment {
			t.Fatalf("expected PENDING_PAYMENT, got %s", m.Status)
		}
	})

	t.Run("PENDING_SETTLEMENT without bill — rejects", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.AdvanceToPayment(); err != ErrMissingSettlementBill {
			t.Fatalf("expected ErrMissingSettlementBill, got %v", err)
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		m := &MoveOutNotice{
			Status:           MoveOutStatusPendingMeter,
			SettlementBillID: &billID,
		}
		if err := m.AdvanceToPayment(); err != ErrCannotAdvanceToPayment {
			t.Fatalf("expected ErrCannotAdvanceToPayment, got %v", err)
		}
	})
}

func TestMoveOutNotice_RecordPayment(t *testing.T) {
	cash := domain.PaymentMethodCash
	transfer := domain.PaymentMethodTransfer

	t.Run("PENDING_PAYMENT → READY_TO_CLOSE with method", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingPayment}
		if err := m.RecordPayment(PaymentOutcomeRefunded, &transfer, "คืนเงิน 500 บาท"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("expected READY_TO_CLOSE, got %s", m.Status)
		}
		if m.PaymentOutcome == nil || *m.PaymentOutcome != PaymentOutcomeRefunded {
			t.Fatal("payment_outcome not set")
		}
		if m.PaymentMethod == nil || *m.PaymentMethod != domain.PaymentMethodTransfer {
			t.Fatalf("payment_method = %v, want TRANSFER", m.PaymentMethod)
		}
		if m.PaymentNote != "คืนเงิน 500 บาท" {
			t.Fatalf("payment_note = %q, want 'คืนเงิน 500 บาท'", m.PaymentNote)
		}
	})

	t.Run("ZERO_BALANCE accepts nil method", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingPayment}
		if err := m.RecordPayment(PaymentOutcomeZeroBalance, nil, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.PaymentMethod != nil {
			t.Fatalf("expected nil method for ZERO, got %v", *m.PaymentMethod)
		}
	})

	// Correction path: a previously-recorded notice (READY_TO_CLOSE + outcome
	// set) accepts a re-record. Outcome/method/note are REPLACED — no merge.
	t.Run("correction from READY_TO_CLOSE replaces outcome and stays at READY_TO_CLOSE", func(t *testing.T) {
		old := PaymentOutcomePaidExtra
		m := &MoveOutNotice{
			Status:         MoveOutStatusReadyToClose,
			PaymentOutcome: &old,
			PaymentMethod:  &cash,
			PaymentNote:    "ของเดิม",
		}
		if err := m.RecordPayment(PaymentOutcomeRefunded, &transfer, "ของใหม่"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("status changed unexpectedly: got %s, want READY_TO_CLOSE", m.Status)
		}
		if m.PaymentOutcome == nil || *m.PaymentOutcome != PaymentOutcomeRefunded {
			t.Fatalf("outcome not replaced: got %v, want REFUNDED", m.PaymentOutcome)
		}
		if m.PaymentMethod == nil || *m.PaymentMethod != domain.PaymentMethodTransfer {
			t.Fatalf("method not replaced: got %v, want TRANSFER", m.PaymentMethod)
		}
		if m.PaymentNote != "ของใหม่" {
			t.Fatalf("note not replaced: got %q, want 'ของใหม่'", m.PaymentNote)
		}
	})

	// Back-fill path: a skipped notice (READY_TO_CLOSE + nil outcome) accepts
	// a first record without flipping status backwards.
	t.Run("backfill from skipped READY_TO_CLOSE sets fields, status stays", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusReadyToClose}
		if err := m.RecordPayment(PaymentOutcomePaidExtra, &cash, ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("status changed unexpectedly: got %s, want READY_TO_CLOSE", m.Status)
		}
		if m.PaymentOutcome == nil || *m.PaymentOutcome != PaymentOutcomePaidExtra {
			t.Fatal("outcome not set")
		}
	})

	// Phase-2 back-fill: a closed-with-unsettled notice (COMPLETED + nil
	// outcome) accepts a record without reopening the contract — status
	// stays COMPLETED.
	t.Run("backfill from COMPLETED + nil sets fields, status stays COMPLETED", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCompleted}
		if err := m.RecordPayment(PaymentOutcomeRefunded, &cash, "บันทึกหลังปิด"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("status changed unexpectedly: got %s, want COMPLETED", m.Status)
		}
		if m.PaymentOutcome == nil || *m.PaymentOutcome != PaymentOutcomeRefunded {
			t.Fatal("outcome not set on COMPLETED back-fill")
		}
		if m.PaymentMethod == nil || *m.PaymentMethod != domain.PaymentMethodCash {
			t.Fatalf("method not set on COMPLETED back-fill: got %v", m.PaymentMethod)
		}
		if m.PaymentNote != "บันทึกหลังปิด" {
			t.Fatalf("note not set: got %q", m.PaymentNote)
		}
	})

	// Pin the invariant: removing the `if m.IsPendingPayment() { ... }` guard
	// in RecordPayment would silently demote READY_TO_CLOSE back to itself
	// (no symptom) but break correction semantics if a later refactor flipped
	// the conditional. This test exists to anchor the rule for future readers.
	t.Run("does not change status when called on READY_TO_CLOSE", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusReadyToClose}
		_ = m.RecordPayment(PaymentOutcomeZeroBalance, nil, "")
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("status changed: got %s, want READY_TO_CLOSE", m.Status)
		}
	})

	t.Run("wrong status rejects", func(t *testing.T) {
		// PENDING_METER stands in for any non-(PENDING_PAYMENT|READY_TO_CLOSE)
		// status; the parameterized CanRecordPayment loop covers the rest.
		m := &MoveOutNotice{Status: MoveOutStatusPendingMeter}
		if err := m.RecordPayment(PaymentOutcomePaidExtra, &cash, ""); err != ErrCannotRecordPayment {
			t.Fatalf("expected ErrCannotRecordPayment, got %v", err)
		}
	})
}

func TestMoveOutNotice_SkipPayment(t *testing.T) {
	t.Run("PENDING_PAYMENT → READY_TO_CLOSE, outcome stays nil", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingPayment}
		if err := m.SkipPayment(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusReadyToClose {
			t.Fatalf("expected READY_TO_CLOSE, got %s", m.Status)
		}
		if m.PaymentOutcome != nil {
			t.Fatalf("expected nil PaymentOutcome, got %v", *m.PaymentOutcome)
		}
	})

	// Domain layer is strict — service layer adds the idempotency short-circuit.
	wrongStatuses := []MoveOutStatus{
		MoveOutStatusPendingMeter,
		MoveOutStatusPendingSettlement,
		MoveOutStatusReadyToClose,
		MoveOutStatusCompleted,
		MoveOutStatusCancelled,
	}
	for _, s := range wrongStatuses {
		t.Run("rejects from "+string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			if err := m.SkipPayment(); err != ErrCannotSkipPayment {
				t.Fatalf("expected ErrCannotSkipPayment, got %v", err)
			}
		})
	}
}

func TestMoveOutNotice_IsUnsettled(t *testing.T) {
	outcome := PaymentOutcomePaidExtra
	for _, s := range allStatuses() {
		t.Run(string(s)+"_with_nil_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			// Phase-2: COMPLETED + nil also counts as unsettled (closed-with-
			// unsettled re-entry path).
			want := s == MoveOutStatusReadyToClose || s == MoveOutStatusCompleted
			if got := m.IsUnsettled(); got != want {
				t.Errorf("IsUnsettled(%s, nil) = %v, want %v", s, got, want)
			}
		})
		t.Run(string(s)+"_with_set_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s, PaymentOutcome: &outcome}
			if m.IsUnsettled() {
				t.Errorf("IsUnsettled(%s, set) = true, want false (set outcome → not unsettled)", s)
			}
		})
	}
}

func TestMoveOutNotice_IsSettled(t *testing.T) {
	outcome := PaymentOutcomePaidExtra
	for _, s := range allStatuses() {
		t.Run(string(s)+"_with_nil_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			if m.IsSettled() {
				t.Errorf("IsSettled(%s, nil) = true, want false (nil outcome → not settled)", s)
			}
		})
		t.Run(string(s)+"_with_set_outcome", func(t *testing.T) {
			m := &MoveOutNotice{Status: s, PaymentOutcome: &outcome}
			// Phase-2: COMPLETED + outcome also counts as settled (history).
			want := s == MoveOutStatusReadyToClose || s == MoveOutStatusCompleted
			if got := m.IsSettled(); got != want {
				t.Errorf("IsSettled(%s, set) = %v, want %v", s, got, want)
			}
		})
	}
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

// --- ActualMoveOutDate tests ---

func TestMoveOutNotice_CanSetActualDate(t *testing.T) {
	for _, s := range allStatuses() {
		t.Run(string(s), func(t *testing.T) {
			m := &MoveOutNotice{Status: s}
			err := m.CanSetActualDate()
			wantOK := !m.IsTerminal()
			if wantOK && err != nil {
				t.Errorf("CanSetActualDate(%s) unexpected error: %v", s, err)
			}
			if !wantOK && err == nil {
				t.Errorf("CanSetActualDate(%s) expected error, got nil", s)
			}
			if !wantOK && err != nil && err != ErrCannotSetActualDate {
				t.Errorf("CanSetActualDate(%s) = %v, want ErrCannotSetActualDate", s, err)
			}
		})
	}
}

func TestMoveOutNotice_SetActualDate(t *testing.T) {
	d := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	t.Run("sets date in non-terminal state", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPendingSettlement}
		if err := m.SetActualDate(d); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.ActualMoveOutDate == nil || !m.ActualMoveOutDate.Equal(d) {
			t.Fatalf("actual_move_out_date = %v, want %v", m.ActualMoveOutDate, d)
		}
	})

	t.Run("rejects in terminal state", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCompleted}
		if err := m.SetActualDate(d); err != ErrCannotSetActualDate {
			t.Fatalf("expected ErrCannotSetActualDate, got %v", err)
		}
	})
}

func TestMoveOutNotice_RequireActualDate(t *testing.T) {
	d := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	t.Run("returns date when set", func(t *testing.T) {
		m := &MoveOutNotice{ActualMoveOutDate: &d}
		got, err := m.RequireActualDate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(d) {
			t.Fatalf("got %v, want %v", got, d)
		}
	})

	t.Run("error when nil", func(t *testing.T) {
		m := &MoveOutNotice{}
		_, err := m.RequireActualDate()
		if err != ErrActualMoveOutDateRequired {
			t.Fatalf("expected ErrActualMoveOutDateRequired, got %v", err)
		}
	})
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
