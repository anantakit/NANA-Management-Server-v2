-- +goose Up
-- Q1 Recovery Decision — add METER_RECOVERY_WAIVED reason + amount invariant.
--
-- Waive resolution = a ZERO-amount ADJUSTMENT line so "resolved" stays derivable
-- from the same inverse-FK probe (HasNonVoidAdjustmentLineByRecoveryID) as
-- charge/refund — single source of truth, no separate resolution table. The note
-- records why. Doctrine: project_reading_recovery_q1_unified_decision_surface_lock.
--
-- Triple-guard with domain ValidateAdjustment (billing/model.go) + Q1 wiring.

-- Extend reason enum: add METER_RECOVERY_WAIVED.
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_reason_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_reason_check
    CHECK (adjustment_reason_code IS NULL
        OR adjustment_reason_code IN ('METER_RECOVERY', 'METER_RECOVERY_WAIVED'));

-- Amount-by-reason invariant (corruption guard):
--   METER_RECOVERY        → must move money (amount <> 0)
--   METER_RECOVERY_WAIVED → must be zero (amount = 0)
-- Non-ADJUSTMENT lines are unconstrained here (first clause passes).
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_amount_by_reason
    CHECK (line_type <> 'ADJUSTMENT'
        OR (adjustment_reason_code = 'METER_RECOVERY' AND amount <> 0)
        OR (adjustment_reason_code = 'METER_RECOVERY_WAIVED' AND amount = 0));

-- +goose Down
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_amount_by_reason;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_reason_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_adjustment_reason_check
    CHECK (adjustment_reason_code IS NULL
        OR adjustment_reason_code IN ('METER_RECOVERY'));
