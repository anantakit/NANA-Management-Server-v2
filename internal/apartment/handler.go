package apartment

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type ApartmentHandler struct {
	svc ApartmentService
}

func NewApartmentHandler(svc ApartmentService) *ApartmentHandler {
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
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", apartments)
}

func (h *ApartmentHandler) GetByID(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	apt, err := h.svc.GetByID(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", apt)
}

func (h *ApartmentHandler) Create(c fiber.Ctx) error {
	var req CreateApartmentRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	apt, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "สร้างอาคารแล้ว", ToApartmentResponse(*apt))
}

func (h *ApartmentHandler) Update(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req UpdateApartmentRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	apt, err := h.svc.Update(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตอาคารแล้ว", ToApartmentResponse(*apt))
}
