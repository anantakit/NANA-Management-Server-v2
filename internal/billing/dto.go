package billing

import (
	"time"

	"nana/internal/shared/money"
	"nana/internal/shared/pagination"

	"github.com/google/uuid"
)

// --- Request DTOs ---

type CreateMonthlyBillRequest struct {
	ContractID   string `json:"contract_id" validate:"required,uuid"`
	BillingMonth string `json:"billing_month" validate:"required,len=7"` // YYYY-MM
	MeterReadingID string `json:"meter_reading_id" validate:"required,uuid"`
}

type VoidBillRequest struct {
	Reason string `json:"reason" validate:"required,min=1,max=100"`
}

// CorrectBillRequest triggers the void+recreate correction flow on a
// FINALIZED bill. Old bill becomes VOID with void_reason=CORRECTION + a
// forward link to the new DRAFT. New DRAFT carries the regenerated line
// items (no override/manual copy — fresh recalculation from source).
//
// CorrectionReason is required and shown verbatim in the audit timeline
// for both the SUPERSEDE and CREATE_FROM_CORRECTION events. Min-length
// matches the locked Phase 1 rule for correction_reason (≥5 chars) so
// the audit row is forensically useful rather than just "fix".
type CorrectBillRequest struct {
	CorrectionReason string `json:"correction_reason" validate:"required,min=5,max=500"`
}

// ManualLineItemRequest is the shared shape for manual line item inputs.
// Used by both UpdateMonthlyDraftRequest (in this file) and
// UpdateSettlementDraftRequest (in internal/billing/settlement/dto.go).
// Stays at billing root because both workflows reference it.
type ManualLineItemRequest struct {
	LineType    string  `json:"line_type" validate:"required"`
	Description string  `json:"description" validate:"required,min=1,max=200"`
	Amount      float64 `json:"amount"` // baht — required when quantity mode is not used
	Quantity    *int    `json:"quantity,omitempty"`
	UnitPrice   *float64 `json:"unit_price,omitempty"` // baht per unit
}

// UpdateMonthlyDraftRequest replaces all MANUAL line items, applies optional
// overrides to AUTO line item amounts, and updates the note on a DRAFT monthly
// bill.
//
// Edit scope (locked, mirrors settlement minus deposit):
//   - MANUAL items: request is the new source of truth — items not in the
//     request are deleted, items in the request are inserted (append after
//     AUTO in request order). Empty array is valid (bill keeps only AUTO).
//   - AUTO items: line_type, description, sort_order are immutable. Only the
//     .amount field is mutable, via the OverrideMap (keyed by LineType).
//   - Deposit fields are intentionally absent — settlement-only concept.
//
// See project_billing_editable_monthly_arch_lock.md for the why.
type UpdateMonthlyDraftRequest struct {
	ManualItems []ManualLineItemRequest `json:"manual_items" validate:"dive"`
	Note        *string                 `json:"note"`
	Overrides   map[string]float64      `json:"overrides"` // override_key (LineType) → baht
}

type BillListParams struct {
	pagination.PaginationParams
	ContractID  string `query:"contract_id"`
	ApartmentID string `query:"apartment_id"`
	Month       string `query:"month"`
	Status      string `query:"status"`
	BillType    string `query:"bill_type"`
}

// BillSummaryParams filters for the summary aggregate endpoint.
// Scoped to apartment + billing month (same as the list page's primary context).
// BillType optionally narrows to a single bill type (e.g. "MONTHLY").
type BillSummaryParams struct {
	ApartmentID string `query:"apartment_id"`
	Month       string `query:"month"`
	BillType    string `query:"bill_type"`
}

// BillSummaryResponse returns aggregate counts and totals for a filtered bill set.
//
// Amount semantics (baht):
//   - total_amount      = sum for non-VOID bills
//   - pending_amount    = sum for FINALIZED bills only (matches pending_count — collectable AR)
//   - paid_amount       = sum for PAID
//   - voided_amount     = sum for VOID (kept for reconciliation; not part of AR)
//
// DRAFT is intentionally excluded from pending_amount: a bill cannot be collected
// until it is FINALIZED. pending_count and pending_amount now describe the same population.
//
// When partial payments are introduced, pending_amount / paid_amount must be
// recomputed from a payments table rather than derived from bill.status.
type BillSummaryResponse struct {
	TotalCount    int     `json:"total_count"`
	PendingCount  int     `json:"pending_count"`
	PaidCount     int     `json:"paid_count"`
	VoidedCount   int     `json:"voided_count"`
	TotalAmount   float64 `json:"total_amount"`
	PendingAmount float64 `json:"pending_amount"`
	PaidAmount    float64 `json:"paid_amount"`
	VoidedAmount  float64 `json:"voided_amount"`
}

