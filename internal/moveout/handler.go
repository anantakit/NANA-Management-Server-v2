package moveout

import (
	"nana/internal/shared/bind"
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
	router.Get("/:id", h.GetByID)
	router.Put("/:id", h.Update)
	router.Post("/:id/cancel", h.Cancel)
	router.Post("/:id/complete", h.Complete)
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

	return respond.Created(c, "สร้างใบแจ้งย้ายออกสำเร็จ", ToMoveOutResponse(*result))
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

	return respond.Success(c, "อัปเดตใบแจ้งย้ายออกสำเร็จ", ToMoveOutResponse(*result))
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

	return respond.Success(c, "ยกเลิกใบแจ้งย้ายออกสำเร็จ", ToMoveOutResponse(*result))
}

func (h *MoveOutHandler) Complete(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("รหัสใบแจ้งย้ายออกไม่ถูกต้อง"))
	}

	result, err := h.svc.Complete(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "ดำเนินการย้ายออกสำเร็จ", ToMoveOutResponse(*result))
}
