package handler

import (
	"nana/internal/dto"
	"nana/internal/service"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type TenantHandler struct {
	svc service.TenantService
}

func NewTenantHandler(svc service.TenantService) *TenantHandler {
	return &TenantHandler{svc: svc}
}

func (h *TenantHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Get("/:id", h.GetByID)
	router.Put("/:id", h.Update)
	router.Delete("/:id", h.Delete)
}

func (h *TenantHandler) List(c fiber.Ctx) error {
	var params dto.PaginationParams
	if err := BindQuery(c, &params); err != nil {
		return err
	}
	params.Normalize()

	tenants, total, err := h.svc.List(c.Context(), params)
	if err != nil {
		return Error(c, err)
	}

	meta := dto.ComputeMeta(params.Page, params.Limit, total)
	return SuccessWithMeta(c, "สำเร็จ", dto.ToTenantResponseList(tenants), meta)
}

func (h *TenantHandler) GetByID(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	tenant, err := h.svc.GetByID(c.Context(), tenantID)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "สำเร็จ", dto.ToTenantResponse(*tenant))
}

func (h *TenantHandler) Create(c fiber.Ctx) error {
	var req dto.CreateTenantRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	tenant, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return Error(c, err)
	}
	return Created(c, "สร้างผู้เช่าสำเร็จ", dto.ToTenantResponse(*tenant))
}

func (h *TenantHandler) Update(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	var req dto.UpdateTenantRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	tenant, err := h.svc.Update(c.Context(), tenantID, req)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "อัปเดตผู้เช่าสำเร็จ", dto.ToTenantResponse(*tenant))
}

func (h *TenantHandler) Delete(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), tenantID); err != nil {
		return Error(c, err)
	}
	return Success(c, "ลบผู้เช่าสำเร็จ", nil)
}
