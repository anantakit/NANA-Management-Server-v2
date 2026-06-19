package settlement

import "github.com/gofiber/fiber/v3"

// Handler hosts the settlement workflow's HTTP surface.
//
// In commit 2 of the W4 extraction the handler exists but registers no
// routes — settlement routes (POST /settlement, POST /settlement/preview,
// PATCH /:id/settlement-draft) migrate in commit 6 of the plan. Mounting
// the empty handler in cmd/main.go in commit 2 is intentional: it pins the
// registration order between monthlyHandler and billHandler so the
// literal-before-param radix invariant is preserved when routes land later.
type Handler struct {
	svc *Service
}

// NewHandler wires the settlement workflow's handler with the workflow
// service. Constructor exists in commit 2 even though routes are empty so
// cmd/main.go can establish the wiring shape ahead of route migration.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the settlement workflow's routes under the
// caller-supplied router (typically admin.Group("/bills")).
//
// MUST be mounted AFTER monthly.Handler.RegisterRoutes and BEFORE
// billing.Handler.RegisterRoutes so the literal segments (/settlement,
// /settlement/preview) register first and beat /:id at the radix match
// level. The PATCH /:id/settlement-draft route uses a literal at slot 2
// and does not conflict structurally with billing root's PATCH /:id/finalize
// etc. — but mounting order is preserved for forward consistency.
//
// Empty in commit 2 of W4. Routes land in commit 6.
func (h *Handler) RegisterRoutes(r fiber.Router) {
	// Intentionally empty — see method doc.
}
