package moveout

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Queue happy path: 4 PENDING notices spread across urgency buckets, each
// either with or without an EXIT meter. Verify partition, sort order, and
// summary counts using a fixed clock.
func TestMoveOutService_Queue_PartitionsSortsSummarizes(t *testing.T) {
	today := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	fixedNow := func() time.Time { return today }

	mk := func(name string, daysFromToday int, hasExitMeter bool) NoticeWithMeterFlag {
		return NoticeWithMeterFlag{
			MoveOutWithRelations: MoveOutWithRelations{
				MoveOutNotice: MoveOutNotice{
					ID:                   uuid.New(),
					ContractID:           uuid.New(),
					NoticeDate:           today.AddDate(0, 0, -10),
					ScheduledMoveOutDate: today.AddDate(0, 0, daysFromToday),
					Status:               MoveOutStatusPending,
				},
				TenantName:    name,
				RoomNumber:    "101",
				ApartmentName: "นานาคอร์ท",
			},
			HasExitMeter: hasExitMeter,
		}
	}

	overdueNoMeter := mk("overdue", -5, false) // OVERDUE → awaiting
	todayMeter := mk("today", 0, true)         // TODAY    → ready
	soonNoMeter := mk("soon", 4, false)        // SOON     → awaiting
	normalMeter := mk("normal", 24, true)      // NORMAL   → ready

	repo := &mockMoveOutRepo{
		listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]NoticeWithMeterFlag, error) {
			// Repo returns date-asc; service must re-sort by urgency rank.
			return []NoticeWithMeterFlag{overdueNoMeter, todayMeter, soonNoMeter, normalMeter}, nil
		},
	}

	svc := &moveOutService{
		repo: repo,
		now:  fixedNow,
	}

	res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "active"})
	if err != nil {
		t.Fatalf("Queue: unexpected error: %v", err)
	}

	awaiting, ok := res.Sections["awaiting_meter"]
	if !ok {
		t.Fatal("missing awaiting_meter section")
	}
	ready, ok := res.Sections["ready_to_complete"]
	if !ok {
		t.Fatal("missing ready_to_complete section")
	}

	// Partition
	if awaiting.Count != 2 {
		t.Errorf("awaiting count: got %d, want 2", awaiting.Count)
	}
	if ready.Count != 2 {
		t.Errorf("ready count: got %d, want 2", ready.Count)
	}

	// Sort order: awaiting → OVERDUE first (rank 0), SOON second (rank 2)
	if awaiting.Items[0].TenantName != "overdue" {
		t.Errorf("awaiting[0]: got %q, want overdue", awaiting.Items[0].TenantName)
	}
	if awaiting.Items[1].TenantName != "soon" {
		t.Errorf("awaiting[1]: got %q, want soon", awaiting.Items[1].TenantName)
	}
	// ready → TODAY first (rank 1), NORMAL second (rank 3)
	if ready.Items[0].TenantName != "today" {
		t.Errorf("ready[0]: got %q, want today", ready.Items[0].TenantName)
	}
	if ready.Items[1].TenantName != "normal" {
		t.Errorf("ready[1]: got %q, want normal", ready.Items[1].TenantName)
	}

	// Enriched fields populated
	if awaiting.Items[0].Urgency != string(UrgencyOverdue) {
		t.Errorf("overdue urgency: got %q, want OVERDUE", awaiting.Items[0].Urgency)
	}
	if awaiting.Items[0].WorkflowStatus != string(WorkflowAwaitingMeter) {
		t.Errorf("overdue workflow_status: got %q, want AWAITING_METER", awaiting.Items[0].WorkflowStatus)
	}
	if ready.Items[0].WorkflowStatus != string(WorkflowReadyToComplete) {
		t.Errorf("today workflow_status: got %q, want READY_TO_COMPLETE", ready.Items[0].WorkflowStatus)
	}
	if ready.Items[0].DaysUntil != 0 {
		t.Errorf("today days_until: got %d, want 0", ready.Items[0].DaysUntil)
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

	// Stable shape: not truncated, count == len(items)
	if awaiting.Truncated || ready.Truncated {
		t.Errorf("unexpected truncation: awaiting=%v ready=%v", awaiting.Truncated, ready.Truncated)
	}
	if awaiting.Count != len(awaiting.Items) || ready.Count != len(ready.Items) {
		t.Errorf("count != len(items)")
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
// the *full* pre-cap size — frontend uses TotalCount to render "+N more".
// Off-by-one risk: cap boundary is the most likely place for a logic slip.
func TestMoveOutService_Queue_TruncatesSectionAtCap(t *testing.T) {
	today := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	// 105 awaiting-meter notices, all in NORMAL bucket (same urgency rank
	// → stable sort by date asc, which equals input order here).
	const total = 105
	notices := make([]NoticeWithMeterFlag, total)
	for i := 0; i < total; i++ {
		notices[i] = NoticeWithMeterFlag{
			MoveOutWithRelations: MoveOutWithRelations{
				MoveOutNotice: MoveOutNotice{
					ID:                   uuid.New(),
					NoticeDate:           today,
					ScheduledMoveOutDate: today.AddDate(0, 0, 30+i),
					Status:               MoveOutStatusPending,
				},
			},
			HasExitMeter: false,
		}
	}

	repo := &mockMoveOutRepo{
		listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]NoticeWithMeterFlag, error) {
			return notices, nil
		},
	}
	svc := &moveOutService{repo: repo, now: func() time.Time { return today }}

	res, err := svc.Queue(context.Background(), MoveOutQueueParams{Scope: "active"})
	if err != nil {
		t.Fatalf("Queue: unexpected error: %v", err)
	}

	awaiting := res.Sections["awaiting_meter"]
	if !awaiting.Truncated {
		t.Error("awaiting.truncated: got false, want true")
	}
	if awaiting.Count != queueSectionCap {
		t.Errorf("awaiting.count: got %d, want %d", awaiting.Count, queueSectionCap)
	}
	if len(awaiting.Items) != queueSectionCap {
		t.Errorf("len(items): got %d, want %d", len(awaiting.Items), queueSectionCap)
	}
	if awaiting.TotalCount != total {
		t.Errorf("awaiting.total_count: got %d, want %d (must reflect pre-cap size)", awaiting.TotalCount, total)
	}

	// Summary must count the full pre-cap set (TotalActive == total),
	// otherwise the dashboard under-reports load.
	if res.Summary.TotalActive != total {
		t.Errorf("summary.total_active: got %d, want %d", res.Summary.TotalActive, total)
	}
}

