package moveout

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"
	"nana/internal/shared/paymentmethod"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type MoveOutService interface {
	List(ctx context.Context, params MoveOutListParams) ([]MoveOutWithRelations, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Create(ctx context.Context, req CreateMoveOutRequest) (*MoveOutWithRelations, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateMoveOutRequest) (*MoveOutWithRelations, error)
	Queue(ctx context.Context, params MoveOutQueueParams) (*MoveOutQueueResponse, error)

	// Actual move-out date
	SetActualMoveOutDate(ctx context.Context, id uuid.UUID, req SetActualMoveOutDateRequest) (*MoveOutWithRelations, error)

	// Settlement preview (non-persisting, delegates to billing)
	PreviewSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*SettlementPreviewResult, error)

	// Forward commands
	RecordExitMeter(ctx context.Context, id uuid.UUID, req RecordExitMeterRequest) (*MoveOutWithRelations, error)
	GenerateSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*MoveOutWithRelations, error)
	FinalizeSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	RecordPaymentOutcome(ctx context.Context, id uuid.UUID, req RecordPaymentOutcomeRequest) (*MoveOutWithRelations, error)
	SkipPayment(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	CloseMoveOut(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	CloseMoveOutWithUnsettled(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	Cancel(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)

	// Correction commands
	UpdateExitMeter(ctx context.Context, id uuid.UUID, req UpdateExitMeterRequest) (*MoveOutWithRelations, error)
	RegenerateSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*MoveOutWithRelations, error)
	ReopenForCorrection(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error)
	CorrectSettlement(ctx context.Context, id uuid.UUID, req CorrectSettlementRequest, actor *uuid.UUID) (*MoveOutWithRelations, error)
}

type moveOutService struct {
	repo         MoveOutRepository
	contracts    ContractQuerier
	contractCmd  ContractCommander
	roomCmd      RoomCommander
	meterCmd     MeterReadingCommander
	billingCmd   BillingCommander
	billingQuery BillingQuerier
	tx           database.TxManager
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
	billingQuery BillingQuerier,
	tx database.TxManager,
) MoveOutService {
	return &moveOutService{
		repo:         repo,
		contracts:    contracts,
		contractCmd:  contractCmd,
		roomCmd:      roomCmd,
		meterCmd:     meterCmd,
		billingCmd:   billingCmd,
		billingQuery: billingQuery,
		tx:           tx,
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

// --- Actual move-out date ---

// SetActualMoveOutDate sets the real date the tenant vacated. Must be set before
// settlement generation. Validates: non-terminal state, date >= contract start.
func (s *moveOutService) SetActualMoveOutDate(ctx context.Context, id uuid.UUID, req SetActualMoveOutDateRequest) (*MoveOutWithRelations, error) {
	actualDate, err := time.Parse("2006-01-02", req.ActualMoveOutDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ย้ายออกจริงไม่ถูกต้อง")
	}

	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		// Validate against contract start date
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if actualDate.Before(c.StartDate) {
			return respond.ErrBadRequest.WithMessage(ErrActualDateBeforeContractStart.Error())
		}
		if err := notice.SetActualDate(actualDate); err != nil {
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
		return nil, fmt.Errorf("set actual move-out date: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// --- Forward commands ---

// RecordExitMeter is a single business command: set actual move-out date,
// create EXIT reading, and advance PENDING_METER → PENDING_SETTLEMENT — all
// atomically in one transaction.
func (s *moveOutService) RecordExitMeter(ctx context.Context, id uuid.UUID, req RecordExitMeterRequest) (*MoveOutWithRelations, error) {
	actualDate, err := time.Parse("2006-01-02", req.ActualMoveOutDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ย้ายออกจริงไม่ถูกต้อง")
	}

	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}

		// 1. Set actual move-out date
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if actualDate.Before(c.StartDate) {
			return respond.ErrBadRequest.WithMessage(ErrActualDateBeforeContractStart.Error())
		}
		if err := notice.SetActualDate(actualDate); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}

		// 2. Create EXIT meter reading (meter-hardware flags — F1 parity; Model B
		//    over-record flags — the over-record re-anchor is created here, before
		//    the exit reading, when set).
		if err := s.meterCmd.CreateExitForMoveOut(txCtx, c.RoomID, actualDate, req.ElectricityCurrent, req.WaterCurrent,
			req.IsElectricityMeterReplaced, req.IsWaterMeterReplaced, req.IsElectricityMeterRollover, req.IsWaterMeterRollover,
			req.IsElectricityOverRecord, req.IsWaterOverRecord); err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("create exit reading: %w", err)
		}

		// 3. Advance status
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

// PreviewSettlement computes a settlement preview without creating a draft.
// Thin adapter: resolves contract_id from notice, delegates to BillingQuerier.
func (s *moveOutService) PreviewSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*SettlementPreviewResult, error) {
	notice, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
	}
	return s.billingQuery.PreviewSettlementForNotice(ctx, notice.ContractID, rentMode)
}

// GenerateSettlement creates a DRAFT settlement bill and attaches it to the notice.
// Stays in PENDING_SETTLEMENT — user must review/edit then finalize separately.
// Invariant D9: 1 active (non-VOIDED) settlement draft per notice.
func (s *moveOutService) GenerateSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.CanRecordSettlement(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		moveOutDate, err := notice.RequireActualDate()
		if err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		result, err := s.billingCmd.GenerateSettlement(txCtx, notice.ContractID, moveOutDate, rentMode)
		if err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("generate settlement: %w", err)
		}
		if err := notice.AttachDraft(result.BillID, result.NetAmount); err != nil {
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

// FinalizeSettlement finalizes the DRAFT settlement bill and advances
// PENDING_SETTLEMENT → PENDING_PAYMENT. Called after the user reviews the draft.
func (s *moveOutService) FinalizeSettlement(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.CanAdvanceToPayment(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		// Finalize the bill via billing port
		if err := s.billingCmd.FinalizeSettlement(txCtx, *notice.SettlementBillID); err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("finalize settlement: %w", err)
		}
		if err := notice.AdvanceToPayment(); err != nil {
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
		return nil, fmt.Errorf("finalize settlement: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// RecordPaymentOutcome records how the settlement was resolved and advances
// PENDING_PAYMENT → READY_TO_CLOSE. The broadened domain guard also accepts
// READY_TO_CLOSE so the same call can back-fill a skipped settlement or
// correct a previously recorded one (Phase-1 overwrite policy: unrestricted).
func (s *moveOutService) RecordPaymentOutcome(ctx context.Context, id uuid.UUID, req RecordPaymentOutcomeRequest) (*MoveOutWithRelations, error) {
	outcome := PaymentOutcome(req.PaymentOutcome)

	// ZERO_BALANCE normalization (defensive): force method to nil regardless
	// of what FE sent. Protects against stale FE state that retains a
	// previously-picked method when outcome flips to ZERO.
	var method *paymentmethod.PaymentMethod
	if outcome != PaymentOutcomeZeroBalance {
		if req.PaymentMethod == "" {
			return nil, respond.ErrBadRequest.WithMessage("ต้องเลือกวิธีชำระสำหรับผลการชำระแบบนี้")
		}
		m := paymentmethod.PaymentMethod(req.PaymentMethod)
		method = &m
	}

	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.RecordPayment(outcome, method, req.PaymentNote); err != nil {
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

// SkipPayment defers the financial record and moves PENDING_PAYMENT →
// READY_TO_CLOSE without setting an outcome. Idempotent: if the notice is
// already past PENDING_PAYMENT (READY_TO_CLOSE / COMPLETED / CANCELLED) the
// call is a no-op that returns the current state — preventing double-clicks
// from overwriting a previously-recorded outcome via the domain reject path.
func (s *moveOutService) SkipPayment(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		// Idempotent short-circuit, narrow scope: ONLY READY_TO_CLOSE is a
		// legitimate "already-skipped, double-click" case. COMPLETED and
		// CANCELLED fall through to the domain method which rejects with
		// ErrCannotSkipPayment — surfacing the error so an admin acting on a
		// stale notice (e.g. another tab finished closing it) gets a 400 they
		// can recover from instead of a misleading success toast.
		if notice.IsReadyToClose() {
			noticeID = notice.ID
			return nil
		}
		if err := notice.SkipPayment(); err != nil {
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
		return nil, fmt.Errorf("skip payment: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}

// CloseMoveOutWithUnsettled transitions to COMPLETED without recording a
// payment outcome. Phase-2 explicit "ปิดงาน (ยังไม่ชำระ)" path:
//   - PENDING_PAYMENT / READY_TO_CLOSE + nil outcome → COMPLETED (sets
//     ClosedAt, ends contract, marks room vacant) — all atomic.
//   - COMPLETED + nil outcome → idempotent no-op (operator double-clicked or
//     hit the endpoint twice from a stale tab; we return current state
//     instead of erroring so the FE settles cleanly).
//
// Settled notices are rejected — they must use the regular CloseMoveOut path
// so the two routes never blur. Auditability of "closed without payment" is
// captured by the persisted nil PaymentOutcome on a COMPLETED row.
func (s *moveOutService) CloseMoveOutWithUnsettled(ctx context.Context, id uuid.UUID) (*MoveOutWithRelations, error) {
	now := s.clock()
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		// Guard upfront so we surface AppError before any side effect.
		if err := notice.CanCloseWithUnsettled(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		// Idempotent path: already COMPLETED + nil outcome. The contract is
		// already ended and the room already vacant from the original close;
		// re-running EndContract / MarkVacant against an ENDED contract /
		// VACANT room would either error or no-op depending on impl, and the
		// operator semantics ("nothing changed") are clearer if we short-
		// circuit. LastActionAt update is the only mutation we'd want here,
		// but that's part of the audit-log Phase-2 work which is out of
		// scope — see backlog_moveout_phase2_step4.md.
		if notice.IsCompleted() {
			noticeID = notice.ID
			return nil
		}
		moveOutDate, err := notice.RequireActualDate()
		if err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if err := notice.CloseWithUnsettled(now); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		if err := s.contractCmd.EndContract(txCtx, notice.ContractID, moveOutDate); err != nil {
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
		return nil, fmt.Errorf("close move-out with unsettled: %w", err)
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
		moveOutDate, err := notice.RequireActualDate()
		if err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}
		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		if err := s.contractCmd.EndContract(txCtx, notice.ContractID, moveOutDate); err != nil {
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
		// Domain transition stamps both Status + CancelledAt — mirrors
		// notice.Close(now) for the COMPLETED path so the service stays
		// thin and the timestamp can't be forgotten.
		if err := notice.Cancel(time.Now()); err != nil {
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
	return s.repo.FindByID(ctx, noticeID)
}

// --- Correction commands ---

// UpdateExitMeter updates the EXIT meter reading in-place, then handles
// settlement side effects (void + regenerate if draft exists).
// - PENDING_SETTLEMENT: update meter → void+regenerate draft (if exists) → stays PENDING_SETTLEMENT
// - PENDING_PAYMENT: update meter → void+regenerate draft → reverts → PENDING_SETTLEMENT
// All steps run atomically in one transaction.
func (s *moveOutService) UpdateExitMeter(ctx context.Context, id uuid.UUID, req UpdateExitMeterRequest) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}

		if !notice.IsPendingSettlement() && !notice.IsPendingPayment() {
			return respond.ErrBadRequest.WithMessage("แก้ไขมิเตอร์ย้ายออกได้เฉพาะสถานะรอสร้างบิลหรือรอชำระ")
		}

		// 1. Guard: at least one meter field must be provided
		if req.ElectricityCurrent == nil && req.WaterCurrent == nil && req.ReadingDateActual == nil {
			return respond.ErrBadRequest.WithMessage("ต้องระบุค่ามิเตอร์อย่างน้อย 1 รายการ")
		}

		// 2. Resolve roomID via contract
		c, err := s.contracts.FindByIDSimple(txCtx, notice.ContractID)
		if err != nil {
			return fmt.Errorf("find contract: %w", err)
		}

		// 3. Parse optional reading date.
		// When the date changes, sync notice.ActualMoveOutDate too — settlement
		// preview + regenerate read prorate from the notice (RequireActualDate),
		// not from the meter row, so updating only the meter would leave the
		// settlement using the OLD date. Validates against contract start for
		// parity with RecordExitMeter.
		var readingDate *time.Time
		if req.ReadingDateActual != nil {
			d, err := time.Parse("2006-01-02", *req.ReadingDateActual)
			if err != nil {
				return respond.ErrBadRequest.WithMessage("รูปแบบวันที่บันทึกมิเตอร์ไม่ถูกต้อง")
			}
			if d.Before(c.StartDate) {
				return respond.ErrBadRequest.WithMessage(ErrActualDateBeforeContractStart.Error())
			}
			if err := notice.SetActualDate(d); err != nil {
				return respond.ErrBadRequest.WithMessage(err.Error())
			}
			readingDate = &d
		}

		// 4. Update EXIT meter reading in-place
		if err := s.meterCmd.UpdateExitForMoveOut(txCtx, c.RoomID,
			req.ElectricityCurrent, req.WaterCurrent, readingDate,
			req.IsElectricityReplaced, req.IsWaterReplaced,
			req.IsElectricityRollover, req.IsWaterRollover); err != nil {
			if _, ok := respond.Is(err); ok {
				return err
			}
			return fmt.Errorf("update exit reading: %w", err)
		}

		// 5. Handle settlement side effects
		if notice.SettlementBillID != nil {
			// Void + regenerate (preserves MANUAL items + note)
			moveOutDate, err := notice.RequireActualDate()
			if err != nil {
				return respond.ErrBadRequest.WithMessage(err.Error())
			}
			result, err := s.billingCmd.RegenerateSettlement(txCtx, *notice.SettlementBillID, notice.ContractID, moveOutDate, "")
			if err != nil {
				if _, ok := respond.Is(err); ok {
					return err
				}
				return fmt.Errorf("regenerate settlement: %w", err)
			}
			// Explicitly update notice with new bill
			notice.SettlementBillID = &result.BillID
			notice.NetAmount = &result.NetAmount
		}

		// 6. If was PENDING_PAYMENT → revert to PENDING_SETTLEMENT
		// PENDING_PAYMENT references a FINALIZED bill — step 5 voids it
		// (CanVoid accepts FINALIZED) then regenerates a new DRAFT.
		if notice.IsPendingPayment() {
			notice.Status = MoveOutStatusPendingSettlement
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
// one from fresh AUTO data, preserving MANUAL items + note.
// Works from PENDING_SETTLEMENT when a draft is attached.
func (s *moveOutService) RegenerateSettlement(ctx context.Context, id uuid.UUID, rentMode RentMode) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}
		if err := notice.CanRegenerateDraft(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		moveOutDate, err := notice.RequireActualDate()
		if err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		// Void + regenerate with MANUAL preservation
		result, err := s.billingCmd.RegenerateSettlement(txCtx, *notice.SettlementBillID, notice.ContractID, moveOutDate, rentMode)
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

// CorrectSettlement is the move-out workflow's user-triggered void+recreate
// flow for a FINALIZED settlement bill. Allowed from PENDING_PAYMENT or
// READY_TO_CLOSE only — COMPLETED is Phase 2.1F backlog; PAID is blocked
// at the billing layer via CanCorrect.
//
// One tx composes four state mutations: billing voids old + regenerates
// new DRAFT (incl. absorbed-bill restore→re-absorb), notice rebinds
// settlement_bill_id + net_amount, notice downgrades to PENDING_SETTLEMENT
// (mirrors the UpdateExitMeter precedent), notice clears payment metadata.
// See moveout.BillingCommander.CorrectSettlement + design lock for the
// rationale of each piece.
func (s *moveOutService) CorrectSettlement(ctx context.Context, id uuid.UUID, req CorrectSettlementRequest, actor *uuid.UUID) (*MoveOutWithRelations, error) {
	var noticeID uuid.UUID
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		notice, err := s.repo.FindByIDForUpdate(txCtx, id)
		if err != nil {
			return respond.ErrNotFound.WithMessage("ไม่พบใบแจ้งย้ายออก")
		}

		// Guard 1: workflow status must allow the rewind.
		if err := notice.CanDowngradeToPendingSettlement(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		// Guard 2: must have a settlement bill to correct. Independent of
		// status so a future regression that lets a no-bill PENDING_PAYMENT
		// notice slip through still gets caught.
		if notice.SettlementBillID == nil {
			return respond.ErrBadRequest.WithMessage(ErrMissingSettlementBill.Error())
		}

		// Resolve context for billing: contract for rates, actual move-out
		// date for plan rebuild. ActualMoveOutDate is invariant once set
		// (settlement requires it), so RequireActualDate succeeds here for
		// any notice that has a settlement bill attached.
		moveOutDate, err := notice.RequireActualDate()
		if err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}

		// Phase 1: billing voids old + regenerates new (with restore/re-absorb).
		// Empty RentMode → billing reads from the locked old bill, so the
		// correction preserves rent-mode policy (fixing numbers, not policy).
		result, err := s.billingCmd.CorrectSettlement(txCtx, CorrectSettlementInput{
			ExistingBillID:   *notice.SettlementBillID,
			ContractID:       notice.ContractID,
			MoveOutDate:      moveOutDate,
			RentMode:         "",
			CorrectionReason: req.CorrectionReason,
			Actor:            actor,
		})
		if err != nil {
			return err
		}

		// Phase 2: notice rewind. Order matters — DowngradeToPendingSettlement
		// must run BEFORE AttachDraft because AttachDraft's CanRecordSettlement
		// guard requires PENDING_SETTLEMENT.
		if err := notice.DowngradeToPendingSettlement(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := notice.AttachDraft(result.BillID, result.NetAmount); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		notice.ClearPaymentOutcome()

		if err := s.repo.Update(txCtx, notice); err != nil {
			return err
		}
		noticeID = notice.ID
		return nil
	}); err != nil {
		if _, ok := respond.Is(err); ok {
			return nil, err
		}
		return nil, fmt.Errorf("correct settlement: %w", err)
	}
	return s.repo.FindByID(ctx, noticeID)
}
