-- +goose Up

-- Drop old indexes
DROP INDEX IF EXISTS idx_meter_readings_room_date;
DROP INDEX IF EXISTS idx_meter_readings_reading_date;

-- Drop old column
ALTER TABLE meter_readings DROP COLUMN reading_date;

-- Create non-unique index (UNIQUE already created in 00008)
CREATE INDEX idx_meter_readings_billing_month ON meter_readings(billing_month);

-- +goose Down

-- Restore reading_date (lossy: day info lost, uses 1st of month)
DROP INDEX IF EXISTS idx_meter_readings_billing_month;

ALTER TABLE meter_readings ADD COLUMN reading_date DATE;
UPDATE meter_readings SET reading_date = (billing_month || '-01')::DATE;
ALTER TABLE meter_readings ALTER COLUMN reading_date SET NOT NULL;

-- Restore old indexes (UNIQUE index on billing_month stays — 00008 down will drop it)
CREATE UNIQUE INDEX idx_meter_readings_room_date
    ON meter_readings(room_id, reading_date) WHERE deleted_at IS NULL;
CREATE INDEX idx_meter_readings_reading_date ON meter_readings(reading_date);
