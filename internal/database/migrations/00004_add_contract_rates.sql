-- +goose Up
ALTER TABLE contracts ADD COLUMN electricity_rate_per_unit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE contracts ADD COLUMN water_rate_per_unit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE contracts ADD COLUMN move_out_date DATE;

-- +goose Down
ALTER TABLE contracts DROP COLUMN move_out_date;
ALTER TABLE contracts DROP COLUMN water_rate_per_unit;
ALTER TABLE contracts DROP COLUMN electricity_rate_per_unit;
