-- +goose Up
-- Reading Recovery Phase 4 — ADJUSTMENT line type + 3 nullable columns + 8 CHECK
-- constraints + FK ON DELETE RESTRICT on bill_line_items.
--
-- Doctrine: feedback_reading_recovery_doctrine.md (locked 2026-06-22)
-- Phase 1: /Users/anantakit/.claude/plans/mutable-swimming-firefly.md
-- Phase 2: /Users/anantakit/.claude/plans/lexical-popping-canyon.md
-- Phase 4: /Users/anantakit/.claude/plans/hidden-waddling-marble.md
--
-- The ADJUSTMENT line carries money truth (refund/charge) with FK provenance
-- back to the meter_readings row that triggered the recovery. Source MUST be
-- MANUAL (operator-deliberated, never AUTO computed). Reason code enum starts
-- with METER_RECOVERY only; PRIOR_OVERCHARGE / PRIOR_UNDERCHARGE / OTHER
-- deferred until those workflows open (no dormant enum values).

-- Extend line_type CHECK to include ADJUSTMENT. Pattern matches
-- 00024_add_outstanding_bill_line_type.sql.
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_line_type_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_line_type_check
    CHECK (line_type IN (
        'ROOM_RENT', 'ELECTRICITY', 'WATER',
        'CLEANING_FEE', 'KEY_SERVICE',
        'PRORATE_RENT', 'PENALTY', 'PREPAID_CREDIT', 'OTHER',
        'OUTSTANDING_BILL',
        'ADJUSTMENT'
    ));

-- 3 nullable columns + FK. ON DELETE RESTRICT matches Phase 1 precedent
-- (recovery_source_reading_id, migration 00038). Hard-deleting a recovery
-- meter row that's referenced by an ADJUSTMENT line errors loudly — the
-- correct path is to reverse the adjustment first.
ALTER TABLE bill_line_items
    ADD COLUMN adjustment_recovery_reading_id uuid
        REFERENCES meter_readings(id) ON DELETE RESTRICT,
    ADD COLUMN adjustment_reason_code        varchar(30),
    ADD COLUMN adjustment_note               text;

-- ── 8 CHECK constraints (one concern per CHECK, matches Phase 1's 4-CHECK
-- granularity precedent). Each violation message names exactly which
-- invariant fired, making test failures grep-friendly. ──

-- FK presence (ADJUSTMENT requires recovery FK)
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_fk_required
        CHECK (line_type != 'ADJUSTMENT'
            OR adjustment_recovery_reading_id IS NOT NULL);

-- FK exclusivity (only ADJUSTMENT may carry recovery FK)
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_fk_only_for_type
        CHECK (line_type = 'ADJUSTMENT'
            OR adjustment_recovery_reading_id IS NULL);

-- Reason presence
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_reason_required
        CHECK (line_type != 'ADJUSTMENT'
            OR adjustment_reason_code IS NOT NULL);

-- Reason exclusivity
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_reason_only_for_type
        CHECK (line_type = 'ADJUSTMENT'
            OR adjustment_reason_code IS NULL);

-- Note presence — DB enforces NOT NULL only. Whitespace + min-10-chars
-- policy lives in domain ValidateAdjustment. Single authority per concern
-- (matches Phase 1's "DB owns NOT NULL; domain owns whitespace" lock).
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_note_required
        CHECK (line_type != 'ADJUSTMENT' OR adjustment_note IS NOT NULL);

-- Note exclusivity
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_note_only_for_type
        CHECK (line_type = 'ADJUSTMENT' OR adjustment_note IS NULL);

-- Source = MANUAL on ADJUSTMENT (triple-guard with domain + Phase 5 wiring)
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_source_manual
        CHECK (line_type != 'ADJUSTMENT' OR source = 'MANUAL');

-- Reason-code enum. Starts with METER_RECOVERY only. Future codes added
-- via a separate migration when the corresponding workflows open.
ALTER TABLE bill_line_items
    ADD CONSTRAINT bill_line_items_adjustment_reason_check
        CHECK (adjustment_reason_code IS NULL
            OR adjustment_reason_code IN ('METER_RECOVERY'));

-- NOTE: No partial index on adjustment_recovery_reading_id in Phase 4 —
-- no query consumes it yet. Phase 5 adds the index when the recovery
-- lookup query lands (first table ≠ index doctrine, matches Phase 1
-- partial-index decision in migration 00038).

-- +goose Down
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_reason_check;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_source_manual;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_note_only_for_type;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_note_required;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_reason_only_for_type;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_reason_required;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_fk_only_for_type;
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_adjustment_fk_required;

ALTER TABLE bill_line_items
    DROP COLUMN IF EXISTS adjustment_note,
    DROP COLUMN IF EXISTS adjustment_reason_code,
    DROP COLUMN IF EXISTS adjustment_recovery_reading_id;

-- Restore 00024-era line_type CHECK (10 types, no ADJUSTMENT).
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_line_type_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_line_type_check
    CHECK (line_type IN (
        'ROOM_RENT', 'ELECTRICITY', 'WATER',
        'CLEANING_FEE', 'KEY_SERVICE',
        'PRORATE_RENT', 'PENALTY', 'PREPAID_CREDIT', 'OTHER',
        'OUTSTANDING_BILL'
    ));
