package domain

import (
	"time"

	"github.com/google/uuid"
)

type Tenant struct {
	ID               uuid.UUID `json:"id"`
	FullName         string    `json:"full_name"`
	IDCardNumber     string    `json:"id_card_number"`
	Phone            string    `json:"phone"`
	Email            string    `json:"email"`
	EmergencyContact string    `json:"emergency_contact"`
	EmergencyPhone   string    `json:"emergency_phone"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
