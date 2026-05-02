package billingconfig

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type BillingConfigHandler struct {
	svc BillingConfigService
}

func NewBillingConfigHandler(svc BillingConfigService) *BillingConfigHandler {
	return &BillingConfigHandler{svc: svc}
}

func (h *BillingConfigHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Put("/:configId", h.Update)
	router.Delete("/:configId", h.Delete)
}

func (h *BillingConfigHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสอพาร์ทเมนท์ไม่ถูกต้อง"))
	}

	configs, err := h.svc.ListByApartment(c.Context(), apartmentID)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ดึงข้อมูลการตั้งค่าสำเร็จ", ToBillingConfigResponseList(configs))
}

func (h *BillingConfigHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสอพาร์ทเมนท์ไม่ถูกต้อง"))
	}

	var req CreateBillingConfigRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	config, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างการตั้งค่าแล้ว", ToBillingConfigResponse(*config))
}

func (h *BillingConfigHandler) Update(c fiber.Ctx) error {
	configID, err := uuid.Parse(c.Params("configId"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสการตั้งค่าไม่ถูกต้อง"))
	}

	var req UpdateBillingConfigRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	config, err := h.svc.Update(c.Context(), configID, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตการตั้งค่าแล้ว", ToBillingConfigResponse(*config))
}

func (h *BillingConfigHandler) Delete(c fiber.Ctx) error {
	configID, err := uuid.Parse(c.Params("configId"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสการตั้งค่าไม่ถูกต้อง"))
	}

	if err := h.svc.Delete(c.Context(), configID); err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ลบการตั้งค่าแล้ว", nil)
}
