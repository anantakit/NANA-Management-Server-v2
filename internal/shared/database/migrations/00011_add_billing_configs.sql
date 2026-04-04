-- +goose Up
CREATE TABLE billing_configs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    apartment_id UUID NOT NULL REFERENCES apartments(id),
    fee_type VARCHAR(30) NOT NULL,
    default_amount BIGINT NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_billing_configs_apartment_fee_type ON billing_configs(apartment_id, fee_type) WHERE deleted_at IS NULL;
CREATE INDEX idx_billing_configs_apartment_id ON billing_configs(apartment_id) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS billing_configs;
