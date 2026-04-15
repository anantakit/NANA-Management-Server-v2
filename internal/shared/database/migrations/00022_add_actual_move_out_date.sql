-- +goose Up
ALTER TABLE move_out_notices
    ADD COLUMN actual_move_out_date DATE;

-- +goose Down
ALTER TABLE move_out_notices
    DROP COLUMN IF EXISTS actual_move_out_date;
