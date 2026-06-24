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

// CreateRecoveryRequest is the body of POST
// /api/v1/apartments/:apartmentId/meter-readings/recovery (Phase 6).
//
// Lock A: no previous_* fields — service derives prev = current.
// Lock C: no room_id — derived from source.RoomID.
// Lock E: no billing_month — server clock derives recoveryMonth.
//
// Lock B (operator-authoritative Amount): the FE may render a "ประมาณ" hint
// alongside this input (calculator helper), but MUST NEVER prefill it. The
// server stores Amount as-is. AdjustmentNote (≥10 chars, ValidateAdjustment)
// is the primary forensic signal — see feedback_reading_recovery_doctrine.md
// line 38.
type CreateRecoveryRequest struct {
	SourceReadingID    string  `json:"source_reading_id" validate:"required,uuid"`
	ElectricityCurrent int     `json:"electricity_current" validate:"min=0"`
	WaterCurrent       int     `json:"water_current" validate:"min=0"`
	Amount             float64 `json:"amount"`
	ReasonCode         string  `json:"reason_code" validate:"required,oneof=METER_RECOVERY"`
	AnchorNote         string  `json:"anchor_note" validate:"required,min=1"`
	AdjustmentNote     string  `json:"adjustment_note" validate:"required,min=10"`
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
	Month       string `query:"month" validate:"omitempty"`                          // format: YYYY-MM
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

type RoomBaselineResponse struct {
	RoomID                   string `json:"room_id"`
	ElectricityBaseline      int    `json:"electricity_baseline"`
	WaterBaseline            int    `json:"water_baseline"`
	ElectricityHasEnoughData bool   `json:"electricity_has_enough_data"`
	WaterHasEnoughData       bool   `json:"water_has_enough_data"`
}
