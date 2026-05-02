-- +goose Up

-- Track when a move-out notice transitioned to CANCELLED. Mirrors closed_at
-- for the COMPLETED transition. The frontend's CancelledBanner had been
-- using updated_at as a proxy, which drifts if any other field is updated
-- post-cancel — cancelled_at is the precise transition timestamp.
ALTER TABLE move_out_notices
  ADD COLUMN cancelled_at TIMESTAMPTZ;

-- +goose Down

ALTER TABLE move_out_notices
  DROP COLUMN IF EXISTS cancelled_at;
