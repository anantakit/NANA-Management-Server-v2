package billingreconciliation

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the read-only reconciliation endpoint. Phase 1A has
// exactly one route by design — no decision writes (1B), no bulk (1C), no
// generate (1D). Routes extend additively in later phases.
func (h *Handler) RegisterRoutes(r fiber.Router) {
	r.Get("/", h.Reconcile)
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
