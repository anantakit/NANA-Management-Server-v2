package apartment

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"nana/internal/shared/respond"
)

// ManualLineItemPreset represents a quick-add preset for manual bill line items.
// Excludes utility charges (water, electric) which are meter-driven.
//
// PricingType:
//   - "FLAT"     → prefill default_amount as total
//   - "PER_UNIT" → prefill default_quantity × default_unit_price
type ManualLineItemPreset struct {
	Code             string `json:"code"`
	Label            string `json:"label"`
	PricingType      string `json:"pricing_type"`
	DefaultAmount    int64  `json:"default_amount"`
	DefaultQuantity  int    `json:"default_quantity,omitempty"`
	DefaultUnitPrice int64  `json:"default_unit_price,omitempty"`
	Editable         bool   `json:"editable"`
	SortOrder        int    `json:"sort_order"`
}

// manualLineItemPresets are server-side constants for quick manual charges.
// These are intentionally NOT utility charges (water/electric).
var manualLineItemPresets = []ManualLineItemPreset{
	{Code: "KEY_SERVICE", Label: "ค่ากุญแจเซอร์วิส", PricingType: "PER_UNIT", DefaultQuantity: 1, DefaultUnitPrice: 50, Editable: true, SortOrder: 1},
	{Code: "KEY_LOSS", Label: "ค่ากุญแจและคีการ์ด", PricingType: "FLAT", DefaultAmount: 250, Editable: true, SortOrder: 2},
}

// PresetHandler serves manual line item presets scoped to an apartment.
type PresetHandler struct{}

func NewPresetHandler() *PresetHandler {
	return &PresetHandler{}
}

func (h *PresetHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.ListPresets)
}

// ListPresets returns the static list of manual line item presets.
// Apartment ID is validated but not used yet (presets are global constants for now).
func (h *PresetHandler) ListPresets(c fiber.Ctx) error {
	if _, err := uuid.Parse(c.Params("id")); err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	return respond.Success(c, "สำเร็จ", manualLineItemPresets)
}
