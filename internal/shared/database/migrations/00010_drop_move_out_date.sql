-- +goose Up
ALTER TABLE contracts DROP COLUMN IF EXISTS move_out_date;

-- +goose Down
ALTER TABLE contracts ADD COLUMN move_out_date DATE;
