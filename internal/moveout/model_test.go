package moveout

import (
	"testing"
	"time"
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
				NoticeDate:        tt.notice,
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

func TestMoveOutNotice_StatusChecks(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPending}
		if !m.IsPending() {
			t.Error("expected IsPending true")
		}
		if m.IsCompleted() {
			t.Error("expected IsCompleted false")
		}
		if m.IsCancelled() {
			t.Error("expected IsCancelled false")
		}
	})

	t.Run("completed", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCompleted}
		if m.IsPending() {
			t.Error("expected IsPending false")
		}
		if !m.IsCompleted() {
			t.Error("expected IsCompleted true")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCancelled}
		if !m.IsCancelled() {
			t.Error("expected IsCancelled true")
		}
	})
}

func TestMoveOutNotice_Cancel(t *testing.T) {
	t.Run("cancel pending — success", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPending}
		if err := m.Cancel(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCancelled {
			t.Fatalf("expected CANCELLED, got %s", m.Status)
		}
	})

	t.Run("cancel completed — error", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCompleted}
		if err := m.Cancel(); err != ErrNotPending {
			t.Fatalf("expected ErrNotPending, got %v", err)
		}
	})

	t.Run("cancel cancelled — error", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCancelled}
		if err := m.Cancel(); err != ErrNotPending {
			t.Fatalf("expected ErrNotPending, got %v", err)
		}
	})
}

func TestMoveOutNotice_Complete(t *testing.T) {
	t.Run("complete pending — success", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusPending}
		if err := m.Complete(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Status != MoveOutStatusCompleted {
			t.Fatalf("expected COMPLETED, got %s", m.Status)
		}
	})

	t.Run("complete completed — error", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCompleted}
		if err := m.Complete(); err != ErrNotPending {
			t.Fatalf("expected ErrNotPending, got %v", err)
		}
	})

	t.Run("complete cancelled — error", func(t *testing.T) {
		m := &MoveOutNotice{Status: MoveOutStatusCancelled}
		if err := m.Complete(); err != ErrNotPending {
			t.Fatalf("expected ErrNotPending, got %v", err)
		}
	})
}

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

	// Cross-day boundary: scheduled one second into next day vs today one
	// second before midnight must yield +1 (catches naive wall-clock diffs).
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

	// DST-safe: a 25h spring-forward day in America/Los_Angeles must still
	// count as exactly 1 day, not 0.
	t.Run("DST spring forward still 1 day", func(t *testing.T) {
		la, err := time.LoadLocation("America/Los_Angeles")
		if err != nil {
			t.Skip("tz data unavailable:", err)
		}
		// 2026-03-08 is the US DST start (spring forward).
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

func TestComputeWorkflowStatus(t *testing.T) {
	cases := []struct {
		name         string
		noticeStatus MoveOutStatus
		hasMeter     bool
		want         WorkflowStatus
	}{
		{"pending without meter", MoveOutStatusPending, false, WorkflowAwaitingMeter},
		{"pending with meter", MoveOutStatusPending, true, WorkflowReadyToComplete},
		{"completed without meter", MoveOutStatusCompleted, false, WorkflowCompleted},
		{"completed with meter", MoveOutStatusCompleted, true, WorkflowCompleted},
		{"cancelled without meter", MoveOutStatusCancelled, false, WorkflowCancelled},
		{"cancelled with meter", MoveOutStatusCancelled, true, WorkflowCancelled},
		{"unknown falls back to cancelled", MoveOutStatus("WHATEVER"), false, WorkflowCancelled},
		{"empty falls back to cancelled", MoveOutStatus(""), true, WorkflowCancelled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeWorkflowStatus(tc.noticeStatus, tc.hasMeter); got != tc.want {
				t.Errorf("ComputeWorkflowStatus(%q, %v) = %q, want %q",
					tc.noticeStatus, tc.hasMeter, got, tc.want)
			}
		})
	}
}