// Settlement service-level types (PreviewSettlementInput, SettlementPreview)
// migrated to internal/billing/settlement/dto.go in W4 commit 3 (2026-06-19).

// --- Response DTOs ---

type LineItemResponse struct {
	ID             uuid.UUID `json:"id"`
	LineType       string    `json:"line_type"`
	Source         string    `json:"source"`
	Description    string    `json:"description"`
	Amount         float64   `json:"amount"`
	Quantity       int       `json:"quantity"`
	UnitPrice      float64   `json:"unit_price"`
	SortOrder      int       `json:"sort_order"`
	OverrideKey    string    `json:"override_key"`
	OriginalAmount float64   `json:"original_amount"`
	IsOverridden   bool      `json:"is_overridden"`
	Overrideable   bool      `json:"overrideable"`
	// Phase 6 — Reading Recovery ADJUSTMENT provenance.
	// Populated only on ADJUSTMENT line items (omitempty drops them on every
	// other line type). FK back to the recovery meter row (Phase 5 atomicity).
	// BillDrawer FE consumes these to render the source-link affordance.
	AdjustmentRecoveryReadingID *uuid.UUID `json:"adjustment_recovery_reading_id,omitempty"`
	AdjustmentReasonCode        *string    `json:"adjustment_reason_code,omitempty"`
	AdjustmentNote              *string    `json:"adjustment_note,omitempty"`
}

type BillResponse struct {
	ID             uuid.UUID          `json:"id"`
	ContractID     uuid.UUID          `json:"contract_id"`
	BillingMonth   string             `json:"billing_month"`
	BillType       string             `json:"bill_type"`
	Status         string             `json:"status"`
	VoidReason     *string            `json:"void_reason"`
	// SupersededByBillID forward-links this VOID bill to its replacement
	// when admin uses the void+recreate correction flow. Populated only
	// when status='VOID' AND void_reason='CORRECTION'. Null on every
	// other bill (including VOID(ABSORBED_BY_SETTLEMENT) and unsuperseded
	// VOID). FE renders the "ใบนี้ถูกแทนที่ด้วยใบใหม่" cross-link off
	// this field; absence collapses the cross-link UI.
	SupersededByBillID *uuid.UUID         `json:"superseded_by_bill_id,omitempty"`
	// CorrectedFromBillID is the REVERSE link — populated only when this
	// bill is the DRAFT/FINALIZED/PAID replacement created by a correction.
	// Resolved by GetByID via a single indexed lookup on
	// superseded_by_bill_id (no N+1 because detail is per-click).
	// Omitted on list responses to keep the list path cheap; FE renders
	// the "บิลนี้สร้างจากการแก้ไขบิลเดิม" hint in the drawer off this
	// field's presence. Null on every bill that is not a correction
	// replacement (the common case).
	CorrectedFromBillID *uuid.UUID        `json:"corrected_from_bill_id,omitempty"`
	// CorrectionReason is the admin-typed reason captured at correction
	// time (min 5 chars). Populated only when this bill is VOID with
	// void_reason='CORRECTION' — pulled from the latest SUPERSEDE audit
	// event for this bill. Empty/absent for every other bill. FE renders
	// verbatim beneath the humanized void_reason ("ยกเลิกเพื่อแก้ไข") so
	// admins can see *why* without DB access. Detail-only — list path
	// stays cheap.
	CorrectionReason *string            `json:"correction_reason,omitempty"`
	DepositAmount    float64            `json:"deposit_amount"`
	DepositBalance   float64            `json:"deposit_balance"`
	DepositForfeited bool               `json:"deposit_forfeited"`
	TotalAmount      float64            `json:"total_amount"`
	RentPaid           bool               `json:"rent_paid"`
	SettlementRentMode string             `json:"settlement_rent_mode,omitempty"`
	Note               string             `json:"note"`
	// Override + deposit application fields
	Overrides            map[string]float64 `json:"overrides,omitempty"`
	DepositApplication   string             `json:"deposit_application"`
	CustomDepositApplied float64            `json:"custom_deposit_applied"`
	// Deposit breakdown (settlement bills only)
	DepositApplied  float64 `json:"deposit_applied"`
	DepositRefund   float64 `json:"deposit_refund"`
	DepositWithheld float64 `json:"deposit_withheld"`
	AmountDue       float64 `json:"amount_due"`
	// Relation fields
	TenantName    string             `json:"tenant_name"`
	RoomNumber    string             `json:"room_number"`
	ApartmentName string             `json:"apartment_name"`
	ApartmentID   uuid.UUID          `json:"apartment_id"`
	LineItems     []LineItemResponse `json:"line_items,omitempty"`
	FinalizedAt   *time.Time         `json:"finalized_at,omitempty"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`

	// OverdueDays is the calendar-day count past day-5 due (MONTHLY bills
	// only). Populated by the bill detail endpoint as factual context for
	// the "เลยกำหนด N วัน" primary hint. Omitted when 0 (not overdue).
	// See backlog_late_payment_penalty.md for the v1 invariant lock.
	OverdueDays *int `json:"overdue_days,omitempty"`
	// LatePenaltyReferenceAmount is the apartment's LATE_PENALTY policy
	// rate in baht — surfaced as a muted secondary reference, never as
	// a recommendation. Populated only when OverdueDays > 0 AND the
	// apartment has an active LATE_PENALTY config. Omitted otherwise.
	// Display-only; never mutates the bill, never represents a decision.
	LatePenaltyReferenceAmount *float64 `json:"late_penalty_reference_amount,omitempty"`

	// IsEdited is true iff the bill has at least one edit-class audit
	// event (override change, manual item add/remove, note change).
	// Always present on the response (no omitempty) so the FE can render
	// the Edited badge state directly off this field without inferring
	// from missing-vs-false. FE never sees audit action names.
	IsEdited bool `json:"is_edited"`

	// Payment fields — populated only for PAID bills (nil otherwise).
	// Source: bill_payments table via separate batch/single read.
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	PaymentMethod *string    `json:"payment_method,omitempty"`
	PaymentNote   *string    `json:"payment_note,omitempty"`
	// Payment destination snapshot — frozen at DRAFT creation from routing
	// rules active at the time. Nil on bills created before routing shipped.
	// FE must read only from these fields — never re-resolve from current config.
	PaymentBankName      *string `json:"payment_bank_name,omitempty"`
	PaymentAccountNumber *string `json:"payment_account_number,omitempty"`
	PaymentAccountName   *string `json:"payment_account_name,omitempty"`
	// Delivery fields — populated from bill_deliveries via single aggregate query.
	// DeliveryCount is 0 when never delivered. LastDeliveredAt is nil when never delivered.
	DeliveryCount   int     `json:"delivery_count"`
	LastDeliveredAt *string `json:"last_delivered_at,omitempty"`
}

