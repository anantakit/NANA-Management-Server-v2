package apartment

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type PaymentRoutingHandler struct {
	svc PaymentRoutingService
}

func NewPaymentRoutingHandler(svc PaymentRoutingService) *PaymentRoutingHandler {
	return &PaymentRoutingHandler{svc: svc}
}

func (h *PaymentRoutingHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Put("/:ruleId", h.Update)
	router.Delete("/:ruleId", h.Delete)
}

func (h *PaymentRoutingHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	rules, err := h.svc.ListByApartment(c.Context(), apartmentID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToPaymentDestinationRuleResponseList(rules))
}

func (h *PaymentRoutingHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req CreatePaymentDestinationRuleRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	rule, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "เพิ่มกฎการชำระเงินแล้ว", ToPaymentDestinationRuleResponse(*rule))
}

func (h *PaymentRoutingHandler) Update(c fiber.Ctx) error {
	ruleID, err := uuid.Parse(c.Params("ruleId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสกฎไม่ถูกต้อง"})
	}

	var req UpdatePaymentDestinationRuleRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	rule, err := h.svc.Update(c.Context(), ruleID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตกฎการชำระเงินแล้ว", ToPaymentDestinationRuleResponse(*rule))
}

func (h *PaymentRoutingHandler) Delete(c fiber.Ctx) error {
	ruleID, err := uuid.Parse(c.Params("ruleId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสกฎไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), ruleID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบกฎการชำระเงินแล้ว", nil)
}
