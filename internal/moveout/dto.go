package moveout

import (
	"time"

	"github.com/google/uuid"
	"nana/internal/shared/money"
	"nana/internal/shared/pagination"
)

// MoveOutListParams holds query parameters for listing move-out notices.
type MoveOutListParams struct {
	pagination.PaginationParams
	Status      string `query:"status"`
	ApartmentID string `query:"apartment_id"`
}

type CreateMoveOutRequest struct {
	ContractID           string `json:"contract_id" validate:"required,uuid"`
	NoticeDate           string `json:"notice_date" validate:"required,datetime=2006-01-02"`
	ScheduledMoveOutDate string `json:"scheduled_move_out_date" validate:"required,datetime=2006-01-02"`
	Note                 string `json:"note"`
}

type UpdateMoveOutRequest struct {
	ScheduledMoveOutDate *string `json:"scheduled_move_out_date"`
	Note                 *string `json:"note"`
}

// SetActualMoveOutDateRequest holds the body for PATCH /:id/actual-date.
type SetActualMoveOutDateRequest struct {
	ActualMoveOutDate string `json:"actual_move_out_date" validate:"required,datetime=2006-01-02"`
}

// RecordExitMeterRequest holds the body for POST /:id/record-exit-meter.
// Single business command: set actual date + create EXIT reading + advance status.
type RecordExitMeterRequest struct {
	ActualMoveOutDate  string `json:"actual_move_out_date" validate:"required,datetime=2006-01-02"`
	ElectricityCurrent int    `json:"electricity_current" validate:"min=0"`
	WaterCurrent       int    `json:"water_current" validate:"min=0"`
}

// RecordPaymentOutcomeRequest holds the body for POST /:id/record-payment.
type RecordPaymentOutcomeRequest struct {
	PaymentOutcome string `json:"payment_outcome" validate:"required,oneof=PAID_EXTRA REFUNDED ZERO_BALANCE"`
	PaymentNote    string `json:"payment_note"`
}

// --- Response ---

type MoveOutResponse struct {
	ID                   string   `json:"id"`
	ContractID           string   `json:"contract_id"`
	RoomID               string   `json:"room_id,omitempty"`
	ApartmentID          string   `json:"apartment_id,omitempty"`
	NoticeDate           string   `json:"notice_date"`
	ScheduledMoveOutDate string   `json:"scheduled_move_out_date"`
	ActualMoveOutDate    string   `json:"actual_move_out_date,omitempty"`
	Status               string   `json:"status"`
	Note                 string   `json:"note"`
	TenantName           string   `json:"tenant_name,omitempty"`
	RoomNumber           string   `json:"room_number,omitempty"`
	ApartmentName        string   `json:"apartment_name,omitempty"`
	Urgency              string   `json:"urgency,omitempty"`
	DaysUntil            int      `json:"days_until"`
	SettlementBillID     string   `json:"settlement_bill_id,omitempty"`
	NetAmount            *float64 `json:"net_amount,omitempty"`
	PaymentOutcome       string   `json:"payment_outcome,omitempty"`
	PaymentNote          string   `json:"payment_note,omitempty"`
	ClosedAt             string   `json:"closed_at,omitempty"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}

// --- Queue DTOs ---

// MoveOutQueueParams holds query parameters for the move-out queue endpoint.
// Scope: "active" (default), "history", or "all".
type MoveOutQueueParams struct {
	Scope       string `query:"scope"`
	Search      string `query:"search"`
	ApartmentID string `query:"apartment_id"`
}

type MoveOutQueueSection struct {
	Items      []MoveOutResponse `json:"items"`
	Count      int               `json:"count"`
	Truncated  bool              `json:"truncated"`
	TotalCount int               `json:"total_count"`
}

type MoveOutQueueSummary struct {
	Overdue     int `json:"overdue"`
	Today       int `json:"today"`
	ThisWeek    int `json:"this_week"`
	TotalActive int `json:"total_active"`
}

type MoveOutQueueResponse struct {
	Sections map[string]MoveOutQueueSection `json:"sections"`
	Summary  MoveOutQueueSummary            `json:"summary"`
	History  MoveOutQueueSection            `json:"history"`
}

// MoveOutWithRelations is a projection for list/detail with joined data.
type MoveOutWithRelations struct {
	MoveOutNotice
	RoomID        uuid.UUID
	ApartmentID   uuid.UUID
	TenantName    string
	RoomNumber    string
	ApartmentName string
}

// --- Mappers ---

func ToMoveOutResponse(m MoveOutWithRelations) MoveOutResponse {
	resp := MoveOutResponse{
		ID:                   m.ID.String(),
		ContractID:           m.ContractID.String(),
		NoticeDate:           m.NoticeDate.Format("2006-01-02"),
		ScheduledMoveOutDate: m.ScheduledMoveOutDate.Format("2006-01-02"),
		Status:               string(m.Status),
		Note:                 m.Note,
		TenantName:           m.TenantName,
		RoomNumber:           m.RoomNumber,
		ApartmentName:        m.ApartmentName,
		CreatedAt:            m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:            m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if m.ActualMoveOutDate != nil {
		resp.ActualMoveOutDate = m.ActualMoveOutDate.Format("2006-01-02")
	}
	if m.RoomID != uuid.Nil {
		resp.RoomID = m.RoomID.String()
	}
	if m.ApartmentID != uuid.Nil {
		resp.ApartmentID = m.ApartmentID.String()
	}
	// V2 fields
	if m.SettlementBillID != nil {
		resp.SettlementBillID = m.SettlementBillID.String()
	}
	if m.NetAmount != nil {
		v := float64(*m.NetAmount) / 100
		resp.NetAmount = &v
	}
	if m.PaymentOutcome != nil {
		resp.PaymentOutcome = string(*m.PaymentOutcome)
	}
	if m.PaymentNote != "" {
		resp.PaymentNote = m.PaymentNote
	}
	if m.ClosedAt != nil {
		resp.ClosedAt = m.ClosedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return resp
}

// ToMoveOutResponseWithQueue enriches a base response with urgency and
// days_until — computed against the supplied "today".
func ToMoveOutResponseWithQueue(m MoveOutWithRelations, today time.Time) MoveOutResponse {
	resp := ToMoveOutResponse(m)
	resp.Urgency = string(ComputeUrgency(m.ScheduledMoveOutDate, today))
	resp.DaysUntil = DaysUntil(m.ScheduledMoveOutDate, today)
	return resp
}

func ToMoveOutResponseList(items []MoveOutWithRelations) []MoveOutResponse {
	result := make([]MoveOutResponse, len(items))
	for i, m := range items {
		result[i] = ToMoveOutResponse(m)
	}
	return result
}

// --- Settlement Preview DTOs ---

// RentModeRequest is the optional body for generate/regenerate settlement.
type RentModeRequest struct {
	RentMode string `json:"rent_mode" validate:"omitempty,oneof=PRORATED FULL_MONTH_KEEP_DEPOSIT"`
}

// SettlementPreviewLineItemResponse is a line item in a settlement preview.
type SettlementPreviewLineItemResponse struct {
	LineType    string  `json:"line_type"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	SortOrder   int     `json:"sort_order"`
}

