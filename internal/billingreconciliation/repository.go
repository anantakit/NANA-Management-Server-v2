package billingreconciliation

import (
	"context"
	"time"

	"nana/internal/contract"
	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Repository is the read-only port for the reconciliation workspace. The new
// package owns this endpoint, so it owns the JOIN — same display-read pattern
// as billing/repository.go's FindActiveContractsByApartmentID, but LEFT JOIN
// off rooms so vacant + maintenance + no-active-contract rooms still surface.
type Repository interface {
	ListRoomCandidatesByApartment(ctx context.Context, apartmentID uuid.UUID) ([]RoomCandidate, error)
}

type repository struct {
	db *gorm.DB
}

var _ Repository = (*repository)(nil)

func NewRepository(db *gorm.DB) Repository {
	return &repository{db: db}
}

// ListRoomCandidatesByApartment returns every (soft-delete-free) room of an
// apartment with its current active contract + tenant attached when present.
// LEFT JOIN keeps vacant/maintenance rooms in the result set — required for
// the NOT_BILLABLE bucket to be honest about everything that did not bill.
//
// Cross-feature display read — encodes contract status as a level-1 domain
// constant (cross-feature-patterns.md rule). Tenant join is plain display.
func (r *repository) ListRoomCandidatesByApartment(ctx context.Context, apartmentID uuid.UUID) ([]RoomCandidate, error) {
	type joinRow struct {
		RoomID     uuid.UUID  `gorm:"column:room_id"`
		RoomNumber string     `gorm:"column:room_number"`
		RoomFloor  int        `gorm:"column:room_floor"`
		RoomStatus string     `gorm:"column:room_status"`
		ContractID *uuid.UUID `gorm:"column:contract_id"`
		StartDate  *time.Time `gorm:"column:start_date"`
		TenantName *string    `gorm:"column:tenant_name"`
	}

	var rows []joinRow
	err := database.DB(ctx, r.db).
		Table("rooms r").
		Select(`r.id AS room_id,
			r.number AS room_number,
			r.floor AS room_floor,
			r.status AS room_status,
			c.id AS contract_id,
			c.start_date AS start_date,
			t.full_name AS tenant_name`).
		Joins("LEFT JOIN contracts c ON c.room_id = r.id AND c.status = ? AND c.deleted_at IS NULL", contract.ContractStatusActive).
		Joins("LEFT JOIN tenants t ON t.id = c.tenant_id AND t.deleted_at IS NULL").
		Where("r.apartment_id = ? AND r.deleted_at IS NULL", apartmentID).
		Order("r.floor ASC, r.number ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make([]RoomCandidate, len(rows))
	for i, row := range rows {
		c := RoomCandidate{
			RoomID:            row.RoomID,
			RoomNumber:        row.RoomNumber,
			RoomFloor:         row.RoomFloor,
			RoomStatus:        row.RoomStatus,
			ContractID:        row.ContractID,
			ContractStartDate: row.StartDate,
		}
		if row.TenantName != nil {
			c.TenantName = *row.TenantName
		}
		result[i] = c
	}
	return result, nil
}
