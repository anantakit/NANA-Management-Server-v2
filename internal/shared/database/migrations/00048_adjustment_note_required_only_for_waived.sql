-- +goose Up
-- Q1.6 auto-refund — align the adjustment-note DB constraint with the domain.
--
-- Migration 00039 required a note on EVERY ADJUSTMENT line. That matched the
-- pre-Q1.6 model where every recovery was an operator DECISION (charge/refund/
-- waive) carrying a rationale. Q1.6 removed the decision: an over-record refund
-- is deterministic and self-explaining (its own evidence line), so it carries
-- NO operator note. Domain ValidateAdjustment already reflects this — a note is
-- required ONLY for METER_RECOVERY_WAIVED; a METER_RECOVERY refund's note is
-- optional. The DB constraint was left stricter than the domain, so the
-- auto-emitted refund line (note IS NULL) could not persist.
--
-- Realign: a note is required only for the WAIVED reason. (WAIVED itself is
-- dead in Q1.6 and will be removed in a later cleanup migration, which will
-- also drop this constraint.)
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_note_required;
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_note_required
        CHECK (line_type != 'ADJUSTMENT'
            OR adjustment_reason_code != 'METER_RECOVERY_WAIVED'
            OR adjustment_note IS NOT NULL);

-- +goose Down
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_note_required;
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_note_required
        CHECK (line_type != 'ADJUSTMENT' OR adjustment_note IS NOT NULL);
