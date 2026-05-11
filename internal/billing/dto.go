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

type CreateSettlementBillRequest struct {
	ContractID string `json:"contract_id" validate:"required,uuid"`
	RentMode   string `json:"rent_mode" validate:"omitempty,oneof=PRORATED FULL_MONTH_KEEP_DEPOSIT"`
}

type BatchCreateMonthlyBillsRequest struct {
	ApartmentID  string `json:"apartment_id" validate:"required,uuid"`
	BillingMonth string `json:"billing_month" validate:"required,len=7"` // YYYY-MM
}

// MonthlyPreflightRequest is the query for GET /bills/preflight.
// Read-only counts; no batch row created.
type MonthlyPreflightRequest struct {
	ApartmentID  string `query:"apartment_id" validate:"required,uuid"`
	BillingMonth string `query:"billing_month" validate:"required,len=7"` // YYYY-MM
}

type VoidBillRequest struct {
	Reason string `json:"reason" validate:"required,min=1,max=100"`
}

// UpdateSettlementDraftRequest replaces all MANUAL line items + note on a DRAFT settlement bill.
// Also supports overrides (AUTO item amount adjustments) and deposit application mode.
type UpdateSettlementDraftRequest struct {
	ManualItems          []ManualLineItemRequest `json:"manual_items" validate:"dive"`
	Note                 *string                 `json:"note"`
	Overrides            map[string]float64      `json:"overrides"`                                              // override_key → baht
	DepositApplication   *string                 `json:"deposit_application" validate:"omitempty,oneof=FULL NONE CUSTOM"` // FULL|NONE|CUSTOM
	CustomDepositApplied *float64                `json:"custom_deposit_applied" validate:"omitempty,min=0"`              // baht, when CUSTOM
}

type ManualLineItemRequest struct {
	LineType    string  `json:"line_type" validate:"required"`
	Description string  `json:"description" validate:"required,min=1,max=200"`
	Amount      float64 `json:"amount"` // baht — required when quantity mode is not used
	Quantity    *int    `json:"quantity,omitempty"`
	UnitPrice   *float64 `json:"unit_price,omitempty"` // baht per unit
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
type BillSummaryParams struct {
	ApartmentID string `query:"apartment_id"`
	Month       string `query:"month"`
}

// BillSummaryResponse returns aggregate counts and totals for a filtered bill set.
//
// Amount semantics (baht):
//   - total_amount      = sum for non-VOID bills (existing — unchanged for backward compat)
//   - pending_amount    = sum for DRAFT + FINALIZED (i.e. outstanding receivables under
//     the current atomic 1-bill-1-payment model)
//   - paid_amount       = sum for PAID
//   - voided_amount     = sum for VOID (kept for reconciliation; not part of AR)
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

// --- Service-level input (not transport) ---

// PreviewSettlementInput is the service-layer input for settlement preview.
// Decoupled from handler request DTO.
// MoveOutDate is resolved from the move-out notice (same as CreateSettlementBill).
type PreviewSettlementInput struct {
	ContractID uuid.UUID
	RentMode   SettlementRentMode // empty = PRORATED
}

// SettlementPreview is the service-level result of a settlement preview.
// Contains all data needed by the handler to build the response DTO.
type SettlementPreview struct {
	Plan                 *settlementPlan
	MinMonths            int
	Returnable           bool
	MoveOutDate          time.Time
	EffectiveMoveOutDate time.Time
	RentMode             SettlementRentMode
}

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
}

type BillResponse struct {
	ID             uuid.UUID          `json:"id"`
	ContractID     uuid.UUID          `json:"contract_id"`
	BillingMonth   string             `json:"billing_month"`
	BillType       string             `json:"bill_type"`
	Status         string             `json:"status"`
	VoidReason     *string            `json:"void_reason"`
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
}

// --- Settlement preview DTOs ---

// SettlementOutcome summarizes the net result of a settlement.
type SettlementOutcome string

const (
	OutcomeRefund      SettlementOutcome = "REFUND"
	OutcomePayMore     SettlementOutcome = "PAY_MORE"
	OutcomeZeroBalance SettlementOutcome = "ZERO_BALANCE"
)

// PreviewSettlementRequest is the handler-level request DTO.
type PreviewSettlementRequest struct {
	ContractID string `json:"contract_id" validate:"required,uuid"`
	RentMode   string `json:"rent_mode" validate:"omitempty,oneof=PRORATED FULL_MONTH_KEEP_DEPOSIT"`
}

