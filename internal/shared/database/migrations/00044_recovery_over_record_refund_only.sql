-- +goose Up
-- Q1.5 Over-Record Re-model (P1) — refund-only sign + per-utility discriminator.
--
-- Reading Recovery is an over-record correction (recorded > physical): the
-- customer over-paid, so the ADJUSTMENT can only REFUND or WAIVE — never charge.
-- The charge path is removed. Electricity and water are independent decisions,
-- so each ADJUSTMENT line names the utility it settles.
--
-- North-star doctrine: recovery never discovers truth; it acknowledges the
-- previously recorded baseline was wrong. See
-- project_reading_recovery_overrecord_model_lock + Q1_5_OVERRECORD_REMODEL_PLAN.md.
--
-- Unpushed branch → dev DBs reset; no data migration. Triple-guard with domain
-- ValidateAdjustment (billing/model.go).

-- Per-utility discriminator. Nullable in P1 — the service does not populate it
-- until P3 wires per-utility resolution; the NOT-NULL-for-ADJUSTMENT constraint
-- lands in P3 alongside that wiring (preserves the surviving apply invariant).
ALTER TABLE bill_line_items ADD COLUMN adjustment_utility varchar(20);

-- Enum validity (corruption guard). Only ELECTRICITY / WATER are valid.
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_utility_valid
    CHECK (adjustment_utility IS NULL
        OR adjustment_utility IN ('ELECTRICITY', 'WATER'));

-- Utility exclusivity: only ADJUSTMENT lines may carry a utility.
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_utility_only_for_type
    CHECK (line_type = 'ADJUSTMENT' OR adjustment_utility IS NULL);

-- Refund-only sign invariant (replaces 00043's amount <> 0 charge/refund rule):
--   METER_RECOVERY        → refund; amount < 0 (over-record over-payment)
--   METER_RECOVERY_WAIVED → amount = 0
-- A positive (charge) amount is now DB-illegal.
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_amount_by_reason;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_amount_by_reason
    CHECK (line_type <> 'ADJUSTMENT'
        OR (adjustment_reason_code = 'METER_RECOVERY' AND amount < 0)
        OR (adjustment_reason_code = 'METER_RECOVERY_WAIVED' AND amount = 0));

-- +goose Down
-- Restore the 00043 charge/refund (amount <> 0) rule and drop the utility column.
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_amount_by_reason;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_amount_by_reason
    CHECK (line_type <> 'ADJUSTMENT'
        OR (adjustment_reason_code = 'METER_RECOVERY' AND amount <> 0)
        OR (adjustment_reason_code = 'METER_RECOVERY_WAIVED' AND amount = 0));

ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_utility_only_for_type;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_utility_valid;
ALTER TABLE bill_line_items DROP COLUMN IF EXISTS adjustment_utility;
