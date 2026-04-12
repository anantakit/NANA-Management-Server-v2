package moveout

import (
	"time"

	"nana/internal/shared/pagination"
)

// MoveOutListParams holds query parameters for listing move-out notices.
type MoveOutListParams struct {
	pagination.PaginationParams
	Status      string `query:"status"`
	ApartmentID string `query:"apartment_id"`
}

type CreateMoveOutRequest struct {
	ContractID       string `json:"contract_id" validate:"required,uuid"`
	NoticeDate       string `json:"notice_date" validate:"required,datetime=2006-01-02"`
	ScheduledMoveOutDate string `json:"scheduled_move_out_date" validate:"required,datetime=2006-01-02"`
	Note             string `json:"note"`
}

type UpdateMoveOutRequest struct {
	ScheduledMoveOutDate *string `json:"scheduled_move_out_date"`
	Note              *string `json:"note"`
}

type MoveOutResponse struct {
	ID                string  `json:"id"`
	ContractID        string  `json:"contract_id"`
	NoticeDate        string  `json:"notice_date"`
	ScheduledMoveOutDate string  `json:"scheduled_move_out_date"`
	Status            string  `json:"status"`
	Note              string  `json:"note"`
	TenantName        string  `json:"tenant_name,omitempty"`
	RoomNumber        string  `json:"room_number,omitempty"`
	ApartmentName     string  `json:"apartment_name,omitempty"`
	Urgency           string  `json:"urgency,omitempty"`
	// DaysUntil: no omitempty — 0 means "today" and must stay on the wire.
	// Pointer would be cleaner but adds nil-checks across the frontend; the
	// queue endpoint is the only consumer that sets it, so emitting 0 on
	// non-queue responses is acceptable noise.
	DaysUntil int `json:"days_until"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// --- Queue DTOs ---

// MoveOutQueueParams holds query parameters for the move-out queue endpoint.
// Scope: "active" (default), "history", or "all".
// ApartmentID is bound as string (matches MoveOutListParams convention) and
// validated/parsed in the service — go-playground/form has no native uuid decoder.
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
	TenantName    string
	RoomNumber    string
	ApartmentName string
}

func ToMoveOutResponse(m MoveOutWithRelations) MoveOutResponse {
	return MoveOutResponse{
		ID:                m.ID.String(),
		ContractID:        m.ContractID.String(),
		NoticeDate:        m.NoticeDate.Format("2006-01-02"),
		ScheduledMoveOutDate: m.ScheduledMoveOutDate.Format("2006-01-02"),
		Status:            string(m.Status),
		Note:              m.Note,
		TenantName:        m.TenantName,
		RoomNumber:        m.RoomNumber,
		ApartmentName:     m.ApartmentName,
		CreatedAt:         m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:         m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ToMoveOutResponseWithQueue enriches a base response with urgency and
// days_until — computed against the supplied "today". Used by the queue
// endpoint where every item carries effective state.
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
