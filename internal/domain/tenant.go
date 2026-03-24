package domain

import (
	"time"

	"github.com/google/uuid"
)

type Tenant struct {
	ID               uuid.UUID `json:"id"`
	FullName         string    `json:"full_name"`
	IDCard           string    `json:"id_card"`
	Phone            string    `json:"phone"`
	Address          string    `json:"address"`
	EmergencyContact string    `json:"emergency_contact"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
