-- +goose Up
-- Replace Meter — first-class physical continuity event (audit R1–R4.5).
--
-- Doctrine: REPLACE_METER_PREBUILD_INVARIANT_CHALLENGE.md §FINAL BUILD LOCKS.
-- A meter replacement is a PHYSICAL_REPLACEMENT anchor row on the meter_readings
-- timeline (reuses the existing anchor marker + re-anchor lineage mechanic —
-- migration 00038). Per replaced utility U the anchor carries:
--   * {U}_previous = {U}_current = new_initial  (forward baseline; re-anchor)
--   * {U}_old_previous / {U}_old_final          (NEW — the FROZEN old-meter
--     boundary; tail = old_final − old_previous). Frozen at capture so a later
--     lineage mutation (recovery retract, move-out cancel) can NEVER move a
--     closed old-meter segment (R4.5 Q1). old_previous is snapshotted, NOT
--     re-derived from lineage.
--   * predecessor_reading_id — provenance ONLY (never calculation authority),
--     ON DELETE SET NULL so it can never block a legit cancel/retract.
--
-- The old-meter boundary lives on the meter EVENT (a source-of-truth row), so
-- this is recording a physical observation, NOT a bill snapshot feeding lineage
-- (does not violate feedback_meter_lineage_hardening).

ALTER TABLE meter_readings
    ADD COLUMN electricity_replaced     boolean NOT NULL DEFAULT false,
    ADD COLUMN water_replaced           boolean NOT NULL DEFAULT false,
    ADD COLUMN electricity_old_previous int,
    ADD COLUMN electricity_old_final    int,
    ADD COLUMN water_old_previous        int,
    ADD COLUMN water_old_final           int,
    ADD COLUMN predecessor_reading_id    uuid
        REFERENCES meter_readings(id) ON DELETE SET NULL;

-- The per-utility "which meter did the operator physically replace" flags are
-- the event's declared truth (needed for correct per-utility Recovery×Replacement
-- collision detection, and to distinguish a replaced-but-unreadable utility from
-- an untouched one — both leave old_final NULL). True ONLY on PHYSICAL_REPLACEMENT
-- rows, and such a row must replace at least one utility.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_replaced_flags_gated
        CHECK (
            (anchor_reason = 'PHYSICAL_REPLACEMENT' AND (electricity_replaced OR water_replaced))
            OR (anchor_reason IS DISTINCT FROM 'PHYSICAL_REPLACEMENT'
                AND NOT electricity_replaced AND NOT water_replaced)
        );

-- A frozen old-meter boundary can exist only for a utility that was replaced.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_old_boundary_requires_replaced
        CHECK (
            (electricity_old_final IS NULL OR electricity_replaced)
            AND (water_old_final IS NULL OR water_replaced)
        );

-- The frozen old-meter boundary columns belong ONLY to PHYSICAL_REPLACEMENT
-- rows — they must never leak onto normal readings or READING_RECOVERY anchors.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_old_boundary_replacement_only
        CHECK (
            (electricity_old_previous IS NULL AND electricity_old_final IS NULL
             AND water_old_previous IS NULL AND water_old_final IS NULL)
            OR anchor_reason = 'PHYSICAL_REPLACEMENT'
        );

-- A frozen old-meter boundary is a pair: old_final without old_previous (or vice
-- versa) is a corrupt segment. Per utility, both-or-neither.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_old_boundary_pair
        CHECK (
            (electricity_old_previous IS NULL) = (electricity_old_final IS NULL)
            AND (water_old_previous IS NULL) = (water_old_final IS NULL)
        );

-- Meter cannot run backwards on the old device: old_final >= old_previous
-- (equal = stuck/broken meter → tail 0, the honest floor; never negative).
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_old_boundary_not_negative
        CHECK (
            (electricity_old_final IS NULL OR electricity_old_final >= electricity_old_previous)
            AND (water_old_final IS NULL OR water_old_final >= water_old_previous)
        );

-- Relax the anchor uniqueness so it constrains ONLY Recovery. The "one re-anchor
-- per cycle" rule (migration 00041) is a READING_RECOVERY doctrine, NOT a
-- replacement one — a meter can legitimately be replaced more than once in a
-- month (R4.5 Q2 / owner §8: never impose a silent max-1-replacement/month).
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month_anchor;

CREATE UNIQUE INDEX idx_meter_readings_room_billing_month_anchor
ON meter_readings (room_id, billing_month)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL AND anchor_reason = 'READING_RECOVERY';

-- +goose Down
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month_anchor;

-- Restore the 00041 shape (one anchor per (room, month, anchor_reason)).
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month_anchor
ON meter_readings (room_id, billing_month, anchor_reason)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL AND anchor_reason IS NOT NULL;

ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_old_boundary_requires_replaced;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_replaced_flags_gated;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_old_boundary_not_negative;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_old_boundary_pair;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_old_boundary_replacement_only;

ALTER TABLE meter_readings
    DROP COLUMN IF EXISTS predecessor_reading_id,
    DROP COLUMN IF EXISTS water_old_final,
    DROP COLUMN IF EXISTS water_old_previous,
    DROP COLUMN IF EXISTS electricity_old_final,
    DROP COLUMN IF EXISTS electricity_old_previous,
    DROP COLUMN IF EXISTS water_replaced,
    DROP COLUMN IF EXISTS electricity_replaced;
