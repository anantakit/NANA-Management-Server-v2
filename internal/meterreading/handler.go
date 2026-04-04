package meterreading

import (
	"sort"

	"nana/internal/shared/bind"
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
	return respond.Created(c, "บันทึกมิเตอร์สำเร็จ", ToMeterReadingResponse(*reading))
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
	return respond.Created(c, "บันทึกมิเตอร์ย้ายออกสำเร็จ", ToMeterReadingResponse(*reading))
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
	return respond.Created(c, "บันทึกมิเตอร์สำเร็จ", resp)
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
	return respond.Success(c, "อัปเดตมิเตอร์สำเร็จ", ToMeterReadingResponse(*reading))
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
