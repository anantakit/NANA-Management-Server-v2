-- +goose Up
-- Reading Recovery Phase 5 — close the doctrine gap Phase 1 left:
-- READING_RECOVERY anchors MUST have previous = current (electricity + water).
--
-- Doctrine: feedback_reading_recovery_doctrine.md (line 33, locked 2026-06-22).
-- Phase 5 plan: /Users/anantakit/.claude/plans/hashed-gliding-crab.md (Lock A).
--
-- Two narrow CHECKs (one per meter) — one concern per CHECK, matching
-- Phase 1's per-concern CHECK precedent. Each violation message names
-- exactly which meter's invariant fired, making test failures grep-friendly.
--
-- Triple-guard pattern (DB CHECK + ValidateAnchor + service wiring) —
-- same defense-in-depth model Phase 1 used for anchor_note IS NOT NULL,
-- recovery_source_required, and recovery_no_self_reference.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_recovery_elec_prev_eq_curr
        CHECK (anchor_reason != 'READING_RECOVERY'
            OR electricity_previous = electricity_current);

ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_recovery_water_prev_eq_curr
        CHECK (anchor_reason != 'READING_RECOVERY'
            OR water_previous = water_current);

-- +goose Down
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recovery_water_prev_eq_curr;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recovery_elec_prev_eq_curr;
