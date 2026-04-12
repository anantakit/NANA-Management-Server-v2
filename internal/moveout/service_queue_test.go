package moveout

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Queue happy path: 4 active notices across different statuses and urgency
// buckets. Verify partition into 4 sections, sort order, and summary counts.
func TestQueue_PartitionsSortsSummarizes(t *testing.T) {
	today := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	fixedNow := func() time.Time { return today }

	mk := func(name string, daysFromToday int, status MoveOutStatus) MoveOutWithRelations {
		return MoveOutWithRelations{
			MoveOutNotice: MoveOutNotice{
				ID:                   uuid.New(),
				ContractID:           uuid.New(),
				NoticeDate:           today.AddDate(0, 0, -10),
				ScheduledMoveOutDate: today.AddDate(0, 0, daysFromToday),
				Status:               status,
			},
			TenantName:    name,
			RoomNumber:    "101",
			ApartmentName: "นานาคอร์ท",
		}
	}

	overduePendingMeter := mk("overdue", -5, MoveOutStatusPendingMeter)
	todayPendingSettlement := mk("today", 0, MoveOutStatusPendingSettlement)
	soonPendingPayment := mk("soon", 4, MoveOutStatusPendingPayment)
	normalReadyToClose := mk("normal", 24, MoveOutStatusReadyToClose)

	repo := &mockMoveOutRepo{
		listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]MoveOutWithRelations, error) {
			return []MoveOutWithRelations{overduePendingMeter, todayPendingSettlement, soonPendingPayment, normalReadyToClose}, nil
		},
	}

	svc := &moveOutService{repo: repo, now: fixedNow}

	res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "active"})
	if err != nil {
		t.Fatalf("Queue: unexpected error: %v", err)
	}

	// 4 sections exist
	for _, key := range []string{"pending_meter", "pending_settlement", "pending_payment", "ready_to_close"} {
		sec, ok := res.Sections[key]
		if !ok {
			t.Fatalf("missing section %q", key)
		}
		if sec.Count != 1 {
			t.Errorf("section %q count: got %d, want 1", key, sec.Count)
		}
	}

	// Verify items landed in correct sections
	if res.Sections["pending_meter"].Items[0].TenantName != "overdue" {
		t.Errorf("pending_meter[0]: got %q, want overdue", res.Sections["pending_meter"].Items[0].TenantName)
	}
	if res.Sections["pending_settlement"].Items[0].TenantName != "today" {
		t.Errorf("pending_settlement[0]: got %q, want today", res.Sections["pending_settlement"].Items[0].TenantName)
	}
	if res.Sections["pending_payment"].Items[0].TenantName != "soon" {
		t.Errorf("pending_payment[0]: got %q, want soon", res.Sections["pending_payment"].Items[0].TenantName)
	}
	if res.Sections["ready_to_close"].Items[0].TenantName != "normal" {
		t.Errorf("ready_to_close[0]: got %q, want normal", res.Sections["ready_to_close"].Items[0].TenantName)
	}

	// Enriched fields
	if res.Sections["pending_meter"].Items[0].Urgency != string(UrgencyOverdue) {
		t.Errorf("overdue urgency: got %q, want OVERDUE", res.Sections["pending_meter"].Items[0].Urgency)
	}
	if res.Sections["pending_settlement"].Items[0].DaysUntil != 0 {
		t.Errorf("today days_until: got %d, want 0", res.Sections["pending_settlement"].Items[0].DaysUntil)
	}

	// Summary: overdue=1, today=1, this_week=2 (today + soon, days 0..7), total=4
	if res.Summary.Overdue != 1 {
		t.Errorf("summary.overdue: got %d, want 1", res.Summary.Overdue)
	}
	if res.Summary.Today != 1 {
		t.Errorf("summary.today: got %d, want 1", res.Summary.Today)
	}
	if res.Summary.ThisWeek != 2 {
		t.Errorf("summary.this_week: got %d, want 2", res.Summary.ThisWeek)
	}
	if res.Summary.TotalActive != 4 {
		t.Errorf("summary.total_active: got %d, want 4", res.Summary.TotalActive)
	}

	// History section is empty (scope=active) but non-nil
	if res.History.Items == nil {
		t.Error("history.items must be non-nil for stable JSON shape")
	}
	if res.History.Count != 0 {
		t.Errorf("history.count: got %d, want 0", res.History.Count)
	}
}

// Truncation: when a section exceeds queueSectionCap (100), Items must be
// capped, Count == len(Items), Truncated == true, and TotalCount must reflect
// the *full* pre-cap size.
func TestQueue_TruncatesSectionAtCap(t *testing.T) {
	today := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	const total = 105
	notices := make([]MoveOutWithRelations, total)
	for i := 0; i < total; i++ {
		notices[i] = MoveOutWithRelations{
			MoveOutNotice: MoveOutNotice{
				ID:                   uuid.New(),
				NoticeDate:           today,
				ScheduledMoveOutDate: today.AddDate(0, 0, 30+i),
				Status:               MoveOutStatusPendingMeter,
			},
		}
	}

	repo := &mockMoveOutRepo{
		listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]MoveOutWithRelations, error) {
			return notices, nil
		},
	}
	svc := &moveOutService{repo: repo, now: func() time.Time { return today }}

	res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "active"})
	if err != nil {
		t.Fatalf("Queue: unexpected error: %v", err)
	}

	pm := res.Sections["pending_meter"]
	if !pm.Truncated {
		t.Error("pending_meter.truncated: got false, want true")
	}
	if pm.Count != queueSectionCap {
		t.Errorf("pending_meter.count: got %d, want %d", pm.Count, queueSectionCap)
	}
	if len(pm.Items) != queueSectionCap {
		t.Errorf("len(items): got %d, want %d", len(pm.Items), queueSectionCap)
	}
	if pm.TotalCount != total {
		t.Errorf("pending_meter.total_count: got %d, want %d", pm.TotalCount, total)
	}

	if res.Summary.TotalActive != total {
		t.Errorf("summary.total_active: got %d, want %d", res.Summary.TotalActive, total)
	}
}

