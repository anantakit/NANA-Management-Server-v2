package meterreading

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nana/internal/shared/database"
	"nana/internal/shared/respond"
)

// CreateRecoveryInput carries the operator's Reading Recovery commit
// (Phase 5). BillingMonth is NOT in this struct (Lock E — the service
// derives it from server clock). Amount is the operator's committed
// value (Lock B — backend stores as-is). SourceReadingID determines
// the settlement-boundary check (Lock D — source contract must not
// have a completed move-out).
type CreateRecoveryInput struct {
	SourceReadingID    uuid.UUID
	ElectricityCurrent int       // operator's physical reading now
	WaterCurrent       int
	Amount             int64     // signed satang; negative = refund (Lock B)
	ReasonCode         string    // "METER_RECOVERY" for v1
	AnchorNote         string    // required; ≥1 non-whitespace char (ValidateAnchor)
	AdjustmentNote     string    // required; ≥10 chars trimmed (ValidateAdjustment)
	ActorID            *uuid.UUID
}

// CreateRecovery commits a Reading Recovery in a single atomic transaction:
//  1. Insert recovery MeterReading row (anchor_reason=READING_RECOVERY,
//     prev=curr structural lock per Lock A, FK to source via
//     recovery_source_reading_id).
//  2. Find current-month DRAFT bill on the room's active contract.
//  3. Insert ADJUSTMENT BillLineItem with FK provenance back to the
//     recovery meter row + emit bill_audit_log entry.
// All four writes commit or roll back together.
//
// Lock A: ValidateAnchor catches prev != curr before INSERT (triple-guard
// with DB CHECK migration 00040 + service constructor).
// Lock B: Amount is operator-authoritative; service stores as-is.
// Lock C: same-room is structural (derived from source.RoomID).
// Lock D: source contract must not have a COMPLETED move-out.
// Lock E: BillingMonth derived from server clock.
//
// Doctrine: feedback_reading_recovery_doctrine.md.
// Plan:     /Users/anantakit/.claude/plans/hashed-gliding-crab.md.
func (s *meterReadingService) CreateRecovery(ctx context.Context, input CreateRecoveryInput) (*MeterReading, error) {
	// ── Pre-tx validation (cheap reads; reduce lock time per transaction-pattern.md) ──
	if input.SourceReadingID == uuid.Nil {
		return nil, respond.ErrBadRequest.WithMessage("ต้องระบุมิเตอร์ต้นทาง")
	}

	source, err := s.repo.FindByIDSimple(ctx, input.SourceReadingID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, respond.ErrNotFound.WithMessage("ไม่พบมิเตอร์ต้นทาง")
		}
		return nil, fmt.Errorf("load recovery source: %w", err)
	}

	// D2-B: source must be a regular MONTHLY reading.
	if source.ReadingType != ReadingTypeMonthly {
		return nil, respond.ErrBadRequest.WithMessage("มิเตอร์ต้นทางต้องเป็นรอบเดือน (MONTHLY)")
	}
	if source.BillingMonth == nil {
		return nil, respond.ErrBadRequest.WithMessage("มิเตอร์ต้นทางขาด billing_month")
	}
	sourceMonth := *source.BillingMonth

	// Same-room is structural (Lock C): roomID derived from source.RoomID.
	roomID := source.RoomID

	// D2-C (Lock D): settlement-boundary check. A completed move-out on
	// source's contract closes the recovery chain — regardless of whether
	// the same tenant later returns. Three rejection cases collapse into
	// one error for operator-facing simplicity.
	sourceContractID, err := s.contracts.FindContractIDByRoomAndMonth(ctx, roomID, sourceMonth)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrRecoverySettlementBoundaryCrossed
		}
		return nil, fmt.Errorf("find source contract: %w", err)
	}
	activeContractID, err := s.contracts.FindActiveContractIDByRoomID(ctx, roomID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrRecoverySettlementBoundaryCrossed
		}
		return nil, fmt.Errorf("find active contract: %w", err)
	}
	hasMoveOut, err := s.moveOuts.HasCompletedMoveOut(ctx, sourceContractID)
	if err != nil {
		return nil, fmt.Errorf("check settlement boundary: %w", err)
	}
	if hasMoveOut {
		return nil, ErrRecoverySettlementBoundaryCrossed
	}

	// Lock E: recoveryMonth derived from server clock (not operator input).
	recoveryMonth := time.Now().Format("2006-01")

	// ── In-tx commit ──
	var recovery *MeterReading
	err = s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		recoveryReason := AnchorReasonReadingRecovery
		billingMonth := recoveryMonth
		anchorNote := input.AnchorNote
		sourceID := input.SourceReadingID

		// Lock A (service-layer arm): prev=curr is structural — service
		// sets both from one operator input. DB CHECK (migration 00040)
		// + ValidateAnchor catch any bypass.
		recovery = &MeterReading{
			RoomID:                  roomID,
			ReadingType:             ReadingTypeMonthly,
			BillingMonth:            &billingMonth,
			ElectricityPrevious:     input.ElectricityCurrent,
			ElectricityCurrent:      input.ElectricityCurrent,
			WaterPrevious:           input.WaterCurrent,
			WaterCurrent:            input.WaterCurrent,
			AnchorReason:            &recoveryReason,
			AnchorNote:              &anchorNote,
			RecoverySourceReadingID: &sourceID,
		}
		if err := recovery.ValidateAnchor(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.repo.Create(txCtx, recovery); err != nil {
			return fmt.Errorf("create recovery row: %w", err)
		}

		return s.billingAdj.AttachAdjustmentLine(txCtx, AttachAdjustmentParams{
			ContractID:         activeContractID,
			BillingMonth:       recoveryMonth,
			SourceBillingMonth: sourceMonth,
			Amount:             input.Amount,
			RecoveryReadingID:  recovery.ID,
			ReasonCode:         input.ReasonCode,
			Note:               input.AdjustmentNote,
			ActorID:            input.ActorID,
		})
	})
	if err != nil {
		return nil, err
	}

	// Re-fetch outside tx (read committed state).
	return s.repo.FindByIDSimple(ctx, recovery.ID)
}
