-- +goose Up
ALTER TABLE bills
    ADD COLUMN payment_bank_name      VARCHAR(100),
    ADD COLUMN payment_account_number VARCHAR(50),
    ADD COLUMN payment_account_name   VARCHAR(255);

-- +goose Down
ALTER TABLE bills
    DROP COLUMN IF EXISTS payment_bank_name,
    DROP COLUMN IF EXISTS payment_account_number,
    DROP COLUMN IF EXISTS payment_account_name;
