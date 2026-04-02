-- +goose Up
ALTER TABLE meter_readings
    ADD COLUMN is_rollover_electricity BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN is_rollover_water BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE meter_readings
    DROP COLUMN is_rollover_electricity,
    DROP COLUMN is_rollover_water;
