-- +goose Up

ALTER TABLE meter_readings
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN deleted_at TIMESTAMPTZ;

CREATE INDEX idx_meter_readings_deleted_at ON meter_readings(deleted_at);
CREATE UNIQUE INDEX idx_meter_readings_room_date ON meter_readings(room_id, reading_date) WHERE deleted_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_meter_readings_room_date;
DROP INDEX IF EXISTS idx_meter_readings_deleted_at;

ALTER TABLE meter_readings
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS deleted_at;
