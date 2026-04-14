package billing

import (
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
}

type BatchCreateMonthlyBillsRequest struct {
	ApartmentID  string `json:"apartment_id" validate:"required,uuid"`
	BillingMonth string `json:"billing_month" validate:"required,len=7"` // YYYY-MM
}

type VoidBillRequest struct {
	Reason string `json:"reason" validate:"required,min=1,max=100"`
}

// UpdateSettlementDraftRequest replaces all MANUAL line items + note on a DRAFT settlement bill.
type UpdateSettlementDraftRequest struct {
	ManualItems []ManualLineItemRequest `json:"manual_items" validate:"dive"`
	Note        *string                 `json:"note"`
}

type ManualLineItemRequest struct {
	LineType    string  `json:"line_type" validate:"required"`
	Description string  `json:"description" validate:"required,min=1,max=200"`
	Amount      float64 `json:"amount" validate:"required"` // baht (converted to satang)
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

// BillSummaryResponse returns aggregate counts and total for a filtered bill set.
type BillSummaryResponse struct {
	TotalCount   int     `json:"total_count"`
	PendingCount int     `json:"pending_count"`
	PaidCount    int     `json:"paid_count"`
	VoidedCount  int     `json:"voided_count"`
	TotalAmount  float64 `json:"total_amount"` // baht, sum of non-VOID bills
}

// --- Response DTOs ---

type LineItemResponse struct {
	ID          uuid.UUID `json:"id"`
	LineType    string    `json:"line_type"`
	Source      string    `json:"source"`
	Description string    `json:"description"`
	Amount      float64   `json:"amount"`
	Quantity    int       `json:"quantity"`
	UnitPrice   float64   `json:"unit_price"`
	SortOrder   int       `json:"sort_order"`
}

type BillResponse struct {
	ID             uuid.UUID          `json:"id"`
	ContractID     uuid.UUID          `json:"contract_id"`
	BillingMonth   string             `json:"billing_month"`
	BillType       string             `json:"bill_type"`
	Status         string             `json:"status"`
	VoidReason     *string            `json:"void_reason"`
	DepositAmount  float64            `json:"deposit_amount"`
	DepositBalance float64            `json:"deposit_balance"`
	TotalAmount    float64            `json:"total_amount"`
	RentPaid       bool               `json:"rent_paid"`
	Note           string             `json:"note"`
	TenantName     string             `json:"tenant_name"`
	RoomNumber     string             `json:"room_number"`
	ApartmentName  string             `json:"apartment_name"`
	ApartmentID    uuid.UUID          `json:"apartment_id"`
	LineItems      []LineItemResponse `json:"line_items,omitempty"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
}

type BillListItemResponse struct {
	ID             uuid.UUID `json:"id"`
	ContractID     uuid.UUID `json:"contract_id"`
	BillingMonth   string    `json:"billing_month"`
	BillType       string    `json:"bill_type"`
	Status         string    `json:"status"`
	TotalAmount    float64   `json:"total_amount"`
	DepositAmount  float64   `json:"deposit_amount"`
	DepositBalance float64   `json:"deposit_balance"`
	TenantName     string    `json:"tenant_name"`
	RoomNumber     string    `json:"room_number"`
	ApartmentName  string    `json:"apartment_name"`
	ApartmentID    uuid.UUID `json:"apartment_id"`
	CreatedAt      string    `json:"created_at"`
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

func ToLineItemResponse(li BillLineItem) LineItemResponse {
	return LineItemResponse{
		ID:          li.ID,
		LineType:    string(li.LineType),
		Source:      string(li.Source),
		Description: li.Description,
		Amount:      money.ToBaht(li.Amount),
		Quantity:    li.Quantity,
		UnitPrice:   money.ToBaht(li.UnitPrice),
		SortOrder:   li.SortOrder,
	}
}

func ToBillResponse(b Bill) BillResponse {
	items := make([]LineItemResponse, len(b.LineItems))
	for i, li := range b.LineItems {
		items[i] = ToLineItemResponse(li)
	}

	return BillResponse{
		ID:             b.ID,
		ContractID:     b.ContractID,
		BillingMonth:   b.BillingMonth,
		BillType:       string(b.BillType),
		Status:         string(b.Status),
		VoidReason:     b.VoidReason,
		DepositAmount:  money.ToBaht(b.DepositAmount),
		DepositBalance: money.ToBaht(b.DepositBalance),
		TotalAmount:    money.ToBaht(b.TotalAmount),
		RentPaid:       b.RentPaid,
		Note:           b.Note,
		LineItems:      items,
		CreatedAt:      b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:      b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToBillResponseWithRelations(b BillWithRelations) BillResponse {
	resp := ToBillResponse(b.Bill)
	resp.TenantName = b.TenantName
	resp.RoomNumber = b.RoomNumber
	resp.ApartmentName = b.ApartmentName
	resp.ApartmentID = b.ApartmentID
	return resp
}

func ToBillListItemResponse(b BillWithRelations) BillListItemResponse {
	return BillListItemResponse{
		ID:             b.ID,
		ContractID:     b.ContractID,
		BillingMonth:   b.BillingMonth,
		BillType:       string(b.BillType),
		Status:         string(b.Status),
		TotalAmount:    money.ToBaht(b.TotalAmount),
		DepositAmount:  money.ToBaht(b.DepositAmount),
		DepositBalance: money.ToBaht(b.DepositBalance),
		TenantName:     b.TenantName,
		RoomNumber:     b.RoomNumber,
		ApartmentName:  b.ApartmentName,
		ApartmentID:    b.ApartmentID,
		CreatedAt:      b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
