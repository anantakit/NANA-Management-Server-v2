package billingreconciliation

import (
	"context"
	"errors"
	"time"

	"nana/internal/contract"
	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository is the read-only-then-decision port for the reconciliation
// workspace. The package owns the JOIN — same display-read pattern as
// billing/repository.go's FindActiveContractsByApartmentID, but LEFT JOIN
// off rooms so vacant + maintenance + no-active-contract rooms still
// surface. Decision write methods (Phase 1B) live alongside because
// decisions are an internal axis of this package (no separate feature).
type Repository interface {
	ListRoomCandidatesByApartment(ctx context.Context, apartmentID uuid.UUID) ([]RoomCandidate, error)

	// FindRoomCandidate fetches one room's candidate state — used by the
	// PUT/DELETE guards before mutating a decision. apartmentID is part of
	// the lookup so the guard rejects cross-apartment writes early.
	FindRoomCandidate(ctx context.Context, apartmentID, roomID uuid.UUID) (*RoomCandidate, error)

	// FindDecisionsForApartmentMonth returns every decision row for one
	// apartment + month, keyed by room_id, with username pre-joined for
	// inline attribution. Empty map (not nil) when no decisions exist.
	FindDecisionsForApartmentMonth(ctx context.Context, apartmentID uuid.UUID, billingMonth string) (map[uuid.UUID]*DecisionAttribution, error)

	// FindDecisionByRoomMonth returns the single decision row or nil when
	// the room has no decision (Undecided). Used by the GET endpoint.
	FindDecisionByRoomMonth(ctx context.Context, roomID uuid.UUID, billingMonth string) (*Decision, *DecisionAttribution, error)

	// UpsertDecision writes / overwrites the decision for (room, month).
	// Per state-grammar lock, the row is the long-term record — `decided_at`
	// + `decided_by` reflect the LATEST mutation (not the first).
	UpsertDecision(ctx context.Context, d *Decision) error

	// DeleteDecision removes the row entirely — absence = Undecided.
	DeleteDecision(ctx context.Context, roomID uuid.UUID, billingMonth string) error
}

var errDecisionRowNotFound = errors.New("decision not found")

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

// FindRoomCandidate is the single-room twin of ListRoomCandidatesByApartment.
// Used by PUT/DELETE guards so the service can re-classify the target room
// without pulling the whole apartment. apartmentID is required so a stray
// room_id from a different apartment is rejected with NOT_FOUND instead of
// leaking decision writes across buildings.
func (r *repository) FindRoomCandidate(ctx context.Context, apartmentID, roomID uuid.UUID) (*RoomCandidate, error) {
	type joinRow struct {
		RoomID     uuid.UUID  `gorm:"column:room_id"`
		RoomNumber string     `gorm:"column:room_number"`
		RoomFloor  int        `gorm:"column:room_floor"`
		RoomStatus string     `gorm:"column:room_status"`
		ContractID *uuid.UUID `gorm:"column:contract_id"`
		StartDate  *time.Time `gorm:"column:start_date"`
		TenantName *string    `gorm:"column:tenant_name"`
	}
	var row joinRow
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
		Where("r.id = ? AND r.apartment_id = ? AND r.deleted_at IS NULL", roomID, apartmentID).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.RoomID == uuid.Nil {
		return nil, nil
	}
	out := &RoomCandidate{
		RoomID:            row.RoomID,
		RoomNumber:        row.RoomNumber,
		RoomFloor:         row.RoomFloor,
		RoomStatus:        row.RoomStatus,
		ContractID:        row.ContractID,
		ContractStartDate: row.StartDate,
	}
	if row.TenantName != nil {
		out.TenantName = *row.TenantName
	}
	return out, nil
}

// FindDecisionsForApartmentMonth bulk-fetches decisions for the read path —
// joined back to rooms so apartment scoping is honest (a decision row whose
// room was later moved to another apartment is filtered out — unlikely but
// the join keeps the contract honest). Username joined directly so the
// reconcile DTO can carry inline attribution without a second round trip.
func (r *repository) FindDecisionsForApartmentMonth(
	ctx context.Context,
	apartmentID uuid.UUID,
	billingMonth string,
) (map[uuid.UUID]*DecisionAttribution, error) {
	type row struct {
		RoomID    uuid.UUID     `gorm:"column:room_id"`
		Decision  DecisionState `gorm:"column:decision"`
		DecidedAt time.Time     `gorm:"column:decided_at"`
		Username  *string       `gorm:"column:username"`
		FullName  *string       `gorm:"column:full_name"`
	}
	var rows []row
	err := database.DB(ctx, r.db).
		Table("room_billing_decisions d").
		Select(`d.room_id   AS room_id,
			d.decision  AS decision,
			d.decided_at AS decided_at,
			u.username  AS username,
			u.full_name AS full_name`).
		Joins("INNER JOIN rooms r ON r.id = d.room_id AND r.apartment_id = ? AND r.deleted_at IS NULL", apartmentID).
		Joins("LEFT JOIN users u ON u.id = d.decided_by AND u.deleted_at IS NULL").
		Where("d.billing_month = ?", billingMonth).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*DecisionAttribution, len(rows))
	for _, row := range rows {
		out[row.RoomID] = &DecisionAttribution{
			State:         row.Decision,
			DecidedAt:     row.DecidedAt,
			DecidedByName: displayName(row.FullName, row.Username),
		}
	}
	return out, nil
}

