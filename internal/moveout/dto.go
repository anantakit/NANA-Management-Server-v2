package moveout

import "nana/internal/shared/pagination"

// MoveOutListParams holds query parameters for listing move-out notices.
type MoveOutListParams struct {
	pagination.PaginationParams
	Status      string `query:"status"`
	ApartmentID string `query:"apartment_id"`
}

type CreateMoveOutRequest struct {
	ContractID       string `json:"contract_id" validate:"required,uuid"`
	NoticeDate       string `json:"notice_date" validate:"required"`
	ActualMoveOutDate string `json:"actual_move_out_date" validate:"required"`
	Note             string `json:"note"`
}

type UpdateMoveOutRequest struct {
	ActualMoveOutDate *string `json:"actual_move_out_date"`
	Note              *string `json:"note"`
}

type MoveOutResponse struct {
	ID                string  `json:"id"`
	ContractID        string  `json:"contract_id"`
	NoticeDate        string  `json:"notice_date"`
	ActualMoveOutDate string  `json:"actual_move_out_date"`
	Status            string  `json:"status"`
	Note              string  `json:"note"`
	TenantName        string  `json:"tenant_name,omitempty"`
	RoomNumber        string  `json:"room_number,omitempty"`
	ApartmentName     string  `json:"apartment_name,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
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
		ActualMoveOutDate: m.ActualMoveOutDate.Format("2006-01-02"),
		Status:            string(m.Status),
		Note:              m.Note,
		TenantName:        m.TenantName,
		RoomNumber:        m.RoomNumber,
		ApartmentName:     m.ApartmentName,
		CreatedAt:         m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:         m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToMoveOutResponseList(items []MoveOutWithRelations) []MoveOutResponse {
	result := make([]MoveOutResponse, len(items))
	for i, m := range items {
		result[i] = ToMoveOutResponse(m)
	}
	return result
}