// BillListItemResponse is the per-row DTO for the bill list endpoint.
//
// AR-lite payment fields (paid_amount, outstanding_amount) are status-derived
// under the current atomic 1-bill-1-payment model — see Bill.PaidAmount /
// Bill.OutstandingAmount for the contract and the migration path when
// partial payments arrive.
type BillListItemResponse struct {
	ID                uuid.UUID  `json:"id"`
	ContractID        uuid.UUID  `json:"contract_id"`
	BillingMonth      string     `json:"billing_month"`
	BillType          string     `json:"bill_type"`
	Status            string     `json:"status"`
	VoidReason        *string    `json:"void_reason"`
	// SupersededByBillID mirrors BillResponse.SupersededByBillID — see
	// that field's doc. Surfaced on the list row so VOID(CORRECTION)
	// rows can render a cross-link badge without an extra fetch.
	SupersededByBillID *uuid.UUID `json:"superseded_by_bill_id,omitempty"`
	TotalAmount       float64    `json:"total_amount"`
	PaidAmount        float64    `json:"paid_amount"`
	OutstandingAmount float64    `json:"outstanding_amount"`
	DepositAmount     float64    `json:"deposit_amount"`
	DepositBalance    float64    `json:"deposit_balance"`
	TenantName        string     `json:"tenant_name"`
	RoomNumber        string     `json:"room_number"`
	ApartmentName     string     `json:"apartment_name"`
	ApartmentID       uuid.UUID  `json:"apartment_id"`
	FinalizedAt       *time.Time `json:"finalized_at,omitempty"`
	CreatedAt         string     `json:"created_at"`
	// IsEdited mirrors BillResponse.IsEdited — see that field's doc.
	// Populated by the list endpoint via a single batched audit query.
	IsEdited bool `json:"is_edited"`
	// Payment fields — nil for non-PAID bills. Batch-loaded, no N+1.
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	PaymentMethod *string    `json:"payment_method,omitempty"`
	// Delivery fields — populated from bill_deliveries via LEFT JOIN LATERAL
	// in FindAll (no N+1). DeliveryCount is 0 when never delivered.
	// LastDeliveredAt is nil when never delivered.
	DeliveryCount   int     `json:"delivery_count"`
	LastDeliveredAt *string `json:"last_delivered_at,omitempty"`
	// Payment destination snapshot — mirrors BillResponse snapshot fields.
	// Used by DeliveryQueueList to gate the เตรียมส่ง CTA without a
	// separate routing-config fetch.
	PaymentBankName      *string `json:"payment_bank_name,omitempty"`
	PaymentAccountNumber *string `json:"payment_account_number,omitempty"`
	PaymentAccountName   *string `json:"payment_account_name,omitempty"`
}

