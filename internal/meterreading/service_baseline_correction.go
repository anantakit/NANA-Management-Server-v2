package meterreading

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nana/internal/shared/database"
	"nana/internal/shared/respond"
)

// CreateBaselineCorrectionInput carries the operator's Reading Recovery commit.
//
// Phase 7 (Split Meter Truth from Financial Truth, locked 2026-06-25):
// the meter-side commit no longer carries financial intent. Amount,
// AdjustmentNote, and ReasonCode were removed — they belong to the
// Adjustment Application event on the bill side (UpdateMonthlyDraft +
// PendingCorrectionsSection). The recovery row stays pure meter truth.
//
// Locks still active on this surface:
//   - Lock A: prev = current (service sets both from one operator input)
//   - Lock C: same-room is structural (derived from source.RoomID)
//   - Lock D: source contract must not have a COMPLETED move-out
//   - Lock E: BillingMonth derived from server clock (not in input)
//
// Doctrine: feedback_reading_recovery_doctrine.md.
type CreateBaselineCorrectionInput struct {
	SourceReadingID    uuid.UUID
	ElectricityCurrent int // operator's physical reading now
	WaterCurrent       int
	AnchorNote         string // required; ≥1 non-whitespace char (ValidateAnchor)
	ActorID            *uuid.UUID
}

// SoftDeletePendingBaselineCorrection enforces the four ownership +
// state invariants from the Phase 7 plan before soft-deleting a
// READING_RECOVERY anchor row. Doctrine: only the LATEST baseline
// correction is editable; older ones are immutable record. Applied
// corrections (referenced by a non-VOID bill line) must be reversed via
// the bill correction flow, never via direct delete.
//
// Pre-tx ordering (cheap reads first to minimize lock time):
//  1. Ownership probe (apartment + room + anchor_reason in one SQL); 404
//     on mismatch — never leak existence.
//  2. Latest-anchor invariant via FindLatestBaselineCorrectionByRoomID.
//  3. Applied-state probe via the BillingApplicationChecker port.
//  4. In-tx soft delete (single-row write; TX preserves atomicity for any
//     future audit hook).
func (s *meterReadingService) SoftDeletePendingBaselineCorrection(ctx context.Context, apartmentID, roomID, correctionID uuid.UUID, actorID *uuid.UUID) error {
	_ = actorID // reserved for future audit emission (Phase 8+).

	correction, err := s.repo.FindBaselineCorrectionByID(ctx, apartmentID, roomID, correctionID)
	if err != nil {
		if database.IsNotFound(err) {
			return respond.ErrNotFound.WithMessage("ไม่พบรายการปรับฐาน")
		}
		return fmt.Errorf("find baseline correction: %w", err)
	}

	latest, err := s.repo.FindLatestBaselineCorrectionByRoomID(ctx, roomID)
	if err != nil {
		if database.IsNotFound(err) {
			// Should be unreachable — we just loaded a row above. Treat as
			// not-found for defensive consistency.
			return respond.ErrNotFound.WithMessage("ไม่พบรายการปรับฐาน")
		}
		return fmt.Errorf("find latest baseline correction: %w", err)
	}
	if latest.ID != correction.ID {
		return ErrCorrectionNotLatest
	}

	applied, err := s.billing.HasNonVoidAdjustmentLine(ctx, correction.ID)
	if err != nil {
		return fmt.Errorf("check applied state: %w", err)
	}
	if applied {
		return ErrCorrectionAlreadyApplied
	}

	return s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		return s.repo.Delete(txCtx, correction.ID)
	})
}