// Scope routing: each scope is its own code path — a single typo in the
// if-conditions would silently swallow a whole section.
func TestQueue_ScopeRouting(t *testing.T) {
	today := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	activeNotice := MoveOutWithRelations{
		MoveOutNotice: MoveOutNotice{
			ID:                   uuid.New(),
			ScheduledMoveOutDate: today,
			Status:               MoveOutStatusPendingMeter,
		},
	}
	historyNotices := []MoveOutWithRelations{
		{MoveOutNotice: MoveOutNotice{ID: uuid.New(), ScheduledMoveOutDate: today.AddDate(0, 0, -3), Status: MoveOutStatusCompleted}},
		{MoveOutNotice: MoveOutNotice{ID: uuid.New(), ScheduledMoveOutDate: today.AddDate(0, 0, -1), Status: MoveOutStatusCancelled}},
	}

	newSvc := func() (*moveOutService, *int, *int) {
		activeCalls, historyCalls := 0, 0
		repo := &mockMoveOutRepo{
			listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]MoveOutWithRelations, error) {
				activeCalls++
				return []MoveOutWithRelations{activeNotice}, nil
			},
			listHistoryFn: func(_ context.Context, _ MoveOutQueueParams) ([]MoveOutWithRelations, error) {
				historyCalls++
				return historyNotices, nil
			},
		}
		return &moveOutService{repo: repo, now: func() time.Time { return today }}, &activeCalls, &historyCalls
	}

	t.Run("history skips active branch", func(t *testing.T) {
		svc, activeCalls, historyCalls := newSvc()
		res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "history"})
		if err != nil {
			t.Fatalf("Queue: %v", err)
		}
		if *activeCalls != 0 {
			t.Errorf("ListActive calls: got %d, want 0", *activeCalls)
		}
		if *historyCalls != 1 {
			t.Errorf("ListHistory calls: got %d, want 1", *historyCalls)
		}
		if res.History.Count != 2 {
			t.Errorf("history.count: got %d, want 2", res.History.Count)
		}
		// All 4 active sections must be non-nil empty slices
		for _, key := range []string{"pending_meter", "pending_settlement", "pending_payment", "ready_to_close"} {
			sec := res.Sections[key]
			if sec.Items == nil {
				t.Errorf("section %q items must be non-nil", key)
			}
		}
		if res.Summary.TotalActive != 0 {
			t.Errorf("summary.total_active: got %d, want 0", res.Summary.TotalActive)
		}
	})

	t.Run("all populates both branches", func(t *testing.T) {
		svc, activeCalls, historyCalls := newSvc()
		res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "all"})
		if err != nil {
			t.Fatalf("Queue: %v", err)
		}
		if *activeCalls != 1 {
			t.Errorf("ListActive calls: got %d, want 1", *activeCalls)
		}
		if *historyCalls != 1 {
			t.Errorf("ListHistory calls: got %d, want 1", *historyCalls)
		}
		if got := res.Sections["pending_meter"].Count; got != 1 {
			t.Errorf("pending_meter.count: got %d, want 1", got)
		}
		if res.History.Count != 2 {
			t.Errorf("history.count: got %d, want 2", res.History.Count)
		}
		if res.Summary.TotalActive != 1 {
			t.Errorf("summary.total_active: got %d, want 1", res.Summary.TotalActive)
		}
	})
}

// Urgency sort within a single section: OVERDUE first, then TODAY, SOON, NORMAL.
func TestQueue_UrgencySortWithinSection(t *testing.T) {
	today := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	mk := func(name string, daysFromToday int) MoveOutWithRelations {
		return MoveOutWithRelations{
			MoveOutNotice: MoveOutNotice{
				ID:                   uuid.New(),
				ScheduledMoveOutDate: today.AddDate(0, 0, daysFromToday),
				Status:               MoveOutStatusPendingMeter,
			},
			TenantName: name,
		}
	}

	// Insert in non-urgency order; service must re-sort
	normal := mk("normal", 20)
	overdue := mk("overdue", -3)
	soon := mk("soon", 5)
	todayItem := mk("today", 0)

	repo := &mockMoveOutRepo{
		listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]MoveOutWithRelations, error) {
			return []MoveOutWithRelations{normal, overdue, soon, todayItem}, nil
		},
	}
	svc := &moveOutService{repo: repo, now: func() time.Time { return today }}

	res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "active"})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}

	items := res.Sections["pending_meter"].Items
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	expected := []string{"overdue", "today", "soon", "normal"}
	for i, want := range expected {
		if items[i].TenantName != want {
			t.Errorf("items[%d]: got %q, want %q", i, items[i].TenantName, want)
		}
	}
}