// FindDecisionByRoomMonth returns (decision, attribution, nil) for GET.
// Returns (nil, nil, nil) — not an error — when the row doesn't exist; the
// caller maps that to HTTP 404 at the service layer.
func (r *repository) FindDecisionByRoomMonth(
	ctx context.Context,
	roomID uuid.UUID,
	billingMonth string,
) (*Decision, *DecisionAttribution, error) {
	type row struct {
		Decision
		Username *string `gorm:"column:username"`
		FullName *string `gorm:"column:full_name"`
	}
	var hit row
	err := database.DB(ctx, r.db).
		Table("room_billing_decisions d").
		Select(`d.*, u.username AS username, u.full_name AS full_name`).
		Joins("LEFT JOIN users u ON u.id = d.decided_by AND u.deleted_at IS NULL").
		Where("d.room_id = ? AND d.billing_month = ?", roomID, billingMonth).
		Limit(1).
		Scan(&hit).Error
	if err != nil {
		return nil, nil, err
	}
	if hit.ID == uuid.Nil {
		return nil, nil, nil
	}
	attr := &DecisionAttribution{
		State:         hit.Decision.Decision,
		DecidedAt:     hit.DecidedAt,
		DecidedByName: displayName(hit.FullName, hit.Username),
	}
	decision := hit.Decision
	return &decision, attr, nil
}

// UpsertDecision relies on the (room_id, billing_month) unique index — the
// only way to avoid a read-then-write race on a constraint that exists
// precisely to enforce "one decision per (room, month)". `decided_at` +
// `decided_by` overwrite on conflict so the row always reflects the LATEST
// mutation.
func (r *repository) UpsertDecision(ctx context.Context, d *Decision) error {
	return database.DB(ctx, r.db).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "room_id"}, {Name: "billing_month"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"decision", "decided_at", "decided_by", "updated_at",
			}),
		}).
		Create(d).Error
}

// DeleteDecision is a hard delete — there is no soft-delete column on the
// table because absence IS the Undecided state (walk-lock storage grammar).
func (r *repository) DeleteDecision(ctx context.Context, roomID uuid.UUID, billingMonth string) error {
	res := database.DB(ctx, r.db).
		Where("room_id = ? AND billing_month = ?", roomID, billingMonth).
		Delete(&Decision{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errDecisionRowNotFound
	}
	return nil
}

// displayName prefers full_name when populated (seed users often have only
// username), falls back to username, returns nil when both are missing or
// the actor was deleted (decided_by → NULL via ON DELETE SET NULL).
func displayName(fullName, username *string) *string {
	if fullName != nil && *fullName != "" {
		v := *fullName
		return &v
	}
	if username != nil && *username != "" {
		v := *username
		return &v
	}
	return nil
}
