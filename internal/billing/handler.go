package billing

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type BillingHandler struct {
	svc BillingService
}

func NewBillingHandler(svc BillingService) *BillingHandler {
	return &BillingHandler{svc: svc}
}

func (h *BillingHandler) RegisterRoutes(r fiber.Router) {
	r.Get("/", h.List)
	r.Get("/:id", h.GetByID)
	r.Post("/monthly", h.CreateMonthly)
	r.Post("/settlement", h.CreateSettlement)
	r.Post("/batch-monthly", h.BatchCreateMonthly)
	r.Patch("/:id/finalize", h.Finalize)
	r.Patch("/:id/void", h.Void)
	r.Patch("/:id/paid", h.MarkPaid)
}

func (h *BillingHandler) List(c fiber.Ctx) error {
	var params BillListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}

	bills, total, err := h.svc.List(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	items := make([]BillListItemResponse, len(bills))
	for i, b := range bills {
		items[i] = ToBillListItemResponse(b)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", items, meta)
}

func (h *BillingHandler) GetByID(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	bill, err := h.svc.GetByID(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) CreateMonthly(c fiber.Ctx) error {
	var req CreateMonthlyBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.CreateMonthlyBill(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลรายเดือนสำเร็จ", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) CreateSettlement(c fiber.Ctx) error {
	var req CreateSettlementBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.CreateSettlementBill(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลย้ายออกสำเร็จ", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) Finalize(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	bill, err := h.svc.FinalizeBill(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยืนยันบิลสำเร็จ", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) Void(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	var req VoidBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.VoidBill(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยกเลิกบิลสำเร็จ", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) BatchCreateMonthly(c fiber.Ctx) error {
	var req BatchCreateMonthlyBillsRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.BatchCreateMonthlyBills(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สร้างบิลรายเดือนแบบกลุ่มสำเร็จ", ToBatchBillResultResponse(*result))
}

func (h *BillingHandler) MarkPaid(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	bill, err := h.svc.MarkPaid(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "บันทึกชำระเงินสำเร็จ", ToBillResponseWithRelations(*bill))
}
