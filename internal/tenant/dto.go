package tenant

type CreateTenantRequest struct {
	FullName         string `json:"full_name" validate:"required,min=1,max=255"`
	IDCard           string `json:"id_card" validate:"required,len=13"`
	Phone            string `json:"phone" validate:"required,min=1,max=20"`
	Address          string `json:"address" validate:"omitempty,max=1000"`
	EmergencyContact string `json:"emergency_contact" validate:"omitempty,max=255"`
}

type UpdateTenantRequest struct {
	FullName         *string `json:"full_name" validate:"omitempty,min=1,max=255"`
	IDCard           *string `json:"id_card" validate:"omitempty,len=13"`
	Phone            *string `json:"phone" validate:"omitempty,min=1,max=20"`
	Address          *string `json:"address" validate:"omitempty,max=1000"`
	EmergencyContact *string `json:"emergency_contact" validate:"omitempty,max=255"`
}

type TenantResponse struct {
	ID               string `json:"id"`
	FullName         string `json:"full_name"`
	IDCard           string `json:"id_card"`
	Phone            string `json:"phone"`
	Address          string `json:"address"`
	EmergencyContact string `json:"emergency_contact"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func ToTenantResponse(t Tenant) TenantResponse {
	return TenantResponse{
		ID:               t.ID.String(),
		FullName:         t.FullName,
		IDCard:           t.IDCard,
		Phone:            t.Phone,
		Address:          t.Address,
		EmergencyContact: t.EmergencyContact,
		CreatedAt:        t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        t.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToTenantResponseList(tenants []Tenant) []TenantResponse {
	result := make([]TenantResponse, len(tenants))
	for i, t := range tenants {
		result[i] = ToTenantResponse(t)
	}
	return result
}