// Settlement transport DTOs (PreviewSettlementRequest, SettlementPreviewResponse,
// PreviewLineItemResponse, DepositBreakdownResponse, AbsorbedBillResponse,
// SettlementOutcome + OutcomeRefund/PayMore/ZeroBalance constants) migrated
// to internal/billing/settlement/dto.go in W4 commit 3 (2026-06-19).

// --- Batch billing repo params (entities stay here, repo lives in monthly) ---
//
// BatchListParams is the query-binding DTO for the batch list endpoint.
// Stays at billing root as a reference-shared shape — monthly's handler
// binds query params into billing.BatchListParams and passes it to
// monthly.BatchRepository.ListBatches. Moving the type into monthly
// would force a billing → monthly import cycle (handler bind site is
// in billing root's DTO surface for historical compatibility).
// All batch response DTOs + converters live in internal/billing/monthly/dto.go.

type BatchListParams struct {
	pagination.PaginationParams
	ApartmentID  string `query:"apartment_id"`
	BillingMonth string `query:"billing_month"`
	Status       string `query:"status"`
}

// --- Converters ---

// toLineItemResponse converts a line item, enriching it with override state from the bill.
//
// Override application applies to ANY AUTO line whose LineType is in
// overrideableLineTypes — both monthly and settlement bills now allow
// overrides via UpdateMonthlyDraft / UpdateSettlementDraft (FE Phase 1).
// The previous `isSettlement` gate left monthly-bill overrides silently
// stripped from the response, breaking the edit drawer's initial-state
// hydration + post-save refetch round-trip.
func toLineItemResponse(li BillLineItem, overrides OverrideMap) LineItemResponse {
	key := li.OverrideKey()
	originalAmount := money.ToBaht(li.Amount)
	effectiveAmount := originalAmount
	isOverridden := false
	overrideable := false

	if li.IsAuto() {
		overrideable = IsOverrideableLineType(li.LineType)
		if override, ok := overrides[key]; ok {
			effectiveAmount = money.ToBaht(override)
			isOverridden = true
		}
	}

	resp := LineItemResponse{
		ID:             li.ID,
		LineType:       string(li.LineType),
		Source:         string(li.Source),
		Description:    li.Description,
		Amount:         effectiveAmount,
		Quantity:       li.Quantity,
		UnitPrice:      money.ToBaht(li.UnitPrice),
		SortOrder:      li.SortOrder,
		OverrideKey:    key,
		OriginalAmount: originalAmount,
		IsOverridden:   isOverridden,
		Overrideable:   overrideable,
	}
	// Phase 6 — ADJUSTMENT provenance pass-through. Fields are nil on every
	// non-ADJUSTMENT line; omitempty drops them from the JSON output.
	if li.LineType == LineItemAdjustment {
		resp.AdjustmentRecoveryReadingID = li.AdjustmentRecoveryReadingID
		if li.AdjustmentReasonCode != nil {
			rc := string(*li.AdjustmentReasonCode)
			resp.AdjustmentReasonCode = &rc
		}
		resp.AdjustmentNote = li.AdjustmentNote
	}
	return resp
}

