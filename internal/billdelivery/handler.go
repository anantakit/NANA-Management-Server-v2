package billdelivery

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
)

type deliveryHandler struct {
	svc DeliveryService
}

func NewDeliveryHandler(svc DeliveryService) *deliveryHandler {
	return &deliveryHandler{svc: svc}
}

func (h *deliveryHandler) RegisterRoutes(r fiber.Router) {
	r.Post("/", h.RecordDelivery)
}

func (h *deliveryHandler) RecordDelivery(c fiber.Ctx) error {
	var req RecordDeliveryRequest
	if err := bind.Body(c, &req); err != nil {
		return respond.Error(c, err)
	}

	actor := middleware.ActorFromCtx(c)
	result, err := h.svc.RecordDelivery(c.Context(), req, actor)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "บันทึกการส่งบิลแล้ว", result)
}
