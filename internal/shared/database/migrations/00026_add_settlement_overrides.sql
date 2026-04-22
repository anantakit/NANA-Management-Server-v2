-- +goose Up
ALTER TABLE bills ADD COLUMN overrides jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE bills ADD COLUMN deposit_application varchar(10) NOT NULL DEFAULT 'FULL';
ALTER TABLE bills ADD COLUMN custom_deposit_applied bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE bills DROP COLUMN IF EXISTS custom_deposit_applied;
ALTER TABLE bills DROP COLUMN IF EXISTS deposit_application;
ALTER TABLE bills DROP COLUMN IF EXISTS overrides;
