package apartment

import (
	"testing"
)

func TestManualLineItemPresets_NoUtilityItems(t *testing.T) {
	utilityCodesDenyList := map[string]bool{
		"ELECTRICITY": true,
		"WATER":       true,
		"ELECTRIC":    true,
	}

	for _, p := range manualLineItemPresets {
		if utilityCodesDenyList[p.Code] {
			t.Errorf("preset list must not contain utility item %q", p.Code)
		}
	}
}

func TestManualLineItemPresets_StableShape(t *testing.T) {
	if len(manualLineItemPresets) == 0 {
		t.Fatal("preset list must not be empty")
	}

	for i, p := range manualLineItemPresets {
		if p.Code == "" {
			t.Errorf("preset[%d]: code must not be empty", i)
		}
		if p.Label == "" {
			t.Errorf("preset[%d]: label must not be empty", i)
		}
		if p.PricingType == "" {
			t.Errorf("preset[%d]: pricing_type must not be empty", i)
		}
		if p.DefaultAmount < 0 {
			t.Errorf("preset[%d]: default_amount must be >= 0, got %d", i, p.DefaultAmount)
		}
		if p.SortOrder != i+1 {
			t.Errorf("preset[%d]: sort_order must be %d, got %d", i, i+1, p.SortOrder)
		}
	}
}

func TestManualLineItemPresets_UniqueCodes(t *testing.T) {
	seen := make(map[string]bool)
	for _, p := range manualLineItemPresets {
		if seen[p.Code] {
			t.Errorf("duplicate preset code: %q", p.Code)
		}
		seen[p.Code] = true
	}
}

func TestManualLineItemPresets_PerUnitHasUnitPrice(t *testing.T) {
	for _, p := range manualLineItemPresets {
		if p.PricingType == "PER_UNIT" {
			if p.DefaultUnitPrice <= 0 {
				t.Errorf("PER_UNIT preset %q must have default_unit_price > 0", p.Code)
			}
			if p.DefaultQuantity <= 0 {
				t.Errorf("PER_UNIT preset %q must have default_quantity > 0", p.Code)
			}
		}
	}
}
