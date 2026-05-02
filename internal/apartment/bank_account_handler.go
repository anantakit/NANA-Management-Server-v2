package apartment

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type BankAccountHandler struct {
	svc BankAccountService
}

func NewBankAccountHandler(svc BankAccountService) *BankAccountHandler {
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
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	accounts, err := h.svc.ListByApartment(c.Context(), apartmentID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToBankAccountResponseList(accounts))
}

func (h *BankAccountHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req CreateBankAccountRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	account, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "เพิ่มบัญชีธนาคารแล้ว", ToBankAccountResponse(*account))
}

func (h *BankAccountHandler) Update(c fiber.Ctx) error {
	accountID, err := uuid.Parse(c.Params("accountId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสบัญชีไม่ถูกต้อง"})
	}

	var req UpdateBankAccountRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	account, err := h.svc.Update(c.Context(), accountID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "อัปเดตบัญชีธนาคารแล้ว", ToBankAccountResponse(*account))
}

func (h *BankAccountHandler) Delete(c fiber.Ctx) error {
	accountID, err := uuid.Parse(c.Params("accountId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสบัญชีไม่ถูกต้อง"})
	}

	if err := h.svc.Delete(c.Context(), accountID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบบัญชีธนาคารแล้ว", nil)
}
