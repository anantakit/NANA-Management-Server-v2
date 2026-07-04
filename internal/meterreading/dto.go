package meterreading

import (
	"time"

	"nana/internal/shared/pagination"

	"github.com/google/uuid"
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
	// Q1.5 over-record: the previously-recorded (wrong) value per utility being
	// corrected. Optional + independent — nil means that utility is not part of
	// this correction. Must be >= the matching current (ValidateAnchor + DB CHECK;
	// recorded < current is an out-of-scope under-record).
	ElectricityRecorded *int   `json:"electricity_recorded" validate:"omitempty,min=0"`
	WaterRecorded       *int   `json:"water_recorded" validate:"omitempty,min=0"`
	AnchorNote          string `json:"anchor_note" validate:"required,min=1"`
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

// PendingBaselineCorrection is the internal service-shape for a pending
// READING_RECOVERY anchor row. Travels across the meterreading → billing
// boundary (the billing handler resolves bill → room and forwards). Public
// JSON shape is PendingBaselineCorrectionResponse.
//
// Sort contract (locked by ListPendingBaselineCorrectionsByRoom): newest-first
// (created_at DESC, source_billing_month DESC). Bill edit is action surface,
// not narration — most recent correction surfaces first so the operator's
// "I just made this, apply it" flow reads top-down.
type PendingBaselineCorrection struct {
	RecoveryID           uuid.UUID
	SourceReadingID      uuid.UUID
	SourceBillingMonth   string
	SourceElectricity    int
	SourceWater          int
	RecoveryBillingMonth string
	RecoveryElectricity  int
	RecoveryWater        int
	RecoveryCreatedAt    time.Time
	AnchorNote           string

	// Q1.5 over-record meter facts (per utility). Physical = Recovery{Electricity,
	// Water}. Recorded = the previously-recorded (wrong) value; 0 when the utility
	// was not corrected. Affected = recorded > physical (pure meter fact — the
	// money recommendation is derived by billing from the bill's contract rate).
	ElectricityRecorded int
	ElectricityAffected bool
	WaterRecorded       int
	WaterAffected       bool
}

// PendingBaselineCorrectionResponse is the public JSON shape returned by
// GET /pending-baseline-corrections. Field names mirror the FE type in
// frontend/src/features/bills/types.ts.
type PendingBaselineCorrectionResponse struct {
	RecoveryID           string `json:"recovery_id"`
	SourceReadingID      string `json:"source_reading_id"`
	SourceBillingMonth   string `json:"source_billing_month"`
	SourceElectricity    int    `json:"source_electricity_current"`
	SourceWater          int    `json:"source_water_current"`
	RecoveryBillingMonth string `json:"recovery_billing_month"`
	RecoveryElectricity  int    `json:"recovery_electricity_current"`
	RecoveryWater        int    `json:"recovery_water_current"`
	RecoveryCreatedAt    string `json:"recovery_created_at"`
	AnchorNote           string `json:"anchor_note"`

	// Q1.5 over-record meter facts. recovery_*_current above is the physical
	// value; *_recorded is the previously-recorded (wrong) value (0 = utility not
	// corrected); *_affected = recorded > physical.
	ElectricityRecorded int  `json:"electricity_recorded"`
	ElectricityAffected bool `json:"electricity_affected"`
	WaterRecorded       int  `json:"water_recorded"`
	WaterAffected       bool `json:"water_affected"`
}

func ToPendingBaselineCorrectionResponse(p PendingBaselineCorrection) PendingBaselineCorrectionResponse {
	// Source-optional (locked 2026-07-01): a nil-source recovery carries a
	// zero SourceReadingID. Guard the zero UUID — uuid.Nil.String() yields
	// "00000000-..." (non-empty), so emit "" instead. Consumers key the
	// source block's presence on source_billing_month (see FE §2.5).
	sourceReadingID := ""
	if p.SourceReadingID != uuid.Nil {
		sourceReadingID = p.SourceReadingID.String()
	}
	return PendingBaselineCorrectionResponse{
		RecoveryID:           p.RecoveryID.String(),
		SourceReadingID:      sourceReadingID,
		SourceBillingMonth:   p.SourceBillingMonth,
		SourceElectricity:    p.SourceElectricity,
		SourceWater:          p.SourceWater,
		RecoveryBillingMonth: p.RecoveryBillingMonth,
		RecoveryElectricity:  p.RecoveryElectricity,
		RecoveryWater:        p.RecoveryWater,
		RecoveryCreatedAt:    p.RecoveryCreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		AnchorNote:           p.AnchorNote,
		ElectricityRecorded:  p.ElectricityRecorded,
		ElectricityAffected:  p.ElectricityAffected,
		WaterRecorded:        p.WaterRecorded,
		WaterAffected:        p.WaterAffected,
	}
}

func ToPendingBaselineCorrectionResponseList(rows []PendingBaselineCorrection) []PendingBaselineCorrectionResponse {
	out := make([]PendingBaselineCorrectionResponse, len(rows))
	for i, r := range rows {
		out[i] = ToPendingBaselineCorrectionResponse(r)
	}
	return out
}

type RoomBaselineResponse struct {
	RoomID                   string `json:"room_id"`
	ElectricityBaseline      int    `json:"electricity_baseline"`
	WaterBaseline            int    `json:"water_baseline"`
	ElectricityHasEnoughData bool   `json:"electricity_has_enough_data"`
	WaterHasEnoughData       bool   `json:"water_has_enough_data"`
}
