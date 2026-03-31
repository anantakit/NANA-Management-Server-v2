package contract

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type ContractHandler struct {
	svc ContractService
}

func NewContractHandler(svc ContractService) *ContractHandler {
	return &ContractHandler{svc: svc}
}

func (h *ContractHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Get("/:contractId", h.GetByID)
	router.Put("/:contractId", h.Update)
	router.Delete("/:contractId", h.Delete)
}

func (h *ContractHandler) List(c fiber.Ctx) error {
	var params ContractListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	contracts, total, err := h.svc.List(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", ToContractResponseList(contracts), meta)
}

func (h *ContractHandler) GetByID(c fiber.Ctx) error {
	contractID, err := uuid.Parse(c.Params("contractId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสสัญญาไม่ถูกต้อง"})
	}

	contract, err := h.svc.GetByID(c.Context(), contractID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToContractResponse(*contract))
}

func (h *ContractHandler) Create(c fiber.Ctx) error {
	var req CreateContractRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	contract, err := h.svc.Create(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "สร้างสัญญาสำเร็จ", ToContractResponse(*contract))
}

func (h *ContractHandler) Update(c fiber.Ctx) error {
	contractID, err := uuid.Parse(c.Params("contractId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสสัญญาไม่ถูกต้อง"})
	}

	var req UpdateContractRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	contract, err := h.svc.Update(c.Context(), contractID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตสัญญาสำเร็จ", ToContractResponse(*contract))
}

func (h *ContractHandler) Delete(c fiber.Ctx) error {
	contractID, err := uuid.Parse(c.Params("contractId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสสัญญาไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), contractID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบสัญญาสำเร็จ", nil)
}
