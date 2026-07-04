package settlement

import (
	"time"

	"nana/internal/billing"
	"nana/internal/shared/money"

	"github.com/google/uuid"
)

// --- Settlement request DTOs ---

// CreateSettlementBillRequest is the handler-level request body for
// POST /bills/settlement. RentMode is optional and defaults to PRORATED.
type CreateSettlementBillRequest struct {
	ContractID string `json:"contract_id" validate:"required,uuid"`
	RentMode   string `json:"rent_mode" validate:"omitempty,oneof=PRORATED FULL_MONTH_KEEP_DEPOSIT"`
}

// UpdateSettlementDraftRequest replaces all MANUAL line items + note on a
// DRAFT settlement bill. Also supports overrides (AUTO item amount
// adjustments) and deposit application mode. ManualLineItemRequest stays
// at billing root because it is shared with UpdateMonthlyDraft.
type UpdateSettlementDraftRequest struct {
	ManualItems          []billing.ManualLineItemRequest `json:"manual_items" validate:"dive"`
	Note                 *string                         `json:"note"`
	Overrides            map[string]float64              `json:"overrides"`                                                       // override_key → baht
	DepositApplication   *string                         `json:"deposit_application" validate:"omitempty,oneof=FULL NONE CUSTOM"` // FULL|NONE|CUSTOM
	CustomDepositApplied *float64                        `json:"custom_deposit_applied" validate:"omitempty,min=0"`              // baht, when CUSTOM
	// AppliedCorrections resolves pending recoveries into this settlement DRAFT
	// (charge/refund/waive). Mirrors UpdateMonthlyDraft — same doctrine, the
	// settlement bill is the destination. Q1 Recovery Decision.
	AppliedCorrections []billing.AppliedCorrectionInput `json:"applied_corrections" validate:"dive"`
}

// PreviewSettlementRequest is the handler-level request body for
// POST /bills/settlement/preview.
type PreviewSettlementRequest struct {
	ContractID string `json:"contract_id" validate:"required,uuid"`
	RentMode   string `json:"rent_mode" validate:"omitempty,oneof=PRORATED FULL_MONTH_KEEP_DEPOSIT"`
}

// --- Service-level input (not transport) ---

// PreviewSettlementInput is the service-layer input for settlement preview.
// Decoupled from handler request DTO. MoveOutDate is resolved from the
// move-out notice (same as CreateSettlementBill).
type PreviewSettlementInput struct {
	ContractID uuid.UUID
	RentMode   billing.SettlementRentMode // empty = PRORATED
}

// SettlementPreview is the service-level result of a settlement preview.
// Contains all data needed by the handler to build the response DTO.
type SettlementPreview struct {
	Plan                 *settlementPlan
	MinMonths            int
	Returnable           bool
	MoveOutDate          time.Time
	EffectiveMoveOutDate time.Time
	RentMode             billing.SettlementRentMode
}

// --- Settlement preview response DTOs ---

// SettlementOutcome summarizes the net result of a settlement.
type SettlementOutcome string

const (
	OutcomeRefund      SettlementOutcome = "REFUND"
	OutcomePayMore     SettlementOutcome = "PAY_MORE"
	OutcomeZeroBalance SettlementOutcome = "ZERO_BALANCE"
)

// SettlementPreviewResponse is the non-persisted preview of a settlement.
// Distinct from billing.BillResponse — preview is not a persisted bill.
type SettlementPreviewResponse struct {
	ContractID           uuid.UUID                 `json:"contract_id"`
	BillingMonth         string                    `json:"billing_month"`
	ActualMoveOutDate    string                    `json:"actual_move_out_date"`
	EffectiveMoveOutDate string                    `json:"effective_move_out_date"`
	RentMode             string                    `json:"rent_mode"`
	RentPaid             bool                      `json:"rent_paid"`
	MinMonths            int                       `json:"min_months"`
	DepositReturnable    bool                      `json:"deposit_returnable"`
	LineItems            []PreviewLineItemResponse `json:"line_items"`
	TotalAmount          float64                   `json:"total_amount"`
	Deposit              DepositBreakdownResponse  `json:"deposit"`
	AbsorbedBills        []AbsorbedBillResponse    `json:"absorbed_bills"`
	Outcome              SettlementOutcome         `json:"outcome"`
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

// --- Converters ---

// ToSettlementPreviewResponse converts a service-level preview into the
// API response DTO. Lives in the settlement package because the
// SettlementPreview.Plan field references the package-private
// settlementPlan struct.
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
