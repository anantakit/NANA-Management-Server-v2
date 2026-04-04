-- +goose Up
-- Phase 3: Add EXIT reading type to meter_readings

-- Add reading_type column (MONTHLY = existing, EXIT = move-out readings)
ALTER TABLE meter_readings ADD COLUMN reading_type VARCHAR(10) NOT NULL DEFAULT 'MONTHLY';

-- Add reading_date_actual column (the exact date for EXIT readings)
ALTER TABLE meter_readings ADD COLUMN reading_date_actual DATE;

-- Make billing_month nullable (EXIT readings have no billing_month)
ALTER TABLE meter_readings ALTER COLUMN billing_month DROP NOT NULL;

-- Drop old billing_month format check (doesn't allow NULL)
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS chk_billing_month_format;

-- New billing_month format check (allows NULL)
ALTER TABLE meter_readings ADD CONSTRAINT chk_billing_month_format CHECK (
    billing_month IS NULL OR billing_month ~ '^\d{4}-(0[1-9]|1[0-2])$'
);

-- Enforce reading_type ↔ field consistency
ALTER TABLE meter_readings ADD CONSTRAINT chk_reading_type_fields CHECK (
    (reading_type = 'MONTHLY' AND billing_month IS NOT NULL AND reading_date_actual IS NULL)
    OR (reading_type = 'EXIT' AND reading_date_actual IS NOT NULL AND billing_month IS NULL)
);

-- Drop old unique index on (room_id, billing_month)
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month;

-- Unique: one MONTHLY reading per room per month
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month
ON meter_readings (room_id, billing_month)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL;

-- Unique: one EXIT reading per room per date
CREATE UNIQUE INDEX idx_meter_readings_room_exit_date
ON meter_readings (room_id, reading_date_actual)
WHERE reading_type = 'EXIT' AND deleted_at IS NULL;

-- Index for reading_type filter
CREATE INDEX idx_meter_readings_reading_type ON meter_readings (reading_type);

-- +goose Down

DROP INDEX IF EXISTS idx_meter_readings_reading_type;
DROP INDEX IF EXISTS idx_meter_readings_room_exit_date;
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month;

ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS chk_reading_type_fields;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS chk_billing_month_format;

-- Remove EXIT readings before restoring NOT NULL
DELETE FROM meter_readings WHERE reading_type = 'EXIT';

ALTER TABLE meter_readings ALTER COLUMN billing_month SET NOT NULL;

-- Restore original format check
ALTER TABLE meter_readings ADD CONSTRAINT chk_billing_month_format CHECK (
    billing_month ~ '^\d{4}-(0[1-9]|1[0-2])$'
);

-- Restore original unique index
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month
ON meter_readings (room_id, billing_month)
WHERE deleted_at IS NULL;

ALTER TABLE meter_readings DROP COLUMN IF EXISTS reading_date_actual;
ALTER TABLE meter_readings DROP COLUMN IF EXISTS reading_type;
