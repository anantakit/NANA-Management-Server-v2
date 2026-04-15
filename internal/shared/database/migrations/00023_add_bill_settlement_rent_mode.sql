-- +goose Up
ALTER TABLE bills ADD COLUMN settlement_rent_mode VARCHAR(30) NOT NULL DEFAULT 'PRORATED';

-- +goose Down
ALTER TABLE bills DROP COLUMN settlement_rent_mode;
