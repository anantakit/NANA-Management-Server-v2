package meterreading

import (
	"time"

	"nana/internal/shared/pagination"
)

// --- Request DTOs ---

type CreateRequest struct {
	RoomID                     string `json:"room_id" validate:"required,uuid"`
	BillingMonth               string `json:"billing_month" validate:"required"`
	ElectricityCurrent         int    `json:"electricity_current" validate:"min=0"`
	WaterCurrent               int    `json:"water_current" validate:"min=0"`
	IsWaterMeterReplaced       bool   `json:"is_water_meter_replaced"`
	IsElectricityMeterReplaced bool   `json:"is_electricity_meter_replaced"`
	IsWaterMeterRollover       bool   `json:"is_water_meter_rollover"`
	IsElectricityMeterRollover bool   `json:"is_electricity_meter_rollover"`
}

// CreateBaselineCorrectionRequest is the body of POST
// /api/v1/apartments/:apartmentId/meter-readings/baseline-corrections
// (Phase 7).
//
// Phase 7 (Split Meter Truth from Financial Truth, locked 2026-06-25):
// Amount, AdjustmentNote, and ReasonCode REMOVED. Financial intent now
// lives on the bill side (UpdateMonthlyDraft.applied_corrections) where
// the operator decides money in financial context. The recovery row
// itself carries only meter truth.
//
// Lock A: no previous_* fields — service derives prev = current.
// Lock E: no billing_month — server clock derives recoveryMonth.
//
// Source-optional relaxation (locked 2026-07-01): source_reading_id is now
// OPTIONAL (omitempty) — a recovery without a source is valid and complete.
// Lock C is relaxed: when a source is supplied the room is still derived from
// source.RoomID (room_id ignored); when source is absent, room_id carries the
// room anchor the source used to provide. No inference fills a missing source.
type CreateBaselineCorrectionRequest struct {
	SourceReadingID    string `json:"source_reading_id" validate:"omitempty,uuid"`
	RoomID             string `json:"room_id" validate:"omitempty,uuid"`
	ElectricityCurrent int    `json:"electricity_current" validate:"min=0"`
	WaterCurrent       int    `json:"water_current" validate:"min=0"`
	// Q1.6 over-record: the recorded (wrong) value is DERIVED server-side from the
	// source reading's current, not supplied by the operator (the source row is the
	// evidence of how much was mis-recorded). Source-less → re-anchor only, no refund.
	AnchorNote string `json:"anchor_note" validate:"required,min=1"`
}

type ExitCreateRequest struct {
	RoomID                     string `json:"room_id" validate:"required,uuid"`
	ReadingDateActual          string `json:"reading_date_actual" validate:"required"` // YYYY-MM-DD
	ElectricityCurrent         int    `json:"electricity_current" validate:"min=0"`
	WaterCurrent               int    `json:"water_current" validate:"min=0"`
	IsWaterMeterReplaced       bool   `json:"is_water_meter_replaced"`
	IsElectricityMeterReplaced bool   `json:"is_electricity_meter_replaced"`
	IsWaterMeterRollover       bool   `json:"is_water_meter_rollover"`
	IsElectricityMeterRollover bool   `json:"is_electricity_meter_rollover"`
}

type BatchCreateItem struct {
	RoomID                     string `json:"room_id" validate:"required,uuid"`
	ElectricityCurrent         int    `json:"electricity_current" validate:"min=0"`
	WaterCurrent               int    `json:"water_current" validate:"min=0"`
	IsWaterMeterReplaced       bool   `json:"is_water_meter_replaced"`
	IsElectricityMeterReplaced bool   `json:"is_electricity_meter_replaced"`
	IsWaterMeterRollover       bool   `json:"is_water_meter_rollover"`
	IsElectricityMeterRollover bool   `json:"is_electricity_meter_rollover"`
}

type BatchCreateRequest struct {
	BillingMonth string            `json:"billing_month" validate:"required"`
	Items        []BatchCreateItem `json:"items" validate:"required,min=1,dive"`
}

