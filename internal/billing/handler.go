package billing

import (
	"nana/internal/shared/bind"
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

// actorFromCtx pulls the admin's user ID out of fiber locals set by the JWT
// middleware (shared/middleware/jwt.go) so service-layer audit recorders can
// stamp the actor on each event. Returns nil when the request is unauthenticated
// (e.g. before the JWT middleware ran in test paths) — the audit row then
// records a nil actor, which is the same shape used by cross-feature
// system-triggered events.
func actorFromCtx(c fiber.Ctx) *uuid.UUID {
	if uid, ok := c.Locals("userID").(uuid.UUID); ok {
		return &uid
	}
	return nil
}

func (h *BillingHandler) RegisterRoutes(r fiber.Router) {
	// Batches must be registered before /:id to avoid the "batches" literal
	// being captured as a bill id.
	r.Get("/batches", h.ListBatches)
	r.Get("/batches/:id", h.GetBatch)
	r.Get("/batches/:id/items", h.GetBatchItems)
	r.Post("/batches/:id/commit", h.CommitBatch)
	r.Post("/batches/:id/finalize-all", h.BatchFinalizeAll)

	r.Get("/summary", h.Summary)
	r.Get("/preflight", h.PreflightMonthly)
	r.Get("/", h.List)
	r.Get("/:id", h.GetByID)
	r.Post("/monthly", h.CreateMonthly)
	r.Post("/settlement/preview", h.PreviewSettlement)
	r.Post("/settlement", h.CreateSettlement)
	r.Post("/batch-monthly", h.BatchCreateMonthly)
	r.Patch("/:id/finalize", h.Finalize)
	r.Patch("/:id/void", h.Void)
	r.Patch("/:id/paid", h.MarkPaid)
	r.Patch("/:id/settlement-draft", h.UpdateSettlementDraft)
	r.Patch("/:id/monthly-draft", h.UpdateMonthlyDraft)
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

	bill, err := h.svc.CreateMonthlyBill(c.Context(), req, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลรายเดือนแล้ว", ToBillResponseWithRelations(*bill))
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

	bill, err := h.svc.CreateSettlementBill(c.Context(), req, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลย้ายออกแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) Finalize(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	bill, err := h.svc.FinalizeBill(c.Context(), id, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยืนยันบิลแล้ว", ToBillResponseWithRelations(*bill))
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

	bill, err := h.svc.VoidBill(c.Context(), id, req, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยกเลิกบิลแล้ว", ToBillResponseWithRelations(*bill))
}

func (h *BillingHandler) PreflightMonthly(c fiber.Ctx) error {
	var req MonthlyPreflightRequest
	if err := bind.Query(c, &req); err != nil {
		return err
	}

	result, err := h.svc.PreflightMonthly(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToMonthlyPreflightResponse(result))
}

func (h *BillingHandler) BatchCreateMonthly(c fiber.Ctx) error {
	var req BatchCreateMonthlyBillsRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	var createdBy *uuid.UUID
	if uid, ok := c.Locals("userID").(uuid.UUID); ok {
		createdBy = &uid
	}

	result, err := h.svc.BatchCreateMonthlyBills(c.Context(), req, createdBy)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สร้างบิลรายเดือนแบบกลุ่มแล้ว", ToBatchTriggerResponse(result))
}

func (h *BillingHandler) CommitBatch(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	result, err := h.svc.CommitBatch(c.Context(), id)
	if err != nil {
		// Partial commit: return 200 with result so FE can see progress.
		if result != nil {
			return respond.Success(c, "commit บางส่วนล้มเหลว กรุณาลองใหม่", ToCommitBatchResponse(result))
		}
		return respond.Error(c, err)
	}

	return respond.Success(c, "commit บิลแล้ว", ToCommitBatchResponse(result))
}

// BatchFinalizeAll finalizes every DRAFT monthly bill in the given batch.
// Returns 200 + structured result body (success_count / fail_count /
// failures[]) — partial failure is a normal outcome, not an HTTP error,
// so the FE can render per-row reasons without parsing a 4xx body.
func (h *BillingHandler) BatchFinalizeAll(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	result, err := h.svc.BatchFinalizeAll(c.Context(), id, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	msg := "ออกบิลทั้งหมดแล้ว"
	if result.FailCount > 0 {
		msg = "ออกบิลบางใบไม่สำเร็จ กรุณาตรวจสอบ"
	}
	return respond.Success(c, msg, result)
}

func (h *BillingHandler) GetBatch(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}
	batch, err := h.svc.GetBatchByID(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToBatchHeaderResponse(batch))
}

func (h *BillingHandler) GetBatchItems(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}
	items, err := h.svc.GetBatchItems(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}
	resp := make([]BatchItemResponse, len(items))
	for i, it := range items {
		resp[i] = ToBatchItemResponse(it)
	}
	return respond.Success(c, "สำเร็จ", resp)
}

func (h *BillingHandler) ListBatches(c fiber.Ctx) error {
	var params BatchListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	batches, total, err := h.svc.ListBatches(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}
	resp := make([]BatchHeaderResponse, len(batches))
	for i := range batches {
		resp[i] = ToBatchHeaderResponse(&batches[i])
	}
	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", resp, meta)
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

	bill, err := h.svc.UpdateSettlementDraft(c.Context(), id, req, actorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตบิลสรุปยอดแล้ว", ToBillResponseWithRelations(*bill))
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

	bill, err := h.svc.UpdateMonthlyDraft(c.Context(), id, req, actorFromCtx(c))
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
