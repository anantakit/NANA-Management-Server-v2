package moveout

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type MoveOutService interface {
	List(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Create(ctx context.Context, req CreateMoveOutRequest) (*MoveOutWithRelations, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateMoveOutRequest) (*MoveOutWithRelations, error)
	Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Complete(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
}

type moveOutService struct {
	repo        MoveOutRepository
	contracts   ContractQuerier
	contractCmd ContractCommander
	roomCmd     RoomCommander
	meterCmd    MeterReadingCommander
	tx          database.TxManager
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
		Status:            MoveOutStatusPending,
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
	if !notice.IsPending() {
		return nil, respond.ErrBadRequest.WithMessage(ErrNotPending.Error())
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
		if err := notice.Complete(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
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