type UpdateRequest struct {
	ElectricityCurrent         *int  `json:"electricity_current" validate:"omitempty,min=0"`
	WaterCurrent               *int  `json:"water_current" validate:"omitempty,min=0"`
	IsWaterMeterReplaced       *bool `json:"is_water_meter_replaced"`
	IsElectricityMeterReplaced *bool `json:"is_electricity_meter_replaced"`
	IsWaterMeterRollover       *bool `json:"is_water_meter_rollover"`
	IsElectricityMeterRollover *bool `json:"is_electricity_meter_rollover"`
}

// --- Helpers ---

func (r CreateRequest) ReplacedFlags() MeterReplacedFlags {
	return MeterReplacedFlags{
		Water:       r.IsWaterMeterReplaced,
		Electricity: r.IsElectricityMeterReplaced,
	}
}

func (r CreateRequest) RolloverFlags() MeterRolloverFlags {
	return MeterRolloverFlags{
		Water:       r.IsWaterMeterRollover,
		Electricity: r.IsElectricityMeterRollover,
	}
}

func (r ExitCreateRequest) ReplacedFlags() MeterReplacedFlags {
	return MeterReplacedFlags{
		Water:       r.IsWaterMeterReplaced,
		Electricity: r.IsElectricityMeterReplaced,
	}
}

func (r ExitCreateRequest) RolloverFlags() MeterRolloverFlags {
	return MeterRolloverFlags{
		Water:       r.IsWaterMeterRollover,
		Electricity: r.IsElectricityMeterRollover,
	}
}

func (r BatchCreateItem) ReplacedFlags() MeterReplacedFlags {
	return MeterReplacedFlags{
		Water:       r.IsWaterMeterReplaced,
		Electricity: r.IsElectricityMeterReplaced,
	}
}

func (r BatchCreateItem) RolloverFlags() MeterRolloverFlags {
	return MeterRolloverFlags{
		Water:       r.IsWaterMeterRollover,
		Electricity: r.IsElectricityMeterRollover,
	}
}

func (r UpdateRequest) ReplacedFlags() MeterReplacedFlags {
	return MeterReplacedFlags{
		Water:       r.IsWaterMeterReplaced != nil && *r.IsWaterMeterReplaced,
		Electricity: r.IsElectricityMeterReplaced != nil && *r.IsElectricityMeterReplaced,
	}
}

func (r UpdateRequest) RolloverFlags() MeterRolloverFlags {
	return MeterRolloverFlags{
		Water:       r.IsWaterMeterRollover != nil && *r.IsWaterMeterRollover,
		Electricity: r.IsElectricityMeterRollover != nil && *r.IsElectricityMeterRollover,
	}
}

// --- Query params ---

type ListParams struct {
	pagination.PaginationParams
	Month       string `query:"month" validate:"omitempty"`                           // format: YYYY-MM
	ReadingType string `query:"reading_type" validate:"omitempty,oneof=MONTHLY EXIT"` // MONTHLY or EXIT
}

// --- Replace Meter (PHYSICAL_REPLACEMENT event) request ---

// ReplacementUtilityRequest is the per-utility input for a meter replacement.
// Replaced=false → utility untouched. OldFinal optional (nil = old meter
// unreadable → tail 0). NewInitial required when Replaced (validated in service).
type ReplacementUtilityRequest struct {
	Replaced   bool `json:"replaced"`
	OldFinal   *int `json:"old_final"`
	NewInitial *int `json:"new_initial"`
}

func (r ReplacementUtilityRequest) toInput() (ReplacementUtilityInput, error) {
	if r.Replaced && r.NewInitial == nil {
		return ReplacementUtilityInput{}, ErrReplacementNewInitialRequired
	}
	ni := 0
	if r.NewInitial != nil {
		ni = *r.NewInitial
	}
	return ReplacementUtilityInput{Replaced: r.Replaced, OldFinal: r.OldFinal, NewInitial: ni}, nil
}

