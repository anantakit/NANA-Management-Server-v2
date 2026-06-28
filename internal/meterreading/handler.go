package meterreading

import (
	"sort"

	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type MeterReadingHandler struct {
	svc MeterReadingService
}

func NewMeterReadingHandler(svc MeterReadingService) *MeterReadingHandler {
	return &MeterReadingHandler{svc: svc}
}

func (h *MeterReadingHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Get("/baselines", h.GetBaselines)
	router.Post("/", h.Create)
	router.Post("/exit", h.CreateExitReading)
	router.Post("/batch", h.BatchCreate)
	// Phase 7 baseline correction — meter-only commit (Adjustment
	// Application moved to BillEditDrawer / UpdateMonthlyDraft).
	// Static segments registered BEFORE "/:readingId" so Fiber v3's
	// radix match prefers them.
	router.Post("/baseline-corrections", h.CreateBaselineCorrection)
	router.Delete("/rooms/:roomId/baseline-corrections/:correctionId", h.DeleteBaselineCorrection)
	router.Get("/rooms/:roomId/pending-baseline-corrections", h.ListPendingBaselineCorrections)
	router.Get("/rooms/:roomId/latest", h.GetLatest)
	router.Get("/rooms/:roomId/history", h.GetRoomHistory)
	router.Get("/:readingId", h.GetByID)
	router.Put("/:readingId", h.Update)
}

func (h *MeterReadingHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var params ListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	readings, total, err := h.svc.List(c.Context(), apartmentID, params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", ToMeterReadingResponseList(readings), meta)
}

func (h *MeterReadingHandler) GetByID(c fiber.Ctx) error {
	_, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	readingID, err := uuid.Parse(c.Params("readingId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสข้อมูลมิเตอร์ไม่ถูกต้อง"})
	}

	reading, err := h.svc.GetByID(c.Context(), readingID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToMeterReadingResponse(*reading))
}

func (h *MeterReadingHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req CreateRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	reading, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "บันทึกมิเตอร์แล้ว", ToMeterReadingResponse(*reading))
}

func (h *MeterReadingHandler) CreateExitReading(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req ExitCreateRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	reading, err := h.svc.CreateExitReading(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "บันทึกมิเตอร์ย้ายออกแล้ว", ToMeterReadingResponse(*reading))
}

func (h *MeterReadingHandler) BatchCreate(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req BatchCreateRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	readings, err := h.svc.BatchCreate(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}

	resp := BatchCreateResponse{
		Created: len(readings),
		Items:   ToMeterReadingResponseList(readings),
	}
	return respond.Created(c, "บันทึกมิเตอร์แล้ว", resp)
}

func (h *MeterReadingHandler) Update(c fiber.Ctx) error {
	_, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	readingID, err := uuid.Parse(c.Params("readingId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสข้อมูลมิเตอร์ไม่ถูกต้อง"})
	}

	var req UpdateRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	reading, err := h.svc.Update(c.Context(), readingID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตมิเตอร์แล้ว", ToMeterReadingResponse(*reading))
}

func (h *MeterReadingHandler) GetBaselines(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	baselines, err := h.svc.GetBaselines(c.Context(), apartmentID)
	if err != nil {
		return respond.Error(c, err)
	}

	result := make([]RoomBaselineResponse, 0, len(baselines))
	for roomID, bl := range baselines {
		result = append(result, RoomBaselineResponse{
			RoomID:                   roomID.String(),
			ElectricityBaseline:      bl.ElectricityBaseline,
			WaterBaseline:            bl.WaterBaseline,
			ElectricityHasEnoughData: bl.ElectricityHasEnoughData,
			WaterHasEnoughData:       bl.WaterHasEnoughData,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].RoomID < result[j].RoomID
	})
	return respond.Success(c, "สำเร็จ", result)
}

func (h *MeterReadingHandler) GetRoomHistory(c fiber.Ctx) error {
	_, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	var params pagination.PaginationParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	readings, total, err := h.svc.GetRoomHistory(c.Context(), roomID, params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", ToRoomHistoryItemList(readings), meta)
}

// CreateBaselineCorrection commits a baseline correction (Phase 7 — meter-only).
//
// Phase 7 (Split Meter Truth from Financial Truth, locked 2026-06-25):
// the body carries only meter truth — Amount / AdjustmentNote / ReasonCode
// moved to the bill side (UpdateMonthlyDraft.applied_corrections).
//
// The URL apartmentId is validated for routing scope only — the service
// derives roomID from source.RoomID (Lock C). billing_month is NOT in the
// body (Lock E — server-clock derived). previous_* fields are NOT
// accepted (Lock A — service sets prev = curr).
//
// Errors map via respond.Error / MapToHTTP:
//   - ErrBaselineCorrectionSettlementBoundaryCrossed → 409 with typed Thai message
//   - validation errors                    → 400
//   - source-not-found                     → 404
func (h *MeterReadingHandler) CreateBaselineCorrection(c fiber.Ctx) error {
	if _, err := uuid.Parse(c.Params("apartmentId")); err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req CreateBaselineCorrectionRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	sourceID, err := uuid.Parse(req.SourceReadingID)
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสมิเตอร์ต้นทางไม่ถูกต้อง"})
	}

	input := CreateBaselineCorrectionInput{
		SourceReadingID:    sourceID,
		ElectricityCurrent: req.ElectricityCurrent,
		WaterCurrent:       req.WaterCurrent,
		AnchorNote:         req.AnchorNote,
		ActorID:            middleware.ActorFromCtx(c),
	}

	reading, err := h.svc.CreateBaselineCorrection(c.Context(), input)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "บันทึกค่ามิเตอร์ที่ถูกต้องแล้ว", ToRecoveryResponse(reading))
}

// DeleteBaselineCorrection soft-deletes a pending baseline correction.
// Apartment + room scopes appear in the URL so the service's ownership
// invariants are visible at the route level. Service emits typed
// ErrCorrectionAlreadyApplied / ErrCorrectionNotLatest / 404; the
// centralized respond.Error path humanizes them.
func (h *MeterReadingHandler) DeleteBaselineCorrection(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}
	correctionID, err := uuid.Parse(c.Params("correctionId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสรายการปรับฐานไม่ถูกต้อง"})
	}

	if err := h.svc.SoftDeletePendingBaselineCorrection(c.Context(), apartmentID, roomID, correctionID, middleware.ActorFromCtx(c)); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบการปรับฐานแล้ว", nil)
}

// ListPendingBaselineCorrections returns the room's pending baseline
// corrections (applied rows excluded). Room-centric per Phase 7 doctrine
// line 121 — pending state is a property of (room, time), not of any
// specific bill.
func (h *MeterReadingHandler) ListPendingBaselineCorrections(c fiber.Ctx) error {
	if _, err := uuid.Parse(c.Params("apartmentId")); err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}
	rows, err := h.svc.ListPendingBaselineCorrectionsByRoom(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToPendingBaselineCorrectionResponseList(rows))
}

func (h *MeterReadingHandler) GetLatest(c fiber.Ctx) error {
	_, err := uuid.Parse(c.Params("apartmentId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	reading, err := h.svc.GetLatestByRoomID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}

	resp := LatestReadingResponse{
		ReadingType:        string(reading.ReadingType),
		ElectricityCurrent: reading.ElectricityCurrent,
		WaterCurrent:       reading.WaterCurrent,
		BillingMonth:       reading.BillingMonth,
		ReadingDateActual:  formatDatePtr(reading.ReadingDateActual),
	}
	return respond.Success(c, "สำเร็จ", resp)
}