func ToBillResponse(b Bill) BillResponse {
	items := make([]LineItemResponse, len(b.LineItems))
	for i, li := range b.LineItems {
		items[i] = toLineItemResponse(li, b.Overrides)
	}

	resp := BillResponse{
		ID:               b.ID,
		ContractID:       b.ContractID,
		BillingMonth:     b.BillingMonth,
		BillType:         string(b.BillType),
		Status:           string(b.Status),
		VoidReason:       b.VoidReason,
		SupersededByBillID: b.SupersededByBillID,
		DepositAmount:    money.ToBaht(b.DepositAmount),
		DepositBalance:   money.ToBaht(b.DepositBalance),
		DepositForfeited: b.DepositForfeited,
		TotalAmount:      money.ToBaht(b.TotalAmount),
		RentPaid:         b.RentPaid,
		Note:             b.Note,
		LineItems:        items,
		FinalizedAt:      b.FinalizedAt,
		CreatedAt:        b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	// Overrides are exposed for both monthly + settlement bills now —
	// FE 11's edit drawer needs them to hydrate the override inputs and
	// detect "input != original" for the diff-based PATCH payload.
	if len(b.Overrides) > 0 {
		overrides := make(map[string]float64, len(b.Overrides))
		for k, v := range b.Overrides {
			overrides[k] = money.ToBaht(v)
		}
		resp.Overrides = overrides
	}

	if b.IsSettlement() {
		resp.SettlementRentMode = string(b.SettlementRentMode)

		// Deposit application — settlement-only concept (monthly bills have
		// no deposit). FE 11 explicitly excludes deposit fields from
		// UpdateMonthlyDraftRequest, so leaving these zero on monthly
		// responses is the right default.
		depApp := string(b.DepositApp)
		if depApp == "" {
			depApp = string(DepositAppFull)
		}
		resp.DepositApplication = depApp
		resp.CustomDepositApplied = money.ToBaht(b.CustomDepositApplied)

		// Deposit breakdown
		bd := b.DepositBreakdown()
		resp.DepositApplied = money.ToBaht(bd.AppliedAmount)
		resp.DepositRefund = money.ToBaht(bd.RefundAmount)
		resp.DepositWithheld = money.ToBaht(bd.WithheldAmount)
		resp.AmountDue = money.ToBaht(bd.AmountDue)
	}

	// Payment destination snapshot — pass through as-is (nil when not set)
	resp.PaymentBankName = b.PaymentBankName
	resp.PaymentAccountNumber = b.PaymentAccountNumber
	resp.PaymentAccountName = b.PaymentAccountName

	return resp
}

func ToBillResponseWithRelations(b BillWithRelations) BillResponse {
	resp := ToBillResponse(b.Bill)
	resp.TenantName = b.TenantName
	resp.RoomNumber = b.RoomNumber
	resp.ApartmentName = b.ApartmentName
	resp.ApartmentID = b.ApartmentID
	resp.IsEdited = b.IsEdited
	resp.CorrectedFromBillID = b.CorrectedFromBillID
	if b.CorrectionReason != "" {
		cr := b.CorrectionReason
		resp.CorrectionReason = &cr
	}
	if b.OverdueDays > 0 {
		d := b.OverdueDays
		resp.OverdueDays = &d
	}
	if b.LatePenaltyReferenceAmount > 0 {
		v := money.ToBaht(b.LatePenaltyReferenceAmount)
		resp.LatePenaltyReferenceAmount = &v
	}
	if b.PaidAt != nil {
		resp.PaidAt = b.PaidAt
		resp.PaymentMethod = b.PaymentMethod
		resp.PaymentNote = b.PaymentNote
	}
	resp.DeliveryCount = b.DeliveryCount
	if b.LastDeliveredAt != nil {
		s := b.LastDeliveredAt.Format("2006-01-02T15:04:05Z07:00")
		resp.LastDeliveredAt = &s
	}
	return resp
}

// ToSettlementPreviewResponse migrated to internal/billing/settlement/dto.go
// in W4 commit 3 (2026-06-19).

func ToBillListItemResponse(b BillWithRelations) BillListItemResponse {
	r := BillListItemResponse{
		ID:                 b.ID,
		ContractID:         b.ContractID,
		BillingMonth:       b.BillingMonth,
		BillType:           string(b.BillType),
		Status:             string(b.Status),
		VoidReason:         b.VoidReason,
		SupersededByBillID: b.SupersededByBillID,
		TotalAmount:        money.ToBaht(b.TotalAmount),
		PaidAmount:         money.ToBaht(b.PaidAmount()),
		OutstandingAmount:  money.ToBaht(b.OutstandingAmount()),
		DepositAmount:      money.ToBaht(b.DepositAmount),
		DepositBalance:     money.ToBaht(b.DepositBalance),
		TenantName:         b.TenantName,
		RoomNumber:         b.RoomNumber,
		ApartmentName:      b.ApartmentName,
		ApartmentID:        b.ApartmentID,
		FinalizedAt:        b.FinalizedAt,
		CreatedAt:          b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		IsEdited:           b.IsEdited,
	}
	if b.PaidAt != nil {
		r.PaidAt = b.PaidAt
		r.PaymentMethod = b.PaymentMethod
	}
	r.DeliveryCount = b.DeliveryCount
	if b.LastDeliveredAt != nil {
		s := b.LastDeliveredAt.Format("2006-01-02T15:04:05Z07:00")
		r.LastDeliveredAt = &s
	}
	// Payment destination snapshot — pass through as-is (nil when not set)
	r.PaymentBankName = b.PaymentBankName
	r.PaymentAccountNumber = b.PaymentAccountNumber
	r.PaymentAccountName = b.PaymentAccountName
	return r
}
