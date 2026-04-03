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
	Month string `query:"month" validate:"omitempty"` // format: YYYY-MM
}

// --- Response DTOs ---

type MeterReadingResponse struct {
	ID                   string `json:"id"`
	RoomID               string `json:"room_id"`
	RoomNumber           string `json:"room_number"`
	Floor                int    `json:"floor"`
	BillingMonth         string `json:"billing_month"`
	ElectricityPrevious  int    `json:"electricity_previous"`
	ElectricityCurrent   int    `json:"electricity_current"`
	ElectricityUsed      int    `json:"electricity_used"`
	WaterPrevious        int    `json:"water_previous"`
	WaterCurrent         int    `json:"water_current"`
	WaterUsed            int    `json:"water_used"`
	IsRolloverElectricity bool   `json:"is_rollover_electricity"`
	IsRolloverWater       bool   `json:"is_rollover_water"`
	IsAnomalyElectricity  bool   `json:"is_anomaly_electricity"`
	IsAnomalyWater        bool   `json:"is_anomaly_water"`
	TenantName           string `json:"tenant_name"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

// MeterReadingWithRoom holds joined data from meter_readings + rooms + tenants.
type MeterReadingWithRoom struct {
	MeterReading
	RoomNumber string
	Floor      int
	TenantName string
}

func ToMeterReadingResponse(m MeterReadingWithRoom) MeterReadingResponse {
	return MeterReadingResponse{
		ID:                   m.ID.String(),
		RoomID:               m.RoomID.String(),
		RoomNumber:           m.RoomNumber,
		Floor:                m.Floor,
		BillingMonth:          m.BillingMonth,
		ElectricityPrevious:  m.ElectricityPrevious,
		ElectricityCurrent:   m.ElectricityCurrent,
		ElectricityUsed:      m.ElectricityUsed(),
		WaterPrevious:        m.WaterPrevious,
		WaterCurrent:         m.WaterCurrent,
		WaterUsed:            m.WaterUsed(),
		IsRolloverElectricity: m.IsRolloverElectricity,
		IsRolloverWater:       m.IsRolloverWater,
		IsAnomalyElectricity:  m.IsAnomalyElectricity,
		IsAnomalyWater:        m.IsAnomalyWater,
		TenantName:           m.TenantName,
		CreatedAt:            m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:            m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
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
	ID                    string `json:"id"`
	BillingMonth          string `json:"billing_month"`
	ElectricityPrevious   int    `json:"electricity_previous"`
	ElectricityCurrent    int    `json:"electricity_current"`
	ElectricityUsed       int    `json:"electricity_used"`
	WaterPrevious         int    `json:"water_previous"`
	WaterCurrent          int    `json:"water_current"`
	WaterUsed             int    `json:"water_used"`
	IsRolloverElectricity bool   `json:"is_rollover_electricity"`
	IsRolloverWater       bool   `json:"is_rollover_water"`
	IsAnomalyElectricity  bool   `json:"is_anomaly_electricity"`
	IsAnomalyWater        bool   `json:"is_anomaly_water"`
	IsEdited              bool   `json:"is_edited"`
	TenantName            string `json:"tenant_name"`
	ContractStartDate     string `json:"contract_start_date"`
	IsCurrentTenant       bool   `json:"is_current_tenant"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
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
	return RoomHistoryItem{
		ID:                    m.ID.String(),
		BillingMonth:           m.BillingMonth,
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
	ElectricityCurrent int    `json:"electricity_current"`
	WaterCurrent       int    `json:"water_current"`
	BillingMonth       string `json:"billing_month"`
}

type RoomBaselineResponse struct {
	RoomID                   string `json:"room_id"`
	ElectricityBaseline      int    `json:"electricity_baseline"`
	WaterBaseline            int    `json:"water_baseline"`
	ElectricityHasEnoughData bool   `json:"electricity_has_enough_data"`
	WaterHasEnoughData       bool   `json:"water_has_enough_data"`
}
