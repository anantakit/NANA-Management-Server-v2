package billingreconciliation

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the reconciliation surface. Phase 1A = `/` GET;
// Phase 1B adds per-room decision GET/PUT/DELETE. Bulk + Generate land in
// 1C/1D and will extend this router additively.
func (h *Handler) RegisterRoutes(r fiber.Router) {
	r.Get("/", h.Reconcile)
	r.Get("/decisions/:room_id/:billing_month", h.GetDecision)
	r.Put("/decisions/:room_id/:billing_month", h.SetDecision)
	r.Delete("/decisions/:room_id/:billing_month", h.DeleteDecision)
}

func (h *Handler) Reconcile(c fiber.Ctx) error {
	var q ReconciliationQuery
	if err := bind.Query(c, &q); err != nil {
		return err
	}

	report, err := h.svc.Reconcile(c.Context(), q)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToReportResponse(report))
}

func (h *Handler) GetDecision(c fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("room_id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("room_id ไม่ถูกต้อง"))
	}
	billingMonth := c.Params("billing_month")

	d, attr, err := h.svc.GetDecision(c.Context(), roomID, billingMonth)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToDecisionResponse(d, attr))
}

func (h *Handler) SetDecision(c fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("room_id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("room_id ไม่ถูกต้อง"))
	}
	billingMonth := c.Params("billing_month")

	var req SetDecisionRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง"))
	}

	// actor optional — `decided_by` is ON DELETE SET NULL, and the seed
	// admin path in dev tools sometimes lacks a userID Local. The DB will
	// happily accept NULL; the attribution joins surface "—" on the FE.
	var actor *uuid.UUID
	if uid, ok := c.Locals("userID").(uuid.UUID); ok {
		actor = &uid
	}

	d, attr, err := h.svc.SetDecision(c.Context(), apartmentID, roomID, billingMonth, DecisionState(req.Decision), actor)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "บันทึกการตัดสินใจแล้ว", ToDecisionResponse(d, attr))
}

func (h *Handler) DeleteDecision(c fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("room_id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("room_id ไม่ถูกต้อง"))
	}
	billingMonth := c.Params("billing_month")

	var req DeleteDecisionRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง"))
	}

	if err := h.svc.DeleteDecision(c.Context(), apartmentID, roomID, billingMonth); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ยกเลิกการตัดสินใจแล้ว", nil)
}