// SettlementPreviewDepositResponse is deposit breakdown in a settlement preview.
type SettlementPreviewDepositResponse struct {
	Original  float64 `json:"original"`
	Forfeited float64 `json:"forfeited"`
	Applied   float64 `json:"applied"`
	Refund    float64 `json:"refund"`
	Due       float64 `json:"due"`
}

// SettlementPreviewAbsorbedBillResponse is an outstanding bill absorbed into settlement.
type SettlementPreviewAbsorbedBillResponse struct {
	BillID       uuid.UUID `json:"bill_id"`
	BillingMonth string    `json:"billing_month"`
	TotalAmount  float64   `json:"total_amount"`
}

// SettlementPreviewAPIResponse is the move-out-scoped settlement preview response.
type SettlementPreviewAPIResponse struct {
	BillingMonth         string                                  `json:"billing_month"`
	ActualMoveOutDate    string                                  `json:"actual_move_out_date"`
	EffectiveMoveOutDate string                                  `json:"effective_move_out_date"`
	RentMode             string                                  `json:"rent_mode"`
	RentPaid             bool                                    `json:"rent_paid"`
	MinMonths            int                                     `json:"min_months"`
	DepositReturnable    bool                                    `json:"deposit_returnable"`
	LineItems            []SettlementPreviewLineItemResponse     `json:"line_items"`
	TotalAmount          float64                                 `json:"total_amount"`
	Deposit              SettlementPreviewDepositResponse        `json:"deposit"`
	AbsorbedBills        []SettlementPreviewAbsorbedBillResponse `json:"absorbed_bills"`
	Outcome              string                                  `json:"outcome"`
	DataState            string                                  `json:"data_state"`
	Warnings             []string                                `json:"warnings"`
}

// ToSettlementPreviewResponse maps the port result to API response (satang → baht).
func ToSettlementPreviewResponse(r *SettlementPreviewResult) SettlementPreviewAPIResponse {
	items := make([]SettlementPreviewLineItemResponse, len(r.LineItems))
	for i, li := range r.LineItems {
		items[i] = SettlementPreviewLineItemResponse{
			LineType:    li.LineType,
			Description: li.Description,
			Amount:      money.ToBaht(li.Amount),
			Quantity:    li.Quantity,
			UnitPrice:   money.ToBaht(li.UnitPrice),
			SortOrder:   li.SortOrder,
		}
	}

	absorbed := make([]SettlementPreviewAbsorbedBillResponse, len(r.AbsorbedBills))
	for i, b := range r.AbsorbedBills {
		absorbed[i] = SettlementPreviewAbsorbedBillResponse{
			BillID:       b.BillID,
			BillingMonth: b.BillingMonth,
			TotalAmount:  money.ToBaht(b.TotalAmount),
		}
	}

	return SettlementPreviewAPIResponse{
		BillingMonth:         r.BillingMonth,
		ActualMoveOutDate:    r.ActualMoveOutDate.Format("2006-01-02"),
		EffectiveMoveOutDate: r.EffectiveMoveOutDate.Format("2006-01-02"),
		RentMode:             r.RentMode,
		RentPaid:             r.RentPaid,
		MinMonths:            r.MinMonths,
		DepositReturnable:    r.DepositReturnable,
		LineItems:            items,
		TotalAmount:          money.ToBaht(r.TotalAmount),
		Deposit: SettlementPreviewDepositResponse{
			Original:  money.ToBaht(r.Deposit.Original),
			Forfeited: money.ToBaht(r.Deposit.Forfeited),
			Applied:   money.ToBaht(r.Deposit.Applied),
			Refund:    money.ToBaht(r.Deposit.Refund),
			Due:       money.ToBaht(r.Deposit.Due),
		},
		AbsorbedBills: absorbed,
		Outcome:       r.Outcome,
		DataState:     string(r.DataState),
		Warnings:      r.Warnings,
	}
}
