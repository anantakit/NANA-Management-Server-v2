package moveout

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type MoveOutHandler struct {
	svc MoveOutService
}

func NewMoveOutHandler(svc MoveOutService) *MoveOutHandler {
	return &MoveOutHandler{svc: svc}
}

func (h *MoveOutHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Get("/queue", h.Queue)
	router.Get("/:id", h.GetByID)
	router.Put("/:id", h.Update)
	router.Patch("/:id/actual-date", h.SetActualMoveOutDate)

	// Settlement preview (non-persisting)
	router.Get("/:id/settlement-preview", h.PreviewSettlement)

	// Forward commands
	router.Post("/:id/record-exit-meter", h.RecordExitMeter)
	router.Post("/:id/generate-settlement", h.GenerateSettlement)
	router.Post("/:id/finalize-settlement", h.FinalizeSettlement)
	router.Post("/:id/record-payment", h.RecordPaymentOutcome)
	router.Post("/:id/skip-payment", h.SkipPayment)
	router.Post("/:id/close", h.CloseMoveOut)
	router.Post("/:id/close-with-unsettled", h.CloseMoveOutWithUnsettled)
	router.Post("/:id/cancel", h.Cancel)

	// Correction commands
	router.Post("/:id/update-exit-meter", h.UpdateExitMeter)
	router.Post("/:id/regenerate-settlement", h.RegenerateSettlement)
	router.Post("/:id/reopen", h.ReopenForCorrection)
	router.Post("/:id/correct-settlement", h.CorrectSettlement)
}

func (h *MoveOutHandler) List(c fiber.Ctx) error {
	var params MoveOutListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	items, total, err := h.svc.List(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "ดึงข้อมูลใบแจ้งย้ายออกสำเร็จ", ToMoveOutResponseList(items), meta)
}

func (h *MoveOutHandler) Create(c fiber.Ctx) error {
	var req CreateMoveOutRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างใบแจ้งย้ายออกแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) Queue(c fiber.Ctx) error {
	var params MoveOutQueueParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}

	result, err := h.svc.Queue(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ดึงคิวใบแจ้งย้ายออกสำเร็จ", result)
}

func (h *MoveOutHandler) GetByID(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.GetByID(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ดึงข้อมูลใบแจ้งย้ายออกสำเร็จ", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) Update(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req UpdateMoveOutRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.Update(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตใบแจ้งย้ายออกแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) SetActualMoveOutDate(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req SetActualMoveOutDateRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.SetActualMoveOutDate(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "บันทึกวันย้ายออกจริงแล้ว", ToMoveOutResponse(*result))
}

// --- Forward commands ---

func (h *MoveOutHandler) RecordExitMeter(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req RecordExitMeterRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.RecordExitMeter(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "บันทึกย้ายออกแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) PreviewSettlement(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	rentMode := RentMode(c.Query("rent_mode"))
	if !rentMode.IsValid() {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("rent_mode ไม่ถูกต้อง"))
	}

	result, err := h.svc.PreviewSettlement(c.Context(), id, rentMode)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToSettlementPreviewResponse(result))
}

func (h *MoveOutHandler) GenerateSettlement(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req RentModeRequest
	// Body is optional — ignore bind error for empty body
	_ = bind.Body(c, &req)

	result, err := h.svc.GenerateSettlement(c.Context(), id, RentMode(req.RentMode))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สร้างบิลสรุปแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) FinalizeSettlement(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.FinalizeSettlement(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยืนยันบิลสรุปยอดแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) RecordPaymentOutcome(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req RecordPaymentOutcomeRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.RecordPaymentOutcome(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "บันทึกผลการชำระแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) SkipPayment(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.SkipPayment(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "บันทึกข้ามการชำระแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) CloseMoveOut(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.CloseMoveOut(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ปิดการย้ายออกแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) CloseMoveOutWithUnsettled(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.CloseMoveOutWithUnsettled(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ปิดงานโดยไม่บันทึกการเงินแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) Cancel(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.Cancel(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ยกเลิกใบแจ้งย้ายออกแล้ว", ToMoveOutResponse(*result))
}

// --- Correction commands ---

func (h *MoveOutHandler) UpdateExitMeter(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req UpdateExitMeterRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.UpdateExitMeter(c.Context(), id, req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "อัปเดตมิเตอร์ย้ายออกแล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) RegenerateSettlement(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req RentModeRequest
	_ = bind.Body(c, &req)

	result, err := h.svc.RegenerateSettlement(c.Context(), id, RentMode(req.RentMode))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สร้างบิลสรุปใหม่แล้ว", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) ReopenForCorrection(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.ReopenForCorrection(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "เปิดแก้ไขแล้ว", ToMoveOutResponse(*result))
}

// CorrectSettlement triggers the user-initiated void+recreate flow on a
// FINALIZED settlement bill of a PENDING_PAYMENT or READY_TO_CLOSE notice.
// Service orchestrator atomically voids the old settlement, regenerates a
// new DRAFT, rebinds notice.settlement_bill_id, downgrades status to
// PENDING_SETTLEMENT, and clears payment metadata. PAID settlement and
// COMPLETED notices are blocked. Returns the updated notice so the FE
// re-renders the move-out detail at the settlement step.
func (h *MoveOutHandler) CorrectSettlement(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	var req CorrectSettlementRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.CorrectSettlement(c.Context(), id, req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ออกใบสรุปยอดใหม่แล้ว", ToMoveOutResponse(*result))
}
