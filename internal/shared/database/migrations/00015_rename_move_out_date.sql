-- +goose Up
ALTER TABLE move_out_notices RENAME COLUMN actual_move_out_date TO scheduled_move_out_date;
ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_move_out_date_order;
ALTER TABLE move_out_notices ADD CONSTRAINT chk_move_out_date_order CHECK (scheduled_move_out_date >= notice_date);

-- +goose Down
ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_move_out_date_order;
ALTER TABLE move_out_notices RENAME COLUMN scheduled_move_out_date TO actual_move_out_date;
ALTER TABLE move_out_notices ADD CONSTRAINT chk_move_out_date_order CHECK (actual_move_out_date >= notice_date);
