-- +goose Up
-- Q1.5 Over-Record Re-model (P3-B) — enforce adjustment_utility on ADJUSTMENT.
--
-- The P1-deferred half of the utility constraint. Now that the per-utility apply
-- path populates adjustment_utility on every ADJUSTMENT line, the NOT-NULL rule
-- is safe to enforce (P1 left it nullable so the still-live apply path didn't
-- break before it could set the value). Triple-guard with ValidateAdjustment.
--
-- Unpushed branch → dev DBs reset; no data migration.
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_utility_required
    CHECK (line_type <> 'ADJUSTMENT' OR adjustment_utility IS NOT NULL);

-- +goose Down
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_utility_required;
