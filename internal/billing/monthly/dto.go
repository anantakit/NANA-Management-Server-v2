package monthly

import (
	"nana/internal/billing"
	"nana/internal/shared/money"

	"github.com/google/uuid"
)

// --- Request DTOs ---

type BatchCreateMonthlyBillsRequest struct {
	ApartmentID  string `json:"apartment_id" validate:"required,uuid"`
	BillingMonth string `json:"billing_month" validate:"required,len=7"` // YYYY-MM
}

// FinalizeAllByMonthRequest scopes a per-month bulk finalize. Companion to
// the batch-scoped POST /batches/:id/finalize-all — used when bills were
// created via the reconciliation Generate path (no Batch entity exists).
type FinalizeAllByMonthRequest struct {
	ApartmentID  string `json:"apartment_id" validate:"required,uuid"`
	BillingMonth string `json:"billing_month" validate:"required,len=7"` // YYYY-MM
}

// MonthlyPreflightRequest is the query for GET /bills/preflight.
// Read-only counts; no batch row created.
type MonthlyPreflightRequest struct {
	ApartmentID  string `query:"apartment_id" validate:"required,uuid"`
	BillingMonth string `query:"billing_month" validate:"required,len=7"` // YYYY-MM
}

// --- Service result types ---

// MonthlyPreflightResult is the readiness count summary for the Generate page
// — same classification a real batch run would produce, read-only.
type MonthlyPreflightResult struct {
	TotalRooms          int
	ReadyCount          int
	MissingMeterCount   int
	AlreadyExistsCount  int
	MoveOutPendingCount int
	NotBillableCount    int
}

// BatchFinalizeFailureCode enumerates the FE-renderable reasons a bill
// could not be finalized as part of a bulk batch-finalize call. FE switches
// on Code to pick a Thai message + remediation hint per row.
type BatchFinalizeFailureCode string

const (
	// FailureCodeNoLineItems — bill has zero line items, cannot finalize
	// (CanFinalize guard). Admin must add items via UpdateMonthlyDraft first.
	FailureCodeNoLineItems BatchFinalizeFailureCode = "NO_LINE_ITEMS"
	// FailureCodeNotDraft — bill is in a non-DRAFT, non-FINALIZED state
	// (VOID / PAID). Already-FINALIZED bills are silent-skipped, not surfaced
	// here.
	FailureCodeNotDraft BatchFinalizeFailureCode = "NOT_DRAFT"
	// FailureCodeInfraError — system error during persist or audit emission.
	// Surfaces opaquely so admin retries via the same endpoint. Underlying
	// error is server-logged with bill_id for ops triage.
	FailureCodeInfraError BatchFinalizeFailureCode = "INFRA_ERROR"
	// FailureCodePendingRecovery — the bill's room has an unresolved recovery
	// (Q1 finalization gate). Business-rule skip, not a system error: admin must
	// resolve it (charge/refund/waive) before this bill can finalize.
	FailureCodePendingRecovery BatchFinalizeFailureCode = "PENDING_RECOVERY"
)

// BatchFinalizeFailure is one row in the failure list returned by
// BatchFinalizeAll. Order matches the processed-bill order so the FE can
// align failures with the visible row order on BillBatchReview.
type BatchFinalizeFailure struct {
	BillID  uuid.UUID                `json:"bill_id"`
	Code    BatchFinalizeFailureCode `json:"code"`
	Message string                   `json:"message"` // Thai user-facing
}

// BatchFinalizeResult is the full response from BatchFinalizeAll.
//
// success_count = bills newly transitioned DRAFT→FINALIZED in this call.
// fail_count    = len(failures).
// Bills already FINALIZED before this call are SILENT — they neither
// increment success_count nor appear in failures (idempotency).
type BatchFinalizeResult struct {
	TotalCount   int                    `json:"total_count"`
	SuccessCount int                    `json:"success_count"`
	FailCount    int                    `json:"fail_count"`
	Failures     []BatchFinalizeFailure `json:"failures"`
}

// --- Response DTOs ---

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
	ID               uuid.UUID        `json:"id"`
	ContractID       uuid.UUID        `json:"contract_id"`
	RoomID           uuid.UUID        `json:"room_id"`
	RoomNumber       string           `json:"room_number"`
	RoomFloor        int              `json:"room_floor"`
	TenantName       string           `json:"tenant_name"`
	ResultType       string           `json:"result_type"`
	ReasonCode       string           `json:"reason_code,omitempty"`
	ReasonText       string           `json:"reason_text,omitempty"`
	BillID           *uuid.UUID       `json:"bill_id,omitempty"`
	ComputedSnapshot *SnapshotPreview `json:"computed_snapshot,omitempty"`
	BillStatus       *string          `json:"bill_status,omitempty"`
	IsEdited         bool             `json:"is_edited"`
}

// SnapshotPreview is the API-facing version of billing.ComputedSnapshot.
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

func ToCommitBatchResponse(r *billing.CommitBatchResult) CommitBatchResponse {
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

func toBatchSummary(b *billing.BillGenerationBatch) BatchSummaryResponse {
	return BatchSummaryResponse{
		TotalContracts:     b.TotalContracts,
		Created:            b.CreatedCount,
		AlreadyExistsCount: b.AlreadyExistsCount,
		Skipped:            b.SkippedCount,
		Failed:             b.FailedCount,
	}
}

func ToBatchTriggerResponse(b *billing.BillGenerationBatch) BatchTriggerResponse {
	return BatchTriggerResponse{
		BatchID:      b.ID,
		Status:       string(b.Status),
		BillingMonth: b.BillingMonth,
		ApartmentID:  b.ApartmentID,
		Summary:      toBatchSummary(b),
		CreatedAt:    b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToBatchHeaderResponse(b *billing.BillGenerationBatch) BatchHeaderResponse {
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

func ToBatchItemResponse(i billing.BatchItemWithTenant) BatchItemResponse {
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
		IsEdited:   i.IsEdited,
	}
	if i.BillStatus != nil {
		s := string(*i.BillStatus)
		resp.BillStatus = &s
	}
	if i.ResultType == billing.ResultCreated && len(i.ComputedSnapshot.LineItems) > 0 {
		resp.ComputedSnapshot = toSnapshotPreview(i.ComputedSnapshot)
	}
	return resp
}

func toSnapshotPreview(s billing.ComputedSnapshot) *SnapshotPreview {
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