// ListPendingBaselineCorrectionsByRoom returns every pending baseline
// correction for the room (applied rows excluded). Order: created_at
// DESC, source_billing_month DESC — bill edit is an action surface where
// "I just made this, apply it" reads top-down.
//
// Implementation note: repo returns raw rows; this method filters via
// BillingApplicationChecker (cross-feature port) and materializes the
// service DTO with both source + recovery context.
func (s *meterReadingService) ListPendingBaselineCorrectionsByRoom(ctx context.Context, roomID uuid.UUID) ([]PendingBaselineCorrection, error) {
	rows, err := s.repo.FindPendingBaselineCorrectionsByRoomID(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("find pending baseline corrections: %w", err)
	}
	if len(rows) == 0 {
		return []PendingBaselineCorrection{}, nil
	}

	out := make([]PendingBaselineCorrection, 0, len(rows))
	for _, r := range rows {
		applied, err := s.billing.HasNonVoidAdjustmentLine(ctx, r.ID)
		if err != nil {
			return nil, fmt.Errorf("check applied state for %s: %w", r.ID, err)
		}
		if applied {
			continue
		}
		if r.RecoverySourceReadingID == nil {
			// Defensive — schema invariant: READING_RECOVERY rows MUST
			// have a source FK (DB CHECK constraint). Skip rather than
			// crash if the invariant somehow drifted.
			continue
		}
		source, err := s.repo.FindByIDSimple(ctx, *r.RecoverySourceReadingID)
		if err != nil {
			return nil, fmt.Errorf("load recovery source %s: %w", *r.RecoverySourceReadingID, err)
		}
		sourceMonth := ""
		if source.BillingMonth != nil {
			sourceMonth = *source.BillingMonth
		}
		recoveryMonth := ""
		if r.BillingMonth != nil {
			recoveryMonth = *r.BillingMonth
		}
		anchorNote := ""
		if r.AnchorNote != nil {
			anchorNote = *r.AnchorNote
		}
		out = append(out, PendingBaselineCorrection{
			RecoveryID:           r.ID,
			SourceReadingID:      source.ID,
			SourceBillingMonth:   sourceMonth,
			SourceElectricity:    source.ElectricityCurrent,
			SourceWater:          source.WaterCurrent,
			RecoveryBillingMonth: recoveryMonth,
			RecoveryElectricity:  r.ElectricityCurrent,
			RecoveryWater:        r.WaterCurrent,
			RecoveryCreatedAt:    r.CreatedAt,
			AnchorNote:           anchorNote,
		})
	}
	return out, nil
}

// CreateBaselineCorrection commits a Reading Recovery (Phase 7 — meter-only).
//
// Pre-tx validation:
//  1. Source is a regular MONTHLY reading (D2-B).
//  2. Settlement-boundary check (D2-C / Lock D): source's contract must not
//     have any COMPLETED move-out, and the room must still have an active
//     contract today.
//
// In-tx: a single INSERT of the recovery MeterReading row with
// anchor_reason = READING_RECOVERY, anchor_note set, FK to source via
// recovery_source_reading_id, and prev = curr per Lock A. No bill-side
// effect — financial reconciliation happens in BillEditDrawer →
// UpdateMonthlyDraft.applied_corrections.
//
// Append-only doctrine (locked 2026-06-25): the chain is NEVER rewritten.
// Source row stays untouched; intermediate rows untouched; past bills not
// recomputed. The recovery row is appended at the current cycle (Lock E)
// and the operator's source picker is provenance, not target.
//
// Doctrine: feedback_reading_recovery_doctrine.md.
// Plan:     /Users/anantakit/.claude/plans/smooth-coalescing-flute.md.
func (s *meterReadingService) CreateBaselineCorrection(ctx context.Context, input CreateBaselineCorrectionInput) (*MeterReading, error) {
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
	roomID := source.RoomID

	// D2-C (Lock D): settlement-boundary check. Three rejection cases collapse
	// into one error for operator-facing simplicity.
	sourceContractID, err := s.contracts.FindContractIDByRoomAndMonth(ctx, roomID, sourceMonth)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBaselineCorrectionSettlementBoundaryCrossed
		}
		return nil, fmt.Errorf("find source contract: %w", err)
	}
	if _, err := s.contracts.FindActiveContractIDByRoomID(ctx, roomID); err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBaselineCorrectionSettlementBoundaryCrossed
		}
		return nil, fmt.Errorf("find active contract: %w", err)
	}
	hasMoveOut, err := s.moveOuts.HasCompletedMoveOut(ctx, sourceContractID)
	if err != nil {
		return nil, fmt.Errorf("check settlement boundary: %w", err)
	}
	if hasMoveOut {
		return nil, ErrBaselineCorrectionSettlementBoundaryCrossed
	}

	// Lock E: recoveryMonth derived from server clock.
	recoveryMonth := time.Now().Format("2006-01")

	var recovery *MeterReading
	err = s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		recoveryReason := AnchorReasonReadingRecovery
		billingMonth := recoveryMonth
		anchorNote := input.AnchorNote
		sourceID := input.SourceReadingID

		// Lock A: prev = curr is structural — service sets both from one
		// operator input. DB CHECK (migration 00040) + ValidateAnchor are
		// the other two arms of the triple guard.
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
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s.repo.FindByIDSimple(ctx, recovery.ID)
}
