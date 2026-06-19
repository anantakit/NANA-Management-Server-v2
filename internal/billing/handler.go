package billing

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/money"
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

// RegisterRoutes mounts the billing-core routes (W1 single bill, W3 monthly
// draft, W4 settlement, W5 correction) under the caller-supplied router.
//
// The 9 monthly batch routes (/batches/*, /preflight, /batch-monthly,
// /finalize-all-by-month) live on monthly.Handler — mount it BEFORE this
// handler in cmd/main.go so its literal segments register first and beat
// /:id at the radix match level.
func (h *BillingHandler) RegisterRoutes(r fiber.Router) {
	r.Get("/summary", h.Summary)
	r.Get("/", h.List)
	r.Get("/:id", h.GetByID)
	r.Post("/monthly", h.CreateMonthly)
	r.Post("/settlement/preview", h.PreviewSettlement)
	r.Post("/settlement", h.CreateSettlement)
	r.Patch("/:id/finalize", h.Finalize)
	r.Patch("/:id/void", h.Void)
	r.Patch("/:id/paid", h.MarkPaid)
	r.Patch("/:id/settlement-draft", h.UpdateSettlementDraft)
	r.Patch("/:id/monthly-draft", h.UpdateMonthlyDraft)
	r.Post("/:id/correct", h.Correct)
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

func (h *BillingHandler) Summary(c fiber.Ctx) error {
	var params BillSummaryParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}

	raw, err := h.svc.GetSummary(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", BillSummaryResponse{
		TotalCount:    raw.TotalCount,
		PendingCount:  raw.PendingCount,
		PaidCount:     raw.PaidCount,
		VoidedCount:   raw.VoidedCount,
		TotalAmount:   money.ToBaht(raw.TotalAmount),
		PendingAmount: money.ToBaht(raw.PendingAmount),
		PaidAmount:    money.ToBaht(raw.PaidAmount),
		VoidedAmount:  money.ToBaht(raw.VoidedAmount),
	})
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

	bill, err := h.svc.CreateMonthlyBill(c.Context(), req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลร่างรายเดือนแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) PreviewSettlement(c fiber.Ctx) error {
	var req PreviewSettlementRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	contractID, err := uuid.Parse(req.ContractID)
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("contract_id ไม่ถูกต้อง"))
	}

	input := PreviewSettlementInput{
		ContractID: contractID,
	}
	if req.RentMode != "" {
		input.RentMode = SettlementRentMode(req.RentMode)
	}

	preview, err := h.svc.PreviewSettlement(c.Context(), input)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToSettlementPreviewResponse(preview))
}

func (h *BillingHandler) CreateSettlement(c fiber.Ctx) error {
	var req CreateSettlementBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.CreateSettlementBill(c.Context(), req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลร่างปิดสัญญาแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) Finalize(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	bill, err := h.svc.FinalizeBill(c.Context(), id, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ออกบิลแล้ว", ToBillResponseWithRelations(*bill))
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

	bill, err := h.svc.VoidBill(c.Context(), id, req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยกเลิกบิลแล้ว", ToBillResponseWithRelations(*bill))
}

// Correct executes the void+recreate correction flow on a FINALIZED bill.
// Returns 201 with the new DRAFT bill — the FE typically routes from here
// into the DRAFT editor for further adjustments before re-finalizing.
//
// Domain guards (PAID / DRAFT / VOID / SETTLEMENT / already-superseded) surface
// as 400 with the Thai sentinel message. Race-safe via row-lock inside the
// service TX.
func (h *BillingHandler) Correct(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	var req CorrectBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.CorrectBill(c.Context(), id, req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลร่างใหม่แทนใบเดิมแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) UpdateSettlementDraft(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	var req UpdateSettlementDraftRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.UpdateSettlementDraft(c.Context(), id, req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตบิลปิดสัญญาแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) UpdateMonthlyDraft(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	var req UpdateMonthlyDraftRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.UpdateMonthlyDraft(c.Context(), id, req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตบิลรายเดือนแล้ว", ToBillResponseWithRelations(*bill))
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

	return respond.Success(c, "บันทึกการชำระแล้ว", ToBillResponseWithRelations(*bill))
}
