-- +goose Up
-- Reading Recovery — Source-Optional relaxation.
--
-- Scope:    reading-recovery-source-optional-migration-scope.md (locked 2026-07-01).
-- Ontology: feedback_meter_epistemology_root.md — never require historical
--           certainty to accept physical truth. Source demotes from a required
--           input gate to optional narrative metadata.
--
-- This is a RELAXATION, not a feature: the READING_RECOVERY ⟹ source-present
-- gate is dropped. No inference, heuristic, confidence, or advisory mechanism
-- replaces it. Absence of a source is a valid, complete recovery.
--
-- Deliberately UNTOUCHED (still enforced):
--   - meter_readings_recovery_no_self_reference (00038) — NULL passes cleanly.
--   - meter_readings_anchor_note_required (00038) — note stays mandatory.
--   - idx_meter_readings_recovery_source partial index (00038) — still backs
--     the analytics NOT-EXISTS exclusion subquery.
--   - meter_readings_recovery_{elec,water}_prev_eq_curr (00040, Lock A).
--   - meter_readings anchor/consumption uniqueness (00041).
--   - Column recovery_source_reading_id + its ON DELETE RESTRICT FK — the
--     column stays (already nullable at column level); the CHECK was the gate.
ALTER TABLE meter_readings
    DROP CONSTRAINT IF EXISTS meter_readings_recovery_source_required;

-- +goose Down
-- One-directional: re-adding the CHECK FAILS if any nil-source recovery row
-- exists (a recovery committed under the relaxed rule). No data backfill is
-- possible — nil source is a legitimate operator choice, not corruption.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_recovery_source_required
        CHECK (anchor_reason != 'READING_RECOVERY'
            OR recovery_source_reading_id IS NOT NULL);
