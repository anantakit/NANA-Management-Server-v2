package moveout

import (
	"context"
	"fmt"
	"sort"
	"time"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// queueSectionCap caps each section's items after the repo's hard cap of 200.
const queueSectionCap = 100

// Queue scope values — whitelist enforced at service entry.
const (
	queueScopeActive  = "active"
	queueScopeHistory = "history"
	queueScopeAll     = "all"
)

// queueSectionKey maps each active status to its JSON section key.
var queueSectionKey = map[MoveOutStatus]string{
	MoveOutStatusPendingMeter:      "pending_meter",
	MoveOutStatusPendingSettlement: "pending_settlement",
	MoveOutStatusPendingPayment:    "pending_payment",
	MoveOutStatusReadyToClose:      "ready_to_close",
}

// queueBucketFor returns the queue section a notice should be partitioned
// into. COMPLETED + nil PaymentOutcome (closed-with-unsettled) routes into
// the pending_payment bucket so the financial backlog surfaces alongside
// the rest of the unsettled work — keeps the operator mental model
// "everything needing payment lives in one tab".
func queueBucketFor(n MoveOutNotice) MoveOutStatus {
	if n.IsCompleted() && n.PaymentOutcome == nil {
		return MoveOutStatusPendingPayment
	}
	return n.Status
}

// Queue assembles the move-out queue: active notices partitioned by status
// into 4 sections (pending_meter / pending_settlement / pending_payment /
// ready_to_close), sorted by urgency then date, plus a summary and an
// optional recent-history slice. Empty sections use non-nil zero-length
// slices for a stable JSON shape.
func (s *moveOutService) Queue(ctx context.Context, params MoveOutQueueParams) (*MoveOutQueueResponse, error) {
	scope := params.Scope
	if scope == "" {
		scope = queueScopeActive
	}
	if scope != queueScopeActive && scope != queueScopeHistory && scope != queueScopeAll {
		return nil, respond.ErrBadRequest.WithMessage("scope ต้องเป็น active, history หรือ all")
	}
	if params.ApartmentID != "" {
		if _, err := uuid.Parse(params.ApartmentID); err != nil {
			return nil, respond.ErrBadRequest.WithMessage("รหัสอาคารไม่ถูกต้อง")
		}
	}

	today := s.clock()

	resp := &MoveOutQueueResponse{
		Sections: make(map[string]MoveOutQueueSection, len(queueSectionKey)),
		History:  emptyQueueSection(),
	}
	for _, key := range queueSectionKey {
		resp.Sections[key] = emptyQueueSection()
	}

	if scope == queueScopeActive || scope == queueScopeAll {
		active, err := s.repo.ListActive(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list active move-out notices: %w", err)
		}

		// Partition by status
		buckets := make(map[MoveOutStatus][]MoveOutWithRelations, len(queueSectionKey))
		for status := range queueSectionKey {
			buckets[status] = []MoveOutWithRelations{}
		}

		summary := MoveOutQueueSummary{TotalActive: len(active)}
		for _, n := range active {
			// Urgency is a deadline concept (scheduled_move_out_date vs today).
			// COMPLETED + UNSETTLED items live here because the financial
			// record is open, NOT because they have a future deadline — they
			// already moved out. Counting them as OVERDUE would inflate the
			// urgency chips with bookkeeping backlog that doesn't need a
			// scheduled response. We still count them in TotalActive so the
			// payment-tab badge reflects all rows the operator can act on.
			if !n.IsCompleted() {
				d := DaysUntil(n.ScheduledMoveOutDate, today)
				switch {
				case d < 0:
					summary.Overdue++
				case d == 0:
					summary.Today++
				}
				if d >= 0 && d <= 7 {
					summary.ThisWeek++
				}
			}
			bucket := queueBucketFor(n.MoveOutNotice)
			if _, ok := buckets[bucket]; ok {
				buckets[bucket] = append(buckets[bucket], n)
			}
		}

		for status, key := range queueSectionKey {
			items := buckets[status]
			sortByUrgencyThenDate(items, today)
			resp.Sections[key] = buildQueueSection(items, today)
		}
		resp.Summary = summary
	}

	if scope == queueScopeHistory || scope == queueScopeAll {
		history, err := s.repo.ListHistory(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list move-out history: %w", err)
		}
		items := make([]MoveOutResponse, len(history))
		for i, h := range history {
			items[i] = ToMoveOutResponseWithQueue(h, today)
		}
		resp.History = MoveOutQueueSection{
			Items:      items,
			Count:      len(items),
			TotalCount: len(items),
		}
	}

	return resp, nil
}

func emptyQueueSection() MoveOutQueueSection {
	return MoveOutQueueSection{Items: []MoveOutResponse{}}
}

// buildQueueSection caps notices at queueSectionCap and converts them to
// response items, recording total/truncated.
func buildQueueSection(notices []MoveOutWithRelations, today time.Time) MoveOutQueueSection {
	total := len(notices)
	capped := notices
	truncated := false
	if total > queueSectionCap {
		capped = notices[:queueSectionCap]
		truncated = true
	}
	items := make([]MoveOutResponse, len(capped))
	for i, n := range capped {
		items[i] = ToMoveOutResponseWithQueue(n, today)
	}
	return MoveOutQueueSection{
		Items:      items,
		Count:      len(items),
		Truncated:  truncated,
		TotalCount: total,
	}
}

// sortByUrgencyThenDate orders notices by urgency bucket
// (OVERDUE→TODAY→SOON→NORMAL) then ascending scheduled date within a bucket.
// Stable sort to keep equal-key rows in repo (date-asc) order.
func sortByUrgencyThenDate(notices []MoveOutWithRelations, today time.Time) {
	sort.SliceStable(notices, func(i, j int) bool {
		di := DaysUntil(notices[i].ScheduledMoveOutDate, today)
		dj := DaysUntil(notices[j].ScheduledMoveOutDate, today)
		ri := urgencyRank(di)
		rj := urgencyRank(dj)
		if ri != rj {
			return ri < rj
		}
		return notices[i].ScheduledMoveOutDate.Before(notices[j].ScheduledMoveOutDate)
	})
}

func urgencyRank(d int) int {
	switch {
	case d < 0:
		return 0
	case d == 0:
		return 1
	case d <= 7:
		return 2
	default:
		return 3
	}
}
