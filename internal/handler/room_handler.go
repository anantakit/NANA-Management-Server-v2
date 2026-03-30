package handler

import (
	"nana/internal/dto"
	"nana/internal/service"
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type RoomHandler struct {
	svc service.RoomService
}

func NewRoomHandler(svc service.RoomService) *RoomHandler {
	return &RoomHandler{svc: svc}
}

func (h *RoomHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Get("/:roomId", h.GetByID)
	router.Put("/:roomId", h.Update)
	router.Delete("/:roomId", h.Delete)
}

func (h *RoomHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var params dto.PaginationParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	rooms, total, err := h.svc.ListByApartment(c.Context(), apartmentID, params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := dto.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", dto.ToRoomWithContractResponseList(rooms), meta)
}

func (h *RoomHandler) GetByID(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	room, err := h.svc.GetByID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}
	return respond.Success(c, "สำเร็จ", dto.ToRoomWithContractResponseList([]dto.RoomWithContract{*room})[0])
}

func (h *RoomHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req dto.CreateRoomRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	room, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "สร้างห้องสำเร็จ", dto.ToRoomResponse(*room))
}

func (h *RoomHandler) Update(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	var req dto.UpdateRoomRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	room, err := h.svc.Update(c.Context(), roomID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}
	return respond.Success(c, "อัปเดตห้องสำเร็จ", dto.ToRoomResponse(*room))
}

func (h *RoomHandler) Delete(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	// Verify room belongs to apartment
	room, err := h.svc.GetByID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}

	if err := h.svc.Delete(c.Context(), roomID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบห้องสำเร็จ", nil)
}
