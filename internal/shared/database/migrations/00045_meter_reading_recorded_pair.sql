-- +goose Up
-- Q1.5 Over-Record Re-model (P2a) — persist the RECORDED value on the recovery row.
--
-- Reading Recovery corrects an over-record (recorded > physical). To compute the
-- deterministic refund without lineage and without depending on the (narrative-
-- only, optional) source, the recovery row must carry the previously-recorded
-- (wrong) value directly. Lock A (prev = current = physical) is preserved — these
-- are additional, dedicated columns, not a repurposing of previous/current.
--
-- recorded is per-utility and independent (electricity may be corrected while
-- water is not). NULL = that utility was not corrected. Only READING_RECOVERY
-- rows may carry recorded; and recorded may never be below the physical current
-- (recorded < current would be an under-record, which is out of scope — L1).
--
-- Doctrine: project_reading_recovery_overrecord_model_lock (§0a #1, §0b).
-- Unpushed branch → dev DBs reset; no data migration.

ALTER TABLE meter_readings
    ADD COLUMN electricity_recorded int,
    ADD COLUMN water_recorded       int;

-- Exclusivity: only READING_RECOVERY rows may carry recorded values.
-- IS NOT DISTINCT FROM (not `=`) so a NULL anchor_reason (MONTHLY/EXIT rows)
-- yields FALSE, not NULL — otherwise `NULL OR FALSE` = NULL and the CHECK would
-- pass, letting a non-recovery row carry recorded values.
ALTER TABLE meter_readings ADD CONSTRAINT meter_readings_recorded_only_for_recovery
    CHECK (anchor_reason IS NOT DISTINCT FROM 'READING_RECOVERY'
        OR (electricity_recorded IS NULL AND water_recorded IS NULL));

-- No under-record: when present, recorded must be >= the physical current.
-- (recorded = current is an unaffected utility; recorded > current is the
-- over-record that drives a refund. recorded < current is rejected.)
-- NOTE: cross-column invariant. Raising a recovery row's current above its
-- recorded value via the meter Update path would trip this CHECK (a correction
-- must be re-decided, not silently outgrown) — surfaces as a DB error, so any
-- future editable-recovery path should guard it as a domain rule first.
ALTER TABLE meter_readings ADD CONSTRAINT meter_readings_recorded_not_below_current
    CHECK (
        (electricity_recorded IS NULL OR electricity_recorded >= electricity_current)
        AND (water_recorded IS NULL OR water_recorded >= water_current));

-- +goose Down
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recorded_not_below_current;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recorded_only_for_recovery;
ALTER TABLE meter_readings DROP COLUMN IF EXISTS water_recorded;
ALTER TABLE meter_readings DROP COLUMN IF EXISTS electricity_recorded;
