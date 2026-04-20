-- +goose Up
ALTER TABLE bills ADD COLUMN deposit_forfeited BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE bills DROP COLUMN IF EXISTS deposit_forfeited;
