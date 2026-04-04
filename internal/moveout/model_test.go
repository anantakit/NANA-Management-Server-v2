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
				ActualMoveOutDate: tt.moveOut,
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
