package moveout

import (
	"context"
	"fmt"
	"sort"
	"time"

	"nana/internal/shared/database"
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

type MoveOutService interface {
	List(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Create(ctx context.Context, req CreateMoveOutRequest) (*MoveOutWithRelations, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateMoveOutRequest) (*MoveOutWithRelations, error)
	Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Complete(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Queue(ctx context.Context, params MoveOutQueueParams) (*MoveOutQueueResponse, error)
}

type moveOutService struct {
	repo        MoveOutRepository
	contracts   ContractQuerier
	contractCmd ContractCommander
	roomCmd     RoomCommander
	meterCmd    MeterReadingCommander
	tx          database.TxManager
	// now is the clock used by Queue() to compute urgency/days_until.
	// nil → time.Now (production); tests assign a fixed-time function.
	now func() time.Time
}

var _ MoveOutService = (*moveOutService)(nil)

func NewMoveOutService(
	repo MoveOutRepository,
	contracts ContractQuerier,
	contractCmd ContractCommander,
	roomCmd RoomCommander,
	meterCmd MeterReadingCommander,
	tx database.TxManager,
) MoveOutService {
	return &moveOutService{
		repo:        repo,
		contracts:   contracts,
		contractCmd: contractCmd,
		roomCmd:     roomCmd,
		meterCmd:    meterCmd,
		tx:          tx,
	}
}

// Queue assembles the move-out queue: PENDING notices partitioned by
// has_exit_meter into "awaiting_meter" / "ready_to_complete" sections (sorted
// by urgency then date), plus a summary computed over the full filtered active
// set, and optionally a recent-history slice. Empty sections are returned with
// non-nil zero-length slices for a stable JSON shape.
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

	now := s.now
	if now == nil {
		now = time.Now
	}
	today := now()

	resp := &MoveOutQueueResponse{
		Sections: map[string]MoveOutQueueSection{
			"awaiting_meter":    emptyQueueSection(),
			"ready_to_complete": emptyQueueSection(),
		},
		History: emptyQueueSection(),
	}

	if scope == queueScopeActive || scope == queueScopeAll {
		active, err := s.repo.ListActiveWithMeterFlag(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list active move-out notices: %w", err)
		}

		var awaiting, ready []NoticeWithMeterFlag
		summary := MoveOutQueueSummary{TotalActive: len(active)}
		for _, n := range active {
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
			if n.HasExitMeter {
				ready = append(ready, n)
			} else {
				awaiting = append(awaiting, n)
			}
		}
		sortByUrgencyThenDate(awaiting, today)
		sortByUrgencyThenDate(ready, today)

		resp.Sections["awaiting_meter"] = buildQueueSection(awaiting, today)
		resp.Sections["ready_to_complete"] = buildQueueSection(ready, today)
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
func buildQueueSection(notices []NoticeWithMeterFlag, today time.Time) MoveOutQueueSection {
	total := len(notices)
	capped := notices
	truncated := false
	if total > queueSectionCap {
		capped = notices[:queueSectionCap]
		truncated = true
	}
	items := make([]MoveOutResponse, len(capped))
	for i, n := range capped {
		items[i] = ToMoveOutResponseWithQueue(n.MoveOutWithRelations, today)
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
func sortByUrgencyThenDate(notices []NoticeWithMeterFlag, today time.Time) {
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

func (s *moveOutService) List(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error) {
	return s.repo.FindAll(ctx, params)
}

func (s *moveOutService) GetByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	notice, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
	}
	return notice, nil
}

func (s *moveOutService) Create(ctx context.Context, req CreateMoveOutRequest) (*MoveOutWithRelations, error) {
	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รหัสสัญญาไม่ถูกต้อง")
	}

	// Verify contract exists and is active
	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	}
	if !c.IsActive() {
		return nil, respond.ErrBadRequest.WithMessage("สัญญานี้ไม่ได้อยู่ในสถานะใช้งาน")
	}

	// Check no existing active notice for this contract
	hasActive, err := s.repo.HasActiveByContractID(ctx, contractID)
	if err != nil {
		return nil, fmt.Errorf("check active notice: %w", err)
	}
	if hasActive {
		return nil, respond.ErrConflict.WithMessage("สัญญานี้มีใบแจ้งย้ายออกอยู่แล้ว")
	}

	noticeDate, err := time.Parse("2006-01-02", req.NoticeDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่แจ้งไม่ถูกต้อง")
	}
	moveOutDate, err := time.Parse("2006-01-02", req.ScheduledMoveOutDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ย้ายออกไม่ถูกต้อง")
	}

	notice := MoveOutNotice{
		ContractID:        contractID,
		NoticeDate:        noticeDate,
		ScheduledMoveOutDate: moveOutDate,
		Status:            MoveOutStatusPendingMeter,
		Note:              req.Note,
	}

	if err := notice.ValidateDates(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Create(ctx, &notice); err != nil {
		return nil, fmt.Errorf("create move-out notice: %w", err)
	}

	result, err := s.repo.FindByID(ctx, notice.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch created notice: %w", err)
	}
	return result, nil
}

func (s *moveOutService) Update(ctx context.Context, id uuid.UUID, req UpdateMoveOutRequest) (*MoveOutWithRelations, error) {
	notice, err := s.repo.FindByIDSimple(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
	}
	if !notice.IsPendingMeter() {
		return nil, respond.ErrBadRequest.WithMessage(ErrNotPendingMeter.Error())
	}

	if req.ScheduledMoveOutDate != nil {
		moveOutDate, err := time.Parse("2006-01-02", *req.ScheduledMoveOutDate)
		if err != nil {
			return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ย้ายออกไม่ถูกต้อง")
		}
		notice.ScheduledMoveOutDate = moveOutDate
	}
	if req.Note != nil {
		notice.Note = *req.Note
	}

	if err := notice.ValidateDates(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.repo.Update(ctx, notice); err != nil {
		return nil, fmt.Errorf("update move-out notice: %w", err)
	}

	result, err := s.repo.FindByID(ctx, notice.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch updated notice: %w", err)
	}
	return result, nil
}

func (s *moveOutService) Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	// Transaction: lock notice row + validate + mark CANCELLED + soft-delete
	// any active EXIT reading for the room. Row lock prevents lost updates
	// from concurrent status transitions. Soft-deleting the EXIT reading lets
	// the workflow be re-initiated cleanly without hitting the unique-index.
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.Cancel(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		if err := s.meterCmd.DeleteExitByRoomID(txCtx, c.RoomID); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("cancel move-out notice: %w", err)
	}

	result, err := s.repo.FindByID(ctx, noticeID)
	if err != nil {
		return nil, fmt.Errorf("fetch cancelled notice: %w", err)
	}
	return result, nil
}

func (s *moveOutService) Complete(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	// Transaction: lock notice row + validate + mark COMPLETED + end contract + mark room vacant.
	// Row lock prevents concurrent cancel/complete from racing.
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		// TODO(V2): replace with full Close() workflow (settlement + payment).
		// For now, directly transition to COMPLETED to keep the old endpoint working.
		notice.Status = MoveOutStatusCompleted
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		if err := s.contractCmd.EndContract(txCtx, notice.ContractID, notice.ScheduledMoveOutDate); err != nil {
			return err
		}
		if err := s.roomCmd.MarkVacant(txCtx, c.RoomID); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("complete move-out: %w", err)
	}

	result, err := s.repo.FindByID(ctx, noticeID)
	if err != nil {
		return nil, fmt.Errorf("fetch completed notice: %w", err)
	}
	return result, nil
}