// CreateReplacementRequest records a physical meter replacement (admin op),
// decoupled from a monthly/exit reading. replacement_date is the swap date; its
// month is the billing month the tail is attributed to.
type CreateReplacementRequest struct {
	RoomID          string                    `json:"room_id" validate:"required,uuid4"`
	ReplacementDate string                    `json:"replacement_date" validate:"required"`
	Note            string                    `json:"note" validate:"required,min=1"`
	Electricity     ReplacementUtilityRequest `json:"electricity"`
	Water           ReplacementUtilityRequest `json:"water"`
}

// --- Response DTOs ---

type MeterReadingResponse struct {
	ID                    string  `json:"id"`
	RoomID                string  `json:"room_id"`
	RoomNumber            string  `json:"room_number"`
	Floor                 int     `json:"floor"`
	ReadingType           string  `json:"reading_type"`
	BillingMonth          *string `json:"billing_month"`
	ReadingDateActual     *string `json:"reading_date_actual"`
	ElectricityPrevious   int     `json:"electricity_previous"`
	ElectricityCurrent    int     `json:"electricity_current"`
	ElectricityUsed       int     `json:"electricity_used"`
	WaterPrevious         int     `json:"water_previous"`
	WaterCurrent          int     `json:"water_current"`
	WaterUsed             int     `json:"water_used"`
	IsRolloverElectricity bool    `json:"is_rollover_electricity"`
	IsRolloverWater       bool    `json:"is_rollover_water"`
	IsAnomalyElectricity  bool    `json:"is_anomaly_electricity"`
	IsAnomalyWater        bool    `json:"is_anomaly_water"`
	TenantName            string  `json:"tenant_name"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	// Phase 6 — Reading Recovery anchor surface.
	// Populated only on anchor rows (READING_RECOVERY / PHYSICAL_REPLACEMENT /
	// FIRST_ANCHOR); omitempty drops them on normal MONTHLY readings.
	// RecoverySourceReadingID is the FK back to the source meter row (Phase 1
	// schema) — FE uses it to render the source-link in History Drawer +
	// BillDrawer ADJUSTMENT line affordance.
	AnchorReason            *string `json:"anchor_reason,omitempty"`
	AnchorNote              *string `json:"anchor_note,omitempty"`
	RecoverySourceReadingID *string `json:"recovery_source_reading_id,omitempty"`

	// Replace Meter (PHYSICAL_REPLACEMENT event) — omitempty drops on ordinary rows.
	ElectricityReplaced    bool `json:"electricity_replaced,omitempty"`
	WaterReplaced          bool `json:"water_replaced,omitempty"`
	ElectricityOldPrevious *int `json:"electricity_old_previous,omitempty"`
	ElectricityOldFinal    *int `json:"electricity_old_final,omitempty"`
	WaterOldPrevious       *int `json:"water_old_previous,omitempty"`
	WaterOldFinal          *int `json:"water_old_final,omitempty"`
}

// MeterReadingWithRoom holds joined data from meter_readings + rooms + tenants.
type MeterReadingWithRoom struct {
	MeterReading
	RoomNumber string
	Floor      int
	TenantName string
}

func formatDatePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// hydrateAnchorFields copies Phase 1 anchor fields from the GORM model into the
// DTO pointer slots. anchor_reason / anchor_note / recovery_source_reading_id
// are pointer-typed in MeterReadingResponse + RoomHistoryItem so they marshal
// via omitempty on non-anchor rows. The shared helper keeps both mappers
// (ToMeterReadingResponse + ToRoomHistoryItem) byte-aligned on Phase 6's
// surface contract.
func hydrateAnchorFields(reason, note, sourceID **string, m MeterReading) {
	if m.AnchorReason != nil {
		ar := string(*m.AnchorReason)
		*reason = &ar
	}
	if m.AnchorNote != nil {
		an := *m.AnchorNote
		*note = &an
	}
	if m.RecoverySourceReadingID != nil {
		sid := m.RecoverySourceReadingID.String()
		*sourceID = &sid
	}
}

func ToMeterReadingResponse(m MeterReadingWithRoom) MeterReadingResponse {
	resp := MeterReadingResponse{
		ID:                    m.ID.String(),
		RoomID:                m.RoomID.String(),
		RoomNumber:            m.RoomNumber,
		Floor:                 m.Floor,
		ReadingType:           string(m.ReadingType),
		BillingMonth:          m.BillingMonth,
		ReadingDateActual:     formatDatePtr(m.ReadingDateActual),
		ElectricityPrevious:   m.ElectricityPrevious,
		ElectricityCurrent:    m.ElectricityCurrent,
		ElectricityUsed:       m.ElectricityUsed(),
		WaterPrevious:         m.WaterPrevious,
		WaterCurrent:          m.WaterCurrent,
		WaterUsed:             m.WaterUsed(),
		IsRolloverElectricity: m.IsRolloverElectricity,
		IsRolloverWater:       m.IsRolloverWater,
		IsAnomalyElectricity:  m.IsAnomalyElectricity,
		IsAnomalyWater:        m.IsAnomalyWater,
		TenantName:            m.TenantName,
		CreatedAt:             m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:             m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	hydrateAnchorFields(&resp.AnchorReason, &resp.AnchorNote, &resp.RecoverySourceReadingID, m.MeterReading)
	resp.ElectricityReplaced = m.ElectricityReplaced
	resp.WaterReplaced = m.WaterReplaced
	resp.ElectricityOldPrevious, resp.ElectricityOldFinal = m.ElectricityOldPrevious, m.ElectricityOldFinal
	resp.WaterOldPrevious, resp.WaterOldFinal = m.WaterOldPrevious, m.WaterOldFinal
	return resp
}

// ToRecoveryResponse adapts the Phase 5 service return shape (bare
// *MeterReading, NOT MeterReadingWithRoom — the service intentionally returns
// the persisted row without re-JOINing room/tenant context per
// service_recovery.go:150). Room/floor/tenant fields are left empty; FE
// already has them from the source row that triggered the recovery and
// invalidates roomHistory so the next render pulls full context.
func ToRecoveryResponse(m *MeterReading) MeterReadingResponse {
	resp := MeterReadingResponse{
		ID:                    m.ID.String(),
		RoomID:                m.RoomID.String(),
		ReadingType:           string(m.ReadingType),
		BillingMonth:          m.BillingMonth,
		ReadingDateActual:     formatDatePtr(m.ReadingDateActual),
		ElectricityPrevious:   m.ElectricityPrevious,
		ElectricityCurrent:    m.ElectricityCurrent,
		ElectricityUsed:       m.ElectricityUsed(),
		WaterPrevious:         m.WaterPrevious,
		WaterCurrent:          m.WaterCurrent,
		WaterUsed:             m.WaterUsed(),
		IsRolloverElectricity: m.IsRolloverElectricity,
		IsRolloverWater:       m.IsRolloverWater,
		IsAnomalyElectricity:  m.IsAnomalyElectricity,
		IsAnomalyWater:        m.IsAnomalyWater,
		CreatedAt:             m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:             m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	hydrateAnchorFields(&resp.AnchorReason, &resp.AnchorNote, &resp.RecoverySourceReadingID, *m)
	return resp
}

func ToMeterReadingResponseList(readings []MeterReadingWithRoom) []MeterReadingResponse {
	result := make([]MeterReadingResponse, len(readings))
	for i, r := range readings {
		result[i] = ToMeterReadingResponse(r)
	}
	return result
}

// --- Room History Response ---

type RoomHistoryItem struct {
	ID                    string  `json:"id"`
	ReadingType           string  `json:"reading_type"`
	BillingMonth          *string `json:"billing_month"`
	ReadingDateActual     *string `json:"reading_date_actual"`
	ElectricityPrevious   int     `json:"electricity_previous"`
	ElectricityCurrent    int     `json:"electricity_current"`
	ElectricityUsed       int     `json:"electricity_used"`
	WaterPrevious         int     `json:"water_previous"`
	WaterCurrent          int     `json:"water_current"`
	WaterUsed             int     `json:"water_used"`
	IsRolloverElectricity bool    `json:"is_rollover_electricity"`
	IsRolloverWater       bool    `json:"is_rollover_water"`
	IsAnomalyElectricity  bool    `json:"is_anomaly_electricity"`
	IsAnomalyWater        bool    `json:"is_anomaly_water"`
	IsEdited              bool    `json:"is_edited"`
	TenantName            string  `json:"tenant_name"`
	ContractStartDate     string  `json:"contract_start_date"`
	IsCurrentTenant       bool    `json:"is_current_tenant"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	// Phase 6 — same anchor surface as MeterReadingResponse. omitempty drops
	// these on non-anchor rows. FE uses these to render Phase 1 anchor badges
	// + the recovery-source link in History Drawer.
	AnchorReason            *string `json:"anchor_reason,omitempty"`
	AnchorNote              *string `json:"anchor_note,omitempty"`
	RecoverySourceReadingID *string `json:"recovery_source_reading_id,omitempty"`
}

