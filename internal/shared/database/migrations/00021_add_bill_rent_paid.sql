-- +goose Up
ALTER TABLE bills ADD COLUMN rent_paid BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE bills DROP COLUMN rent_paid;
