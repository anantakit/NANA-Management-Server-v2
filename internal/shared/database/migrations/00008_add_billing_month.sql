-- +goose Up

-- billing_month is canonical source of truth for meter reading period (YYYY-MM)
-- replaces reading_date DATE — see 00009 for column drop

-- 1. Add new column (nullable first)
ALTER TABLE meter_readings ADD COLUMN billing_month VARCHAR(7);

-- 2. Backfill from reading_date (safe: skip NULLs then validate)
UPDATE meter_readings SET billing_month = TO_CHAR(reading_date, 'YYYY-MM')
WHERE reading_date IS NOT NULL;

-- 2b. Validate: no NULL billing_month after backfill
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM meter_readings WHERE billing_month IS NULL) THEN
        RAISE EXCEPTION 'Null billing_month after backfill — check rows with NULL reading_date';
    END IF;
END $$;
-- +goose StatementEnd

-- 3. Validate: no duplicates before making unique index
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT room_id, billing_month FROM meter_readings
        WHERE deleted_at IS NULL
        GROUP BY room_id, billing_month HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Duplicate room_id + billing_month found — fix data before migrating';
    END IF;
END $$;
-- +goose StatementEnd

-- 4. Make NOT NULL + CHECK constraint (lock format at DB level)
ALTER TABLE meter_readings ALTER COLUMN billing_month SET NOT NULL;
ALTER TABLE meter_readings
    ADD CONSTRAINT chk_billing_month_format
    CHECK (billing_month ~ '^\d{4}-(0[1-9]|1[0-2])$');

-- 5. Create UNIQUE index immediately (lock invariant before any new writes)
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month
    ON meter_readings(room_id, billing_month) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month;
ALTER TABLE meter_readings DROP CONSTRAINT IF EXISTS chk_billing_month_format;
ALTER TABLE meter_readings DROP COLUMN IF EXISTS billing_month;