// Scope routing: each scope is its own code path — a single typo in the
// if-conditions would silently swallow a whole section. Asserts via call
// counters on the mock so a typo can't be masked by coincidentally-correct
// result shape.
func TestMoveOutService_Queue_ScopeRouting(t *testing.T) {
	today := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)

	activeNotice := NoticeWithMeterFlag{
		MoveOutWithRelations: MoveOutWithRelations{
			MoveOutNotice: MoveOutNotice{
				ID:                   uuid.New(),
				ScheduledMoveOutDate: today,
				Status:               MoveOutStatusPending,
			},
		},
		HasExitMeter: true,
	}
	historyNotices := []MoveOutWithRelations{
		{MoveOutNotice: MoveOutNotice{ID: uuid.New(), ScheduledMoveOutDate: today.AddDate(0, 0, -3), Status: MoveOutStatusCompleted}},
		{MoveOutNotice: MoveOutNotice{ID: uuid.New(), ScheduledMoveOutDate: today.AddDate(0, 0, -1), Status: MoveOutStatusCancelled}},
	}

	// newSvc returns a fresh service plus pointers to per-subtest call
	// counters, so subtests are independent.
	newSvc := func() (*moveOutService, *int, *int) {
		activeCalls, historyCalls := 0, 0
		repo := &mockMoveOutRepo{
			listActiveFn: func(_ context.Context, _ MoveOutQueueParams) ([]NoticeWithMeterFlag, error) {
				activeCalls++
				return []NoticeWithMeterFlag{activeNotice}, nil
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
			t.Errorf("ListActiveWithMeterFlag calls: got %d, want 0", *activeCalls)
		}
		if *historyCalls != 1 {
			t.Errorf("ListHistory calls: got %d, want 1", *historyCalls)
		}
		if res.History.Count != 2 {
			t.Errorf("history.count: got %d, want 2", res.History.Count)
		}
		// Active sections must be non-nil empty slices for stable JSON shape.
		if res.Sections["awaiting_meter"].Items == nil || res.Sections["ready_to_complete"].Items == nil {
			t.Error("active sections must be non-nil empty slices")
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
			t.Errorf("ListActiveWithMeterFlag calls: got %d, want 1", *activeCalls)
		}
		if *historyCalls != 1 {
			t.Errorf("ListHistory calls: got %d, want 1", *historyCalls)
		}
		if got := res.Sections["ready_to_complete"].Count; got != 1 {
			t.Errorf("ready_to_complete.count: got %d, want 1", got)
		}
		if res.History.Count != 2 {
			t.Errorf("history.count: got %d, want 2", res.History.Count)
		}
		if res.Summary.TotalActive != 1 {
			t.Errorf("summary.total_active: got %d, want 1", res.Summary.TotalActive)
		}
	})
}