// SettlementPreviewResponse is the non-persisted preview of a settlement.
// Distinct from BillResponse — preview is not a persisted bill.
type SettlementPreviewResponse struct {
	ContractID          uuid.UUID                `json:"contract_id"`
	BillingMonth        string                   `json:"billing_month"`
	ActualMoveOutDate   string                   `json:"actual_move_out_date"`
	EffectiveMoveOutDate string                  `json:"effective_move_out_date"`
	RentMode            string                   `json:"rent_mode"`
	RentPaid            bool                     `json:"rent_paid"`
	MinMonths           int                      `json:"min_months"`
	DepositReturnable   bool                     `json:"deposit_returnable"`
	LineItems           []PreviewLineItemResponse `json:"line_items"`
	TotalAmount         float64                  `json:"total_amount"`
	Deposit             DepositBreakdownResponse `json:"deposit"`
	AbsorbedBills       []AbsorbedBillResponse   `json:"absorbed_bills"`
	Outcome             SettlementOutcome        `json:"outcome"`
}

type PreviewLineItemResponse struct {
	LineType    string  `json:"line_type"`
	Source      string  `json:"source"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	SortOrder   int     `json:"sort_order"`
}

type DepositBreakdownResponse struct {
	Original  float64 `json:"original"`
	Forfeited float64 `json:"forfeited"`
	Applied   float64 `json:"applied"`
	Refund    float64 `json:"refund"`
	Due       float64 `json:"due"`
}

type AbsorbedBillResponse struct {
	BillID       uuid.UUID `json:"bill_id"`
	BillingMonth string    `json:"billing_month"`
	TotalAmount  float64   `json:"total_amount"`
}

// --- Batch billing DTOs ---

type BatchListParams struct {
	pagination.PaginationParams
	ApartmentID  string `query:"apartment_id"`
	BillingMonth string `query:"billing_month"`
	Status       string `query:"status"`
}

type BatchSummaryResponse struct {
	TotalContracts     int `json:"total_contracts"`
	Created            int `json:"created_count"`
	AlreadyExistsCount int `json:"already_exists_count"`
	Skipped            int `json:"skipped_count"`
	Failed             int `json:"failed_count"`
}

// BatchTriggerResponse is the terse response from POST /bills/batch-monthly.
// FE uses this to redirect to the review page.
type BatchTriggerResponse struct {
	BatchID      uuid.UUID            `json:"batch_id"`
	Status       string               `json:"status"`
	BillingMonth string               `json:"billing_month"`
	ApartmentID  uuid.UUID            `json:"apartment_id"`
	Summary      BatchSummaryResponse `json:"summary"`
	CreatedAt    string               `json:"created_at"`
}

type BatchHeaderResponse struct {
	ID           uuid.UUID            `json:"id"`
	ApartmentID  uuid.UUID            `json:"apartment_id"`
	BillingMonth string               `json:"billing_month"`
	Status       string               `json:"status"`
	Summary      BatchSummaryResponse `json:"summary"`
	CreatedBy    *uuid.UUID           `json:"created_by,omitempty"`
	CreatedAt    string               `json:"created_at"`
	CommitStatus *string              `json:"commit_status,omitempty"`
	CommittedAt  *string              `json:"committed_at,omitempty"`
}

type BatchItemResponse struct {
	ID               uuid.UUID              `json:"id"`
	ContractID       uuid.UUID              `json:"contract_id"`
	RoomID           uuid.UUID              `json:"room_id"`
	RoomNumber       string                 `json:"room_number"`
	RoomFloor        int                    `json:"room_floor"`
	TenantName       string                 `json:"tenant_name"`
	ResultType       string                 `json:"result_type"`
	ReasonCode       string                 `json:"reason_code,omitempty"`
	ReasonText       string                 `json:"reason_text,omitempty"`
	BillID           *uuid.UUID             `json:"bill_id,omitempty"`
	ComputedSnapshot *SnapshotPreview       `json:"computed_snapshot,omitempty"`
}

// SnapshotPreview is the API-facing version of ComputedSnapshot.
// Amounts are converted to baht (float64) for consistency with BillResponse.
type SnapshotPreview struct {
	LineItems   []SnapshotLineItemPreview `json:"line_items"`
	TotalAmount float64                   `json:"total_amount"`
}

type SnapshotLineItemPreview struct {
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Quantity    int     `json:"quantity,omitempty"`
	UnitPrice   float64 `json:"unit_price,omitempty"`
	SortOrder   int     `json:"sort_order,omitempty"`
}

// CommitBatchResponse is returned from POST /bills/batches/:id/commit.
type CommitBatchResponse struct {
	Batch        BatchHeaderResponse `json:"batch"`
	SuccessCount int                 `json:"success_count"`
	FailCount    int                 `json:"fail_count"`
	PendingCount int                 `json:"pending_count"`
}

func ToCommitBatchResponse(r *CommitBatchResult) CommitBatchResponse {
	return CommitBatchResponse{
		Batch:        ToBatchHeaderResponse(r.Batch),
		SuccessCount: r.SuccessCount,
		FailCount:    r.FailCount,
		PendingCount: r.PendingCount,
	}
}

// MonthlyPreflightResponse is the readiness summary returned by
// GET /bills/preflight. Counts only — no per-room detail in P0a.
type MonthlyPreflightResponse struct {
	TotalRooms          int `json:"total_rooms"`
	ReadyCount          int `json:"ready_count"`
	MissingMeterCount   int `json:"missing_meter_count"`
	AlreadyExistsCount  int `json:"already_exists_count"`
	MoveOutPendingCount int `json:"move_out_pending_count"`
	NotBillableCount    int `json:"not_billable_count"`
}

func ToMonthlyPreflightResponse(r *MonthlyPreflightResult) MonthlyPreflightResponse {
	return MonthlyPreflightResponse{
		TotalRooms:          r.TotalRooms,
		ReadyCount:          r.ReadyCount,
		MissingMeterCount:   r.MissingMeterCount,
		AlreadyExistsCount:  r.AlreadyExistsCount,
		MoveOutPendingCount: r.MoveOutPendingCount,
		NotBillableCount:    r.NotBillableCount,
	}
}

func toBatchSummary(b *BillGenerationBatch) BatchSummaryResponse {
	return BatchSummaryResponse{
		TotalContracts:     b.TotalContracts,
		Created:            b.CreatedCount,
		AlreadyExistsCount: b.AlreadyExistsCount,
		Skipped:            b.SkippedCount,
		Failed:             b.FailedCount,
	}
}

func ToBatchTriggerResponse(b *BillGenerationBatch) BatchTriggerResponse {
	return BatchTriggerResponse{
		BatchID:      b.ID,
		Status:       string(b.Status),
		BillingMonth: b.BillingMonth,
		ApartmentID:  b.ApartmentID,
		Summary:      toBatchSummary(b),
		CreatedAt:    b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToBatchHeaderResponse(b *BillGenerationBatch) BatchHeaderResponse {
	resp := BatchHeaderResponse{
		ID:           b.ID,
		ApartmentID:  b.ApartmentID,
		BillingMonth: b.BillingMonth,
		Status:       string(b.Status),
		Summary:      toBatchSummary(b),
		CreatedBy:    b.CreatedBy,
		CreatedAt:    b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if b.CommitStatus != nil {
		s := string(*b.CommitStatus)
		resp.CommitStatus = &s
	}
	if b.CommittedAt != nil {
		s := b.CommittedAt.Format("2006-01-02T15:04:05Z07:00")
		resp.CommittedAt = &s
	}
	return resp
}

func ToBatchItemResponse(i BatchItemWithTenant) BatchItemResponse {
	resp := BatchItemResponse{
		ID:         i.ID,
		ContractID: i.ContractID,
		RoomID:     i.RoomID,
		RoomNumber: i.RoomNumber,
		RoomFloor:  i.RoomFloor,
		TenantName: i.TenantName,
		ResultType: string(i.ResultType),
		ReasonCode: i.ReasonCode,
		ReasonText: i.ReasonText,
		BillID:     i.BillID,
	}
	if i.ResultType == ResultCreated && len(i.ComputedSnapshot.LineItems) > 0 {
		resp.ComputedSnapshot = toSnapshotPreview(i.ComputedSnapshot)
	}
	return resp
}

func toSnapshotPreview(s ComputedSnapshot) *SnapshotPreview {
	items := make([]SnapshotLineItemPreview, len(s.LineItems))
	for i, li := range s.LineItems {
		items[i] = SnapshotLineItemPreview{
			Type:        string(li.Type),
			Description: li.Description,
			Amount:      money.ToBaht(li.Amount),
			Quantity:    li.Quantity,
			UnitPrice:   money.ToBaht(li.UnitPrice),
			SortOrder:   li.SortOrder,
		}
	}
	return &SnapshotPreview{
		LineItems:   items,
		TotalAmount: money.ToBaht(s.TotalAmount),
	}
}

// --- Converters ---

// toLineItemResponse converts a line item, enriching it with override state from the bill.
func toLineItemResponse(li BillLineItem, overrides OverrideMap, isSettlement bool) LineItemResponse {
	key := li.OverrideKey()
	originalAmount := money.ToBaht(li.Amount)
	effectiveAmount := originalAmount
	isOverridden := false
	overrideable := false

	if isSettlement && li.IsAuto() {
		overrideable = IsOverrideableLineType(li.LineType)
		if override, ok := overrides[key]; ok {
			effectiveAmount = money.ToBaht(override)
			isOverridden = true
		}
	}

	return LineItemResponse{
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
}

func ToBillResponse(b Bill) BillResponse {
	isSettlement := b.IsSettlement()

	items := make([]LineItemResponse, len(b.LineItems))
	for i, li := range b.LineItems {
		items[i] = toLineItemResponse(li, b.Overrides, isSettlement)
	}

	resp := BillResponse{
		ID:               b.ID,
		ContractID:       b.ContractID,
		BillingMonth:     b.BillingMonth,
		BillType:         string(b.BillType),
		Status:           string(b.Status),
		VoidReason:       b.VoidReason,
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

	if isSettlement {
		resp.SettlementRentMode = string(b.SettlementRentMode)

		// Override + deposit application
		if len(b.Overrides) > 0 {
			overrides := make(map[string]float64, len(b.Overrides))
			for k, v := range b.Overrides {
				overrides[k] = money.ToBaht(v)
			}
			resp.Overrides = overrides
		}
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

	return resp
}

func ToBillResponseWithRelations(b BillWithRelations) BillResponse {
	resp := ToBillResponse(b.Bill)
	resp.TenantName = b.TenantName
	resp.RoomNumber = b.RoomNumber
	resp.ApartmentName = b.ApartmentName
	resp.ApartmentID = b.ApartmentID
	return resp
}

func ToSettlementPreviewResponse(p *SettlementPreview) SettlementPreviewResponse {
	plan := p.Plan

	items := make([]PreviewLineItemResponse, len(plan.Bill.LineItems))
	for i, li := range plan.Bill.LineItems {
		items[i] = PreviewLineItemResponse{
			LineType:    string(li.LineType),
			Source:      string(li.Source),
			Description: li.Description,
			Amount:      money.ToBaht(li.Amount),
			Quantity:    li.Quantity,
			UnitPrice:   money.ToBaht(li.UnitPrice),
			SortOrder:   li.SortOrder,
		}
	}

	absorbed := make([]AbsorbedBillResponse, len(plan.BillsToAbsorb))
	for i, b := range plan.BillsToAbsorb {
		absorbed[i] = AbsorbedBillResponse{
			BillID:       b.ID,
			BillingMonth: b.BillingMonth,
			TotalAmount:  money.ToBaht(b.TotalAmount),
		}
	}

	d := plan.Deposit
	deposit := DepositBreakdownResponse{
		Original:  money.ToBaht(d.OriginalAmount),
		Forfeited: money.ToBaht(d.ForfeitedAmount),
		Applied:   money.ToBaht(d.AppliedAmount),
		Refund:    money.ToBaht(d.RefundAmount),
		Due:       money.ToBaht(d.AmountDue),
	}

	var outcome SettlementOutcome
	switch {
	case d.RefundAmount > 0:
		outcome = OutcomeRefund
	case d.AmountDue > 0:
		outcome = OutcomePayMore
	default:
		outcome = OutcomeZeroBalance
	}

	return SettlementPreviewResponse{
		ContractID:           plan.Bill.ContractID,
		BillingMonth:         plan.Bill.BillingMonth,
		ActualMoveOutDate:    p.MoveOutDate.Format("2006-01-02"),
		EffectiveMoveOutDate: p.EffectiveMoveOutDate.Format("2006-01-02"),
		RentMode:             string(p.RentMode),
		RentPaid:             plan.Bill.RentPaid,
		MinMonths:            p.MinMonths,
		DepositReturnable:    p.Returnable,
		LineItems:            items,
		TotalAmount:          money.ToBaht(plan.Bill.TotalAmount),
		Deposit:              deposit,
		AbsorbedBills:        absorbed,
		Outcome:              outcome,
	}
}

func ToBillListItemResponse(b BillWithRelations) BillListItemResponse {
	return BillListItemResponse{
		ID:                b.ID,
		ContractID:        b.ContractID,
		BillingMonth:      b.BillingMonth,
		BillType:          string(b.BillType),
		Status:            string(b.Status),
		VoidReason:        b.VoidReason,
		TotalAmount:       money.ToBaht(b.TotalAmount),
		PaidAmount:        money.ToBaht(b.PaidAmount()),
		OutstandingAmount: money.ToBaht(b.OutstandingAmount()),
		DepositAmount:     money.ToBaht(b.DepositAmount),
		DepositBalance:    money.ToBaht(b.DepositBalance),
		TenantName:        b.TenantName,
		RoomNumber:        b.RoomNumber,
		ApartmentName:     b.ApartmentName,
		ApartmentID:       b.ApartmentID,
		FinalizedAt:       b.FinalizedAt,
		CreatedAt:         b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
