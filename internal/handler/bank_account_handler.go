package handler

import (
	"nana/internal/dto"
	"nana/internal/service"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type BankAccountHandler struct {
	svc service.BankAccountService
}

func NewBankAccountHandler(svc service.BankAccountService) *BankAccountHandler {
	return &BankAccountHandler{svc: svc}
}

func (h *BankAccountHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Put("/:accountId", h.Update)
	router.Delete("/:accountId", h.Delete)
}

func (h *BankAccountHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	accounts, err := h.svc.ListByApartment(c.Context(), apartmentID)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "สำเร็จ", dto.ToBankAccountResponseList(accounts))
}

func (h *BankAccountHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req dto.CreateBankAccountRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	account, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return Error(c, err)
	}
	return Created(c, "เพิ่มบัญชีธนาคารสำเร็จ", dto.ToBankAccountResponse(*account))
}

func (h *BankAccountHandler) Update(c fiber.Ctx) error {
	accountID, err := uuid.Parse(c.Params("accountId"))
	if err != nil {
		return ValidationError(c, []string{"รหัสบัญชีไม่ถูกต้อง"})
	}

	var req dto.UpdateBankAccountRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	account, err := h.svc.Update(c.Context(), accountID, req)
	if err != nil {
		return Error(c, err)
	}
	return Success(c, "อัปเดตบัญชีธนาคารสำเร็จ", dto.ToBankAccountResponse(*account))
}

func (h *BankAccountHandler) Delete(c fiber.Ctx) error {
	accountID, err := uuid.Parse(c.Params("accountId"))
	if err != nil {
		return ValidationError(c, []string{"รหัสบัญชีไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), accountID); err != nil {
		return Error(c, err)
	}
	return Success(c, "ลบบัญชีธนาคารสำเร็จ", nil)
}
