package billing

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// recordAudit is the single entry point for writing one bill audit event.
//
// Caller contract:
//   - ctx MUST be a transaction context (txCtx) from the same RunInTx that
//     performed the state mutation. Audit failure rolls back the parent TX —
//     correctness > availability for billing forensics.
//   - audit is the audit-log repository; callers in billingService pass
//     s.audit. The cross-feature MonthlyAdapter passes its own a.audit.
//   - billID is the bill the event belongs to.
//   - action is the typed BillAuditAction. The matching typed payload struct
//     is what should be passed as `payload` (e.g. AuditUpdateOverride pairs
//     with AuditOverridePayload). The marshaler does not enforce pairing —
//     callers must use the right struct.
//   - actor is *uuid.UUID because some events are system-triggered (e.g.
//     settlement regenerate from move-out workflow has no human actor).
//     nil = system event.
//   - payload may be nil for actions whose action name alone carries the
//     event meaning (none today; reserved for future).
//
// On any error this returns wrapped so the caller can fail the parent TX.
//
// Package-level function (not a method on billingService) so the cross-feature
// adapters (PaymentAdapter, MonthlyAdapter) can share the exact same audit-
// emission path without duplicating the marshal + insert logic. Extracted
// from billingService.recordAudit on 2026-06-19 during the monthly extraction
// scaffold so MonthlyAdapter can be pure-delegation rather than a re-implementation.
func recordAudit(
	ctx context.Context,
	audit BillAuditRepository,
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
	if err := audit.Create(ctx, log); err != nil {
		return fmt.Errorf("record audit %s for bill %s: %w", action, billID, err)
	}
	return nil
}

// emitDraftEditAudit emits one audit row per mutation diff for a draft-edit
// call (UpdateMonthlyDraft / UpdateSettlementDraft). Caller passes the before
// and after snapshots it captured around the mutation. Order of emission is
// deterministic so log timelines reproduce identically across runs.
//
// Diff rules (locked):
//   - Manual items: REMOVE_MANUAL_ITEM per old, ADD_MANUAL_ITEM per new (no
//     identity matching — current update strategy is delete + recreate).
//   - Overrides: UPDATE_OVERRIDE per key whose before != after. Keys absent
//     in one side are treated as nil (semantically distinct from explicit 0).
//   - Note: UPDATE_NOTE only when before != after.
//
// Any recordAudit failure aborts immediately and propagates — the caller is
// running inside RunInTx, so the parent mutation rolls back.
func (s *billingService) emitDraftEditAudit(
	txCtx context.Context,
	billID uuid.UUID,
	actor *uuid.UUID,
	oldManuals []BillLineItem,
	newManuals []BillLineItem,
	oldOverrides OverrideMap,
	newOverrides OverrideMap,
	oldNote string,
	newNote string,
) error {
	// Removes first (mirrors the actual mutation order: delete-then-create).
	for _, m := range oldManuals {
		payload := AuditRemoveManualItemPayload{
			LineType:    string(m.LineType),
			Description: m.Description,
			Amount:      m.Amount,
		}
		if err := recordAudit(txCtx, s.audit, billID, AuditRemoveManualItem, actor, payload); err != nil {
			return err
		}
	}

	// Adds.
	for _, m := range newManuals {
		payload := AuditAddManualItemPayload{
			LineType:    string(m.LineType),
			Description: m.Description,
			Amount:      m.Amount,
		}
		if m.Quantity > 0 {
			q := m.Quantity
			payload.Quantity = &q
		}
		if m.UnitPrice > 0 {
			u := m.UnitPrice
			payload.UnitPrice = &u
		}
		if err := recordAudit(txCtx, s.audit, billID, AuditAddManualItem, actor, payload); err != nil {
			return err
		}
	}

	// Overrides — union of keys, sorted alphabetically for deterministic
	// log order. Skip unchanged keys; emit one row per real transition.
	overrideKeys := map[string]bool{}
	for k := range oldOverrides {
		overrideKeys[k] = true
	}
	for k := range newOverrides {
		overrideKeys[k] = true
	}
	sortedKeys := make([]string, 0, len(overrideKeys))
	for k := range overrideKeys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, k := range sortedKeys {
		oldVal, oldOk := oldOverrides[k]
		newVal, newOk := newOverrides[k]
		if oldOk && newOk && oldVal == newVal {
			continue // unchanged — no audit row
		}
		payload := AuditOverridePayload{Key: k}
		if oldOk {
			v := oldVal
			payload.Before = &v
		}
		if newOk {
			v := newVal
			payload.After = &v
		}
		if err := recordAudit(txCtx, s.audit, billID, AuditUpdateOverride, actor, payload); err != nil {
			return err
		}
	}

	// Note — single comparison, single row if changed.
	if oldNote != newNote {
		payload := AuditUpdateNotePayload{Before: oldNote, After: newNote}
		if err := recordAudit(txCtx, s.audit, billID, AuditUpdateNote, actor, payload); err != nil {
			return err
		}
	}

	return nil
}
