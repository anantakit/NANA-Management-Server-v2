package billing

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// recordAudit is the single entry point for writing one bill audit event.
//
// Caller contract:
//   - ctx MUST be a transaction context (txCtx) from the same RunInTx that
//     performed the state mutation. Audit failure rolls back the parent TX —
//     correctness > availability for billing forensics.
//   - billID is the bill the event belongs to.
//   - action is the typed BillAuditAction. The matching typed payload struct
//     is what should be passed as `payload` (e.g. AuditApplyOverride pairs
//     with AuditOverridePayload). The marshaler does not enforce pairing —
//     callers must use the right struct.
//   - actor is *uuid.UUID because some events are system-triggered (e.g.
//     settlement regenerate from move-out workflow has no human actor).
//     nil = system event.
//   - payload may be nil for actions whose action name alone carries the
//     event meaning (none today; reserved for future).
//
// On any error this returns wrapped so the caller can fail the parent TX.
func (s *billingService) recordAudit(
	ctx context.Context,
	billID uuid.UUID,
	action BillAuditAction,
	actor *uuid.UUID,
	payload any,
) error {
	p, err := MarshalAuditPayload(payload)
	if err != nil {
		return fmt.Errorf("record audit %s for bill %s: %w", action, billID, err)
	}
	log := &BillAuditLog{
		BillID:  billID,
		Action:  action,
		ActorID: actor,
		Payload: p,
	}
	if err := s.audit.Create(ctx, log); err != nil {
		return fmt.Errorf("record audit %s for bill %s: %w", action, billID, err)
	}
	return nil
}
