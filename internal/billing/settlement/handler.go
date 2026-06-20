package settlement

import (
	"nana/internal/billing"
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// Handler hosts the settlement workflow's HTTP surface.
//
// Owns 3 routes mounted on the caller-supplied router (typically
// admin.Group("/bills")):
//   - POST   /settlement/preview
//   - POST   /settlement
//   - PATCH  /:id/settlement-draft
//
// MUST be mounted AFTER monthly.Handler.RegisterRoutes and BEFORE
// billing.Handler.RegisterRoutes so the literal segments (/settlement,
// /settlement/preview) register first and beat /:id at the radix match
// level. PATCH /:id/settlement-draft uses a literal at slot 2 and does
// not conflict structurally with billing root's PATCH /:id/finalize etc.,
// but the order convention is preserved.
type Handler struct {
	svc *Service
}

// NewHandler wires the settlement workflow's handler with the workflow
// service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the 3 settlement routes.
func (h *Handler) RegisterRoutes(r fiber.Router) {
	r.Post("/settlement/preview", h.PreviewSettlement)
	r.Post("/settlement", h.CreateSettlement)
	r.Patch("/:id/settlement-draft", h.UpdateSettlementDraft)
}

func (h *Handler) PreviewSettlement(c fiber.Ctx) error {
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
		input.RentMode = billing.SettlementRentMode(req.RentMode)
	}

	preview, err := h.svc.PreviewSettlement(c.Context(), input)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToSettlementPreviewResponse(preview))
}

func (h *Handler) CreateSettlement(c fiber.Ctx) error {
	var req CreateSettlementBillRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	bill, err := h.svc.CreateSettlementBill(c.Context(), req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Created(c, "สร้างบิลร่างปิดสัญญาแล้ว", billing.ToBillResponseWithRelations(*bill))
}

func (h *Handler) UpdateSettlementDraft(c fiber.Ctx) error {
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

	return respond.Success(c, "อัปเดตบิลปิดสัญญาแล้ว", billing.ToBillResponseWithRelations(*bill))
}
