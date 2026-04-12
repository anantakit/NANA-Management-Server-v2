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
	Queue(ctx context.Context, params MoveOutQueueParams) (*MoveOutQueueResponse, error)

	// Forward commands
	RecordExitMeter(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	GenerateSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	RecordPaymentOutcome(ctx context.Context, id uuid.UUID, req RecordPaymentOutcomeRequest) (*MoveOutWithRelations, error)
	CloseMoveOut(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)

	// Correction commands
	UpdateExitMeter(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	RegenerateSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	ReopenForCorrection(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
}

type moveOutService struct {
	repo        MoveOutRepository
	contracts   ContractQuerier
	contractCmd ContractCommander
	roomCmd     RoomCommander
	meterCmd    MeterReadingCommander
	billingCmd  BillingCommander
	tx          database.TxManager
	// now is the clock used by Queue()/CloseMoveOut() for time-dependent logic.
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
	billingCmd BillingCommander,
	tx database.TxManager,
) MoveOutService {
	return &moveOutService{
		repo:        repo,
		contracts:   contracts,
		contractCmd: contractCmd,
		roomCmd:     roomCmd,
		meterCmd:    meterCmd,
		billingCmd:  billingCmd,
		tx:          tx,
	}
}

// clock returns the current time, using the injected clock if available.
func (s *moveOutService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// --- CRUD ---

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

	c, err := s.contracts.FindByIDSimple(ctx, contractID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	}
	if !c.IsActive() {
		return nil, respond.ErrBadRequest.WithMessage("สัญญานี้ไม่ได้อยู่ในสถานะใช้งาน")
	}

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
		ContractID:           contractID,
		NoticeDate:           noticeDate,
		ScheduledMoveOutDate: moveOutDate,
		Status:               MoveOutStatusPendingMeter,
		Note:                 req.Note,
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

// --- Forward commands ---

// RecordExitMeter advances PENDING_METER → PENDING_SETTLEMENT.
// The EXIT reading must be created separately via the meter-reading endpoint
// before calling this command.
func (s *moveOutService) RecordExitMeter(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.AdvanceToSettlement(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("record exit meter: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// GenerateSettlement creates a DRAFT settlement bill and advances
// PENDING_SETTLEMENT → PENDING_PAYMENT. Snapshots at creation time (D1).
// Invariant D9: 1 active (non-VOIDED) settlement draft per notice.
func (s *moveOutService) GenerateSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.CanRecordSettlement(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		result, err := s.billingCmd.GenerateSettlement(txCtx, notice.ContractID, notice.ScheduledMoveOutDate)
		if err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("generate settlement: %w", err)
		}
		if err := notice.RecordSettlement(result.BillID, result.NetAmount); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("generate settlement: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// RecordPaymentOutcome records how the settlement was resolved and advances
// PENDING_PAYMENT → READY_TO_CLOSE.
func (s *moveOutService) RecordPaymentOutcome(ctx context.Context, id uuid.UUID, req RecordPaymentOutcomeRequest) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		outcome := PaymentOutcome(req.PaymentOutcome)
		if err := notice.RecordPayment(outcome, req.PaymentNote); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("record payment outcome: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// CloseMoveOut transitions READY_TO_CLOSE → COMPLETED, ends the contract,
// and marks the room vacant — all atomically. Requires settlement_bill_id +
// payment_outcome (D5).
func (s *moveOutService) CloseMoveOut(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	now := s.clock()
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.Close(now); err != nil {
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
		return nil, fmt.Errorf("close move-out: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// Cancel transitions a non-terminal notice to CANCELLED. Voids the settlement
// draft if one exists (D8) and soft-deletes the EXIT reading.
func (s *moveOutService) Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		// Guard first — avoid side effects if cancel is not valid
		if err := notice.CanCancel(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		// Void settlement draft if exists (D8: ห้าม hard delete)
		if notice.SettlementBillID != nil {
			if err := s.billingCmd.VoidSettlement(txCtx, *notice.SettlementBillID, "CANCELLED_MOVE_OUT"); err != nil {
				return fmt.Errorf("void settlement: %w", err)
			}
		}
		notice.Status = MoveOutStatusCancelled
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
	return s.repo.FindByID(ctx, noticeID)
}

// --- Correction commands ---

// UpdateExitMeter signals that the EXIT reading has been modified.
// - PENDING_SETTLEMENT: no-op (settlement not yet generated)
// - PENDING_PAYMENT: voids the settlement draft, reverts → PENDING_SETTLEMENT
func (s *moveOutService) UpdateExitMeter(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		switch {
		case notice.IsPendingSettlement():
			// No state change — meter can be changed freely before settlement
		case notice.IsPendingPayment():
			// Void the settlement draft (D8)
			if notice.SettlementBillID != nil {
				if err := s.billingCmd.VoidSettlement(txCtx, *notice.SettlementBillID, "EXIT_METER_UPDATED"); err != nil {
					return fmt.Errorf("void settlement: %w", err)
				}
			}
			notice.Status = MoveOutStatusPendingSettlement
			notice.SettlementBillID = nil
			notice.NetAmount = nil
		default:
			return respond.ErrBadRequest.WithMessage("แก้ไขมิเตอร์ย้ายออกได้เฉพาะสถานะรอสร้างบิลหรือรอชำระ")
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("update exit meter: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// RegenerateSettlement voids the current settlement draft and creates a new
// one from fresh data. Stays in PENDING_PAYMENT.
func (s *moveOutService) RegenerateSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if !notice.IsPendingPayment() {
			return respond.ErrBadRequest.WithMessage("สร้างบิลใหม่ได้เฉพาะสถานะรอชำระ")
		}
		// Void existing settlement (D8)
		if notice.SettlementBillID != nil {
			if err := s.billingCmd.VoidSettlement(txCtx, *notice.SettlementBillID, "REGENERATED"); err != nil {
				return fmt.Errorf("void settlement: %w", err)
			}
		}
		// Generate fresh settlement
		result, err := s.billingCmd.GenerateSettlement(txCtx, notice.ContractID, notice.ScheduledMoveOutDate)
		if err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("regenerate settlement: %w", err)
		}
		notice.SettlementBillID = &result.BillID
		notice.NetAmount = &result.NetAmount
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("regenerate settlement: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// ReopenForCorrection reverts READY_TO_CLOSE → PENDING_PAYMENT, clearing the
// payment outcome so the operator can re-record it.
func (s *moveOutService) ReopenForCorrection(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if !notice.IsReadyToClose() {
			return respond.ErrBadRequest.WithMessage("เปิดแก้ไขได้เฉพาะสถานะพร้อมปิด")
		}
		notice.Status = MoveOutStatusPendingPayment
		notice.PaymentOutcome = nil
		notice.PaymentNote = ""
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("reopen for correction: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}
