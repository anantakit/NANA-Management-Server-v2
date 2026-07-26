package settlement

import (
	"context"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"

	"github.com/google/uuid"
)

// All ports here are CONSUMER-DEFINED (settlement is the consumer; billing
// root + contract + meterreading + billingconfig + moveout + apartment are
// the providers). Bill-table + audit ports are satisfied by
// billing.SettlementAdapter (a narrow adapter struct in billing root);
// cross-feature Source ports are satisfied directly by the existing provider
// repos/services via structural typing — no extra adapter struct lives in
// any provider package for those. DI happens at cmd/main.go.
//
// Returns reference billing.* types directly per Option A (locked 2026-06-19):
// settlement is an internal sub-workflow of the billing domain, not a
// separate bounded context, so translation DTOs would add boilerplate
// without reducing real risk at this scale.
//
// Bill + bill_line_items entities stay in billing root because the bills
// table is shared with monthly. Settlement's repo dependency is more
// entangled with the shared bills table than monthly's batch-table case, so
// a future settlement-specific repo split is FUTURE INVESTIGATION with no
// commitment — see project_billing_extraction_backlog.md §2.
//
// ===========================================================================
// STEWARDSHIP CONTRACT — READ BEFORE WIDENING THESE PORTS
// ===========================================================================
//
// Port naming uses SETTLEMENT-LOCAL INTENT (Store / Source) rather than the
// generic Reader / Commander Q/C split. Mirrors the monthly extraction's
// deviation, locked 2026-06-19. The Q/C split doctrine in
// cross-feature-patterns.md remains the default for cross-feature ports;
// settlement is an intentional deviation because it is a sub-workflow of
// billing, not a separate bounded context.
//
// Stewardship rules:
//
//  1. Adding a new BillStore / AuditStore method is OK ONLY if it's a thin
//     read/write that billing.SettlementAdapter can satisfy with a single
//     line of repo/audit passthrough. If you find yourself wanting business
//     logic inside the adapter to satisfy a new port method, the primitive
//     belongs in the billing root package (extract it there first, then add
//     the port).
//
//  2. Do NOT re-split these ports into Reader / Commander pairs to "comply"
//     with the cross-feature doctrine. The deviation is deliberate — see
//     above.
//
//  3. SettlementAdapter is an EXTRACTION SEAM, not an architectural seam.
//     Do NOT widen ports speculatively on the expectation that a settlement
//     repo split is coming — current evidence (shared bills table) suggests
//     the split has weaker justification than monthly's case. Width is here
//     to enable workflow extraction, not to telegraph a future storage split.
//
//  4. Cross-feature Source ports (Contract / MeterReading / BillingConfig /
//     MoveOut / PaymentRouting) are subsets of billing root's existing
//     *Querier ports. They are satisfied structurally by the SAME repos /
//     services that satisfy root — zero new adapter code lives in any
//     provider package for these. Wiring is at cmd/main.go.
// ===========================================================================

// --- Bill table port ---

