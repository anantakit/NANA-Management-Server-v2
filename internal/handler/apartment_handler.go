package handler

import (
	"nana/internal/dto"
	"nana/internal/service"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type ApartmentHandler struct {
	svc service.ApartmentService
}

func NewApartmentHandler(svc service.ApartmentService) *ApartmentHandler {
	return &ApartmentHandler{svc: svc}
}

func (h *ApartmentHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Get("/:id", h.GetByID)
	router.Post("/", h.Create)
	router.Put("/:id", h.Update)
}

func (h *ApartmentHandler) List(c fiber.Ctx) error {
	apartments, err := h.svc.List(c.Context())
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "สำเร็จ", apartments)
}

func (h *ApartmentHandler) GetByID(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	apt, err := h.svc.GetByID(c.Context(), id)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "สำเร็จ", dto.ToApartmentResponse(*apt))
}

func (h *ApartmentHandler) Create(c fiber.Ctx) error {
	var req dto.CreateApartmentRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	apt, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return Error(c, err)
	}
	return Created(c, "สร้างอาคารสำเร็จ", dto.ToApartmentResponse(*apt))
}

func (h *ApartmentHandler) Update(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req dto.UpdateApartmentRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	apt, err := h.svc.Update(c.Context(), id, req)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "อัปเดตอาคารสำเร็จ", dto.ToApartmentResponse(*apt))
}
