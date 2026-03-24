-- +goose Up

-- Simplify address: drop province/district/subdistrict IDs, rename address_details → address
ALTER TABLE apartments DROP COLUMN province_id;
ALTER TABLE apartments DROP COLUMN district_id;
ALTER TABLE apartments DROP COLUMN subdistrict_id;
ALTER TABLE apartments RENAME COLUMN address_details TO address;

-- Remove bank columns from apartments (moved to apartment_bank_accounts)
ALTER TABLE apartments DROP COLUMN bank_name;
ALTER TABLE apartments DROP COLUMN bank_account_name;
ALTER TABLE apartments DROP COLUMN bank_account_number;
ALTER TABLE apartments DROP COLUMN promptpay_id;

-- Bank accounts per apartment (supports multiple)
CREATE TABLE apartment_bank_accounts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    apartment_id UUID NOT NULL REFERENCES apartments(id),
    bank_name VARCHAR(100) NOT NULL,
    account_name VARCHAR(255) NOT NULL,
    account_number VARCHAR(50) NOT NULL,
    promptpay_id VARCHAR(20),
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_apartment_bank_accounts_apartment_id ON apartment_bank_accounts(apartment_id);

-- +goose Down

DROP TABLE IF EXISTS apartment_bank_accounts;

ALTER TABLE apartments ADD COLUMN bank_name VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE apartments ADD COLUMN bank_account_name VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE apartments ADD COLUMN bank_account_number VARCHAR(50) NOT NULL DEFAULT '';
ALTER TABLE apartments ADD COLUMN promptpay_id VARCHAR(20);

ALTER TABLE apartments RENAME COLUMN address TO address_details;
ALTER TABLE apartments ADD COLUMN province_id INT NOT NULL DEFAULT 0;
ALTER TABLE apartments ADD COLUMN district_id INT NOT NULL DEFAULT 0;
ALTER TABLE apartments ADD COLUMN subdistrict_id INT NOT NULL DEFAULT 0;