// BillStore is the settlement workflow's combined read+write surface over
// the shared `bills` + `bill_line_items` tables. Callers MUST pass a txCtx
// for mutating methods when atomicity with audit emission matters (standard
// pattern for create / update / delete-line-items / lock-for-correction).
type BillStore interface {
	// --- queries ---

	FindByID(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*billing.BillWithRelations, error)
	FindByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string, billType billing.BillType) (*billing.Bill, error)

	// FindNonVoidedByContractAndMonth returns every non-VOID bill for the
	// given (contract, billing_month) regardless of type. Used by
	// findBillsToVoid to identify monthly bills replaced by a settlement.
	FindNonVoidedByContractAndMonth(ctx context.Context, contractID uuid.UUID, billingMonth string) ([]billing.Bill, error)

	// FindUnpaidMonthlyByContractID returns every non-paid, non-voided
	// MONTHLY bill for the contract. Powers settlement absorption: bills
	// older than the settlement month roll up into ABSORBED_BY_SETTLEMENT
	// + an OUTSTANDING_BILL line item.
	FindUnpaidMonthlyByContractID(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error)

	// FindAbsorbedByContractID returns monthly bills voided with reason
	// ABSORBED_BY_SETTLEMENT. Used when a settlement is voided or
	// regenerated so the absorbed monthlies can be restored and either
	// re-absorbed by the new settlement or collected normally if the
	// move-out is cancelled.
	FindAbsorbedByContractID(ctx context.Context, contractID uuid.UUID) ([]billing.Bill, error)

	// HasPaidAdvanceRentForMonth returns true iff the contract has a PAID
	// monthly bill whose advance-rent line covers the given move-out month.
	// Powers addRentAdjustment's "no charge, no refund" branch.
	HasPaidAdvanceRentForMonth(ctx context.Context, contractID uuid.UUID, moveOutMonth string) (bool, error)

	// FindApartmentIDByRoomID is a JOIN read returning the apartment_id for
	// a given room. Used by the planner for prorate-rate + config-fee lookup.
	FindApartmentIDByRoomID(ctx context.Context, roomID uuid.UUID) (uuid.UUID, error)

	// FindRoomApartmentInfo returns both apartment_id and room number for a
	// given room — used by payment-destination routing.
	FindRoomApartmentInfo(ctx context.Context, roomID uuid.UUID) (apartmentID uuid.UUID, roomNumber string, err error)

	// LockBillForCorrection acquires a row-level lock (SELECT FOR UPDATE) on
	// the given bill inside the caller's TX. Gates the double-correction race
	// when two concurrent correction calls target the same settlement bill.
	LockBillForCorrection(ctx context.Context, billID uuid.UUID) (*billing.Bill, error)

	// --- commands ---

	// CreateBill inserts one bill. Caller is responsible for pre-populating
	// the ID when the audit row needs to reference it inside the same TX
	// (standard settlement plan path).
	CreateBill(ctx context.Context, b *billing.Bill) error

	// UpdateBill updates the bill row in place. Covers status transitions
	// (DRAFT → VOID, MarkAbsorbed, MarkSuperseded), total recomputation,
	// override + manual carry-over, and rent-mode persistence.
	UpdateBill(ctx context.Context, b *billing.Bill) error

	// DeleteLineItemsBySource removes all line items with the given source
	// from a bill. Used by UpdateSettlementDraft to delete-then-recreate
	// MANUAL items; AUTO items are never touched this way.
	DeleteLineItemsBySource(ctx context.Context, billID uuid.UUID, source billing.LineItemSource) error

	// DeleteEditableManualLineItems removes hand-editable MANUAL items while
	// PRESERVING recovery ADJUSTMENT lines (source=MANUAL but managed by the
	// applied-corrections flow, not the manual editor). Used by the settlement
	// draft-save wipe so re-saving does not drop an applied recovery refund.
	DeleteEditableManualLineItems(ctx context.Context, billID uuid.UUID) error

	// CreateLineItems inserts a slice of line items belonging to a single
	// bill. Used by the draft-edit replace path + the settlement
	// regeneration carry-over path.
	CreateLineItems(ctx context.Context, items []billing.BillLineItem) error

	// HasNonVoidAdjustmentLineByRecoveryID is the inverse-FK applied-state
	// probe for the recovery-resolution race guard (Q1). "Applied" iff a
	// non-VOID ADJUSTMENT line references the recovery — same derivation the
	// monthly path uses, so a recovery can't be double-applied across bills.
	HasNonVoidAdjustmentLineByRecoveryID(ctx context.Context, recoveryReadingID uuid.UUID) (bool, error)

	// Q1.6 per-utility probes + re-baseline (mirror the billing repo).
	// HasNonVoidAdjustmentLineByRecoveryIDAndUtility scopes the applied-state
	// probe to a utility; HasUnreflectedOverRecordByContractID is the per-utility
	// freshness gate (bill predates a meter correction → stale); ZeroAutoLineUsage
	// re-baselines an affected AUTO line to usage 0 / amount 0 (§3.6).
	HasNonVoidAdjustmentLineByRecoveryIDAndUtility(ctx context.Context, recoveryReadingID uuid.UUID, utility billing.AdjustmentUtility) (bool, error)
	HasUnreflectedOverRecordByContractID(ctx context.Context, contractID uuid.UUID) (bool, error)
	// FindUnreflectedOverRecordsByContractID is the row twin of the freshness
	// gate — the same predicate returning the (recovery, utility) pairs the
	// settlement resolver must emit, so detector and resolver never drift
	// (Epic B INV-1). The gate above is `len(this) > 0`.
	FindUnreflectedOverRecordsByContractID(ctx context.Context, contractID uuid.UUID) ([]billing.UnreflectedOverRecord, error)
	// FindRecoverySourceRefundRates resolves the S0 gate + per-utility refund
	// rate for a discovered recovery — the source month's FINALIZED/PAID bill
	// line unit_price (HC6: system prices at the historical source rate, never
	// the current contract rate). Shared with the monthly path so the refund
	// basis never drifts. (nil, nil) when the source month was never billed.
	FindRecoverySourceRefundRates(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*billing.SourceRefundRates, error)
	ZeroAutoLineUsage(ctx context.Context, billID uuid.UUID, lineType billing.LineItemType) error
}

