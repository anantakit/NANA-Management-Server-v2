package tenant

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type TenantHandler struct {
	svc TenantService
}

func NewTenantHandler(svc TenantService) *TenantHandler {
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
	var params pagination.PaginationParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	tenants, total, err := h.svc.List(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", ToTenantResponseList(tenants), meta)
}

func (h *TenantHandler) GetByID(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	tenant, err := h.svc.GetByID(c.Context(), tenantID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToTenantResponse(*tenant))
}

func (h *TenantHandler) Create(c fiber.Ctx) error {
	var req CreateTenantRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	tenant, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "สร้างผู้เช่าแล้ว", ToTenantResponse(*tenant))
}

func (h *TenantHandler) Update(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	var req UpdateTenantRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	tenant, err := h.svc.Update(c.Context(), tenantID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตผู้เช่าแล้ว", ToTenantResponse(*tenant))
}

func (h *TenantHandler) Delete(c fiber.Ctx) error {
	tenantID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสผู้เช่าไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), tenantID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบผู้เช่าแล้ว", nil)
}
