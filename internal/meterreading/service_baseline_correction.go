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
//   - Lock C: same-room is structural — derived from source.RoomID when a
//     source is supplied; on the source-optional path (source nil) the room
//     is carried by RoomID instead (the source used to be the room anchor).
//   - Lock D: source contract must not have a COMPLETED move-out (source path);
//     nil path enforces an active contract on the room (§1.4 of the scope).
//   - Lock E: BillingMonth derived from server clock (not in input)
//
// Source-optional relaxation (locked 2026-07-01): SourceReadingID is a
// *uuid.UUID — nil means the operator supplied no source. Absence is a valid,
// complete recovery, not a gap; no inference fills it. RoomID is consumed only
// on the nil path (when a source is present it is ignored — source.RoomID wins).
//
// Doctrine: feedback_reading_recovery_doctrine.md.
type CreateBaselineCorrectionInput struct {
	SourceReadingID    *uuid.UUID // nil = no source supplied (optional narrative metadata)
	RoomID             *uuid.UUID // room anchor for the nil-source path; ignored when source present
	ElectricityCurrent int        // operator's physical reading now
	WaterCurrent       int
	// Q1.5 over-record: previously-recorded (wrong) value per utility. nil =
	// that utility not corrected. Must be >= the matching current (ValidateAnchor).
	ElectricityRecorded *int
	WaterRecorded       *int
	AnchorNote          string // required; ≥1 non-whitespace char (ValidateAnchor)
	ActorID             *uuid.UUID
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

// CreateBaselineCorrection commits a Reading Recovery (Phase 7 — meter-only).
//
// Source-optional (locked 2026-07-01): a source is enrichment, not a gate. The
// method branches on whether the operator supplied one:
//
//   - Source SUPPLIED: the source is validated as a regular MONTHLY reading
//     (D2-B), the room is derived from source.RoomID (Lock C), and the Lock D
//     reach-back boundary check runs against the source month's contract —
//     preventing an edit into a closed settlement epoch.
//   - Source NIL: no source to load or validate; the room is carried by
//     input.RoomID. Settlement safety is preserved by requiring the room to
//     have an ACTIVE contract today (§1.4). An active contract is by
//     definition not a completed move-out, so the closed-epoch hazard is
//     structurally absent on this path.
//
// In-tx: a single INSERT of the recovery MeterReading row with
// anchor_reason = READING_RECOVERY, anchor_note set, FK to source via
// recovery_source_reading_id (NULL when nil path), and prev = curr per Lock A.
// No bill-side effect — financial reconciliation happens in BillEditDrawer →
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
	var roomID uuid.UUID

	if input.SourceReadingID != nil {
		// --- Source-supplied path (unchanged) ---
		source, err := s.repo.FindByIDSimple(ctx, *input.SourceReadingID)
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
		roomID = source.RoomID // Lock C: room is structural, derived from source

		// D2-C (Lock D): settlement-boundary reach-back check on the source
		// month's contract. Three rejection cases collapse into one error for
		// operator-facing simplicity.
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
	} else {
		// --- Source-optional (nil) path ---
		// No source to derive the room from; the operator's room anchor
		// carries it. Settlement safety (§1.4): require a live contract —
		// an active contract is never a completed move-out, so the
		// closed-epoch hazard is structurally absent here.
		if input.RoomID == nil {
			return nil, respond.ErrBadRequest.WithMessage("ต้องระบุห้อง")
		}
		roomID = *input.RoomID
		if _, err := s.contracts.FindActiveContractIDByRoomID(ctx, roomID); err != nil {
			if database.IsNotFound(err) {
				return nil, ErrBaselineCorrectionSettlementBoundaryCrossed
			}
			return nil, fmt.Errorf("find active contract: %w", err)
		}
	}

	// Lock E: recoveryMonth derived from server clock.
	recoveryMonth := time.Now().Format("2006-01")

	var recovery *MeterReading
	err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		recoveryReason := AnchorReasonReadingRecovery
		billingMonth := recoveryMonth
		anchorNote := input.AnchorNote

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
			RecoverySourceReadingID: input.SourceReadingID, // nil-safe: NULL FK on the nil path
			// Q1.5 over-record: persist recorded per utility (nil-safe: NULL when
			// the utility is not part of this correction).
			ElectricityRecorded: input.ElectricityRecorded,
			WaterRecorded:       input.WaterRecorded,
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
