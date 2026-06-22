-- +goose Up
-- Reading Recovery Phase 1 — additive anchor schema on meter_readings.
--
-- Doctrine:  feedback_reading_recovery_doctrine.md   (locked 2026-06-22)
-- Guard:     feedback_recovery_lineage_vs_analytics_split.md (locked 2026-06-22)
-- Plan:      /Users/anantakit/.claude/plans/mutable-swimming-firefly.md
--
-- Three new columns + four CHECK constraints + one partial index.
-- FIRST_ANCHOR is derived state (latest == nil), NOT an enum member —
-- the enum carries discontinuity reasons only.
ALTER TABLE meter_readings
    ADD COLUMN anchor_reason              varchar(30),
    ADD COLUMN anchor_note                text,
    ADD COLUMN recovery_source_reading_id uuid
        REFERENCES meter_readings(id) ON DELETE RESTRICT;

-- Enum constraint. varchar+CHECK matches project convention
-- (see 00033_add_room_billing_decisions.sql).
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_anchor_reason_check
        CHECK (anchor_reason IS NULL
            OR anchor_reason IN ('PHYSICAL_REPLACEMENT', 'READING_RECOVERY'));

-- DB enforces NOT NULL only. Whitespace policy lives in the domain
-- (ValidateAnchor uses strings.TrimSpace). Postgres trim() and Go
-- strings.TrimSpace do not normalize identical Unicode whitespace sets;
-- one authority per concern.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_anchor_note_required
        CHECK (anchor_reason IS NULL OR anchor_note IS NOT NULL);

-- READING_RECOVERY owns the source-row pointer (doctrine: forward-only
-- ownership). Recovery row must reference a source.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_recovery_source_required
        CHECK (anchor_reason != 'READING_RECOVERY'
            OR recovery_source_reading_id IS NOT NULL);

-- Single-row shape invariant: a recovery anchor cannot reference itself.
-- Works at INSERT because the GORM BeforeCreate hook sets m.ID before
-- the statement reaches Postgres (meterreading/model.go BeforeCreate).
-- Paired with ValidateAnchor's domain rule; the domain check skips when
-- m.ID == uuid.Nil (pre-BeforeCreate) and this CHECK is the safety net.
ALTER TABLE meter_readings
    ADD CONSTRAINT meter_readings_recovery_no_self_reference
        CHECK (recovery_source_reading_id IS NULL
            OR recovery_source_reading_id != id);

-- Partial index supports Phase 2's analytics-pool exclusion subquery
-- (rows referenced as another row's recovery source must be excluded
-- from the baseline pool).
CREATE INDEX idx_meter_readings_recovery_source
    ON meter_readings(recovery_source_reading_id)
    WHERE recovery_source_reading_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_meter_readings_recovery_source;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recovery_no_self_reference;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_recovery_source_required;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_anchor_note_required;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS meter_readings_anchor_reason_check;
ALTER TABLE meter_readings
    DROP COLUMN IF EXISTS recovery_source_reading_id,
    DROP COLUMN IF EXISTS anchor_note,
    DROP COLUMN IF EXISTS anchor_reason;
