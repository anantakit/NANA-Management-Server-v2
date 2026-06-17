-- +goose Up
CREATE TABLE payment_destination_rules (
    id             UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    apartment_id   UUID         NOT NULL REFERENCES apartments(id),
    bank_account_id UUID        NOT NULL REFERENCES apartment_bank_accounts(id),
    rule_type      VARCHAR(20)  NOT NULL CHECK (rule_type IN ('APARTMENT_DEFAULT', 'ROOM_RANGE', 'ROOM_OVERRIDE')),
    room_number    VARCHAR(20),           -- ROOM_OVERRIDE only
    range_start    VARCHAR(20),           -- ROOM_RANGE only
    range_end      VARCHAR(20),           -- ROOM_RANGE only
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);

-- Only one apartment-default rule per apartment (non-deleted)
CREATE UNIQUE INDEX idx_payment_rules_apartment_default
    ON payment_destination_rules (apartment_id)
    WHERE rule_type = 'APARTMENT_DEFAULT' AND deleted_at IS NULL;

-- Only one override rule per (apartment, room_number) (non-deleted)
CREATE UNIQUE INDEX idx_payment_rules_room_override
    ON payment_destination_rules (apartment_id, room_number)
    WHERE rule_type = 'ROOM_OVERRIDE' AND deleted_at IS NULL;

CREATE INDEX idx_payment_rules_apartment ON payment_destination_rules (apartment_id);

-- +goose Down
DROP TABLE IF EXISTS payment_destination_rules;
