package payment

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type PaymentHandler struct {
	svc PaymentService
}

func NewPaymentHandler(svc PaymentService) *PaymentHandler {
	return &PaymentHandler{svc: svc}
}

// RegisterRoutes registers payment endpoints under the bills route group.
// Call as: paymentHandler.RegisterRoutes(admin.Group("/bills"))
func (h *PaymentHandler) RegisterRoutes(router fiber.Router) {
	router.Post("/:id/payments", h.RecordBillPayment)
}

func (h *PaymentHandler) RecordBillPayment(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("bill ID ไม่ถูกต้อง"))
	}

	var req RecordBillPaymentRequest
	if err := bind.Body(c, &req); err != nil {
		return respond.Error(c, err)
	}

	actor := middleware.ActorFromCtx(c)
	result, err := h.svc.RecordBillPayment(c.Context(), id, req, actor)
	if err != nil {
		return respond.Error(c, err)
	}

	c.Status(fiber.StatusCreated)
	return respond.Success(c, "บันทึกการรับชำระแล้ว", result)
}