// --- Audit table port ---

// AuditStore is the settlement workflow's write surface over the shared
// `bill_audit_log` table. Settlement does not currently need a query
// method (the audit-edit-flag read used by GetByID / List lives at billing
// root and is shared across types).
type AuditStore interface {
	// RecordAudit marshals the typed payload and writes one row keyed to
	// the bill. MUST be called with a txCtx if the audit row must roll
	// back alongside the bill mutation (standard contract — failure rolls
	// back the parent workflow step).
	RecordAudit(ctx context.Context, billID uuid.UUID, action billing.BillAuditAction, actor *uuid.UUID, payload any) error
}

// --- Cross-feature read ports (Source suffix per settlement-local intent) ---

// ContractSource is settlement's consumer-defined port onto contract.
// Mirrors billing.ContractQuerier's FindByIDSimple — kept separate so
// settlement doesn't transitively depend on billing root's full port
// surface.
type ContractSource interface {
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*contract.Contract, error)
}

// MeterReadingSource is settlement's consumer-defined port onto
// meterreading. FindExitByRoomID powers settlement planning; FindByIDSimple
// loads a specific recovery meter row when resolving a pending recovery into
// the settlement draft — to confirm anchor identity + read the source billing
// month for the tenant-visible description.
//
// Settlement fetches the exit reading BY TYPE, not by "latest": once Epic B lets
// a READING_RECOVERY row coexist with a move-out, that recovery (a MONTHLY row
// ranked by end-of-its-billing-month) can out-rank the exit reading's actual
// date, so a "latest" lookup would return the recovery and miss the exit.
type MeterReadingSource interface {
	FindExitByRoomID(ctx context.Context, roomID uuid.UUID) (*meterreading.MeterReading, error)
	FindByIDSimple(ctx context.Context, id uuid.UUID) (*meterreading.MeterReading, error)
	// FindReplacementAnchorsByRoomAndMonth returns PHYSICAL_REPLACEMENT events for
	// the settlement month so the exit bill consumes the SAME CanonicalPeriodUsage
	// as Monthly (parity — physical truth is billing-workflow independent).
	FindReplacementAnchorsByRoomAndMonth(ctx context.Context, roomID uuid.UUID, month string) ([]*meterreading.MeterReading, error)
}

// BillingConfigSource is settlement's consumer-defined port onto
// billingconfig. Used for PRORATE_DAILY_RATE lookup (addRentAdjustment) and
// CLEANING_FEE / KEY_SERVICE flat-fee lookup (addConfigFees).
type BillingConfigSource interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID) ([]billingconfig.BillingConfig, error)
}

// MoveOutSource is settlement's consumer-defined port onto moveout.
// Settlement reads the active move-out notice to resolve the actual
// move-out date for Preview / Create / Regenerate paths.
type MoveOutSource interface {
	FindActiveByContractID(ctx context.Context, contractID uuid.UUID) (*moveout.MoveOutNotice, error)
}

// PaymentRoutingSource is settlement's consumer-defined port onto the
// apartment payment-routing service. Returns (nil, nil) when no rules are
// configured — the snapshot fields stay null and delivery blocks at a
// later stage.
type PaymentRoutingSource interface {
	ResolveDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) (*apartment.PaymentDestinationInfo, error)
}