// MeterReadingWithTenant holds a reading enriched with tenant context from contract data.
type MeterReadingWithTenant struct {
	MeterReading
	TenantName        string
	ContractStartDate time.Time
	IsCurrentTenant   bool
}

func ToRoomHistoryItem(m MeterReadingWithTenant) RoomHistoryItem {
	contractStart := ""
	if !m.ContractStartDate.IsZero() {
		contractStart = m.ContractStartDate.Format("2006-01-02")
	}
	resp := RoomHistoryItem{
		ID:                    m.ID.String(),
		ReadingType:           string(m.ReadingType),
		BillingMonth:          m.BillingMonth,
		ReadingDateActual:     formatDatePtr(m.ReadingDateActual),
		ElectricityPrevious:   m.ElectricityPrevious,
		ElectricityCurrent:    m.ElectricityCurrent,
		ElectricityUsed:       m.ElectricityUsed(),
		WaterPrevious:         m.WaterPrevious,
		WaterCurrent:          m.WaterCurrent,
		WaterUsed:             m.WaterUsed(),
		IsRolloverElectricity: m.IsRolloverElectricity,
		IsRolloverWater:       m.IsRolloverWater,
		IsAnomalyElectricity:  m.IsAnomalyElectricity,
		IsAnomalyWater:        m.IsAnomalyWater,
		IsEdited:              isEdited(m.CreatedAt, m.UpdatedAt),
		TenantName:            m.TenantName,
		ContractStartDate:     contractStart,
		IsCurrentTenant:       m.IsCurrentTenant,
		CreatedAt:             m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:             m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	hydrateAnchorFields(&resp.AnchorReason, &resp.AnchorNote, &resp.RecoverySourceReadingID, m.MeterReading)
	return resp
}

func ToRoomHistoryItemList(readings []MeterReadingWithTenant) []RoomHistoryItem {
	result := make([]RoomHistoryItem, len(readings))
	for i, r := range readings {
		result[i] = ToRoomHistoryItem(r)
	}
	return result
}

// isEdited returns true if updated_at is more than 1 second after created_at.
// 1-second tolerance avoids false positives from GORM auto-timestamp.
func isEdited(createdAt, updatedAt time.Time) bool {
	return updatedAt.After(createdAt.Add(1 * time.Second))
}

type BatchCreateResponse struct {
	Created int                    `json:"created"`
	Items   []MeterReadingResponse `json:"items"`
}

type LatestReadingResponse struct {
	ReadingType        string  `json:"reading_type"`
	ElectricityCurrent int     `json:"electricity_current"`
	WaterCurrent       int     `json:"water_current"`
	BillingMonth       *string `json:"billing_month"`
	ReadingDateActual  *string `json:"reading_date_actual"`
}

type RoomBaselineResponse struct {
	RoomID                   string `json:"room_id"`
	ElectricityBaseline      int    `json:"electricity_baseline"`
	WaterBaseline            int    `json:"water_baseline"`
	ElectricityHasEnoughData bool   `json:"electricity_has_enough_data"`
	WaterHasEnoughData       bool   `json:"water_has_enough_data"`
}
