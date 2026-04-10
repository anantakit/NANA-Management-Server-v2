-- +goose Up
ALTER TABLE bill_generation_batch_items
    ADD COLUMN computed_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE bill_generation_batches
    ADD COLUMN commit_status VARCHAR(32) NULL,
    ADD COLUMN committed_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE bill_generation_batches
    DROP COLUMN IF EXISTS committed_at,
    DROP COLUMN IF EXISTS commit_status;

ALTER TABLE bill_generation_batch_items
    DROP COLUMN IF EXISTS computed_snapshot;
