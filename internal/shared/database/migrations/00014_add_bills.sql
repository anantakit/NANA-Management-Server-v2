-- +goose Up

-- Drop legacy bills/payments tables from 00001_init.sql (old schema, never used in production)
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS bills;

CREATE TABLE bills (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    contract_id UUID NOT NULL REFERENCES contracts(id),
    billing_month VARCHAR(7) NOT NULL,
    bill_type VARCHAR(20) NOT NULL CHECK (bill_type IN ('MONTHLY', 'SETTLEMENT')),
    status VARCHAR(20) NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'FINALIZED', 'PAID', 'VOID')),
    void_reason VARCHAR(100),
    deposit_amount BIGINT NOT NULL DEFAULT 0,
    deposit_balance BIGINT NOT NULL DEFAULT 0,
    total_amount BIGINT NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_bills_contract_id ON bills(contract_id);
CREATE INDEX idx_bills_status ON bills(status);
CREATE INDEX idx_bills_billing_month ON bills(billing_month);

-- Only one non-voided MONTHLY bill per contract per billing month
CREATE UNIQUE INDEX idx_bills_unique_monthly
    ON bills(contract_id, billing_month)
    WHERE bill_type = 'MONTHLY' AND status != 'VOID' AND deleted_at IS NULL;

-- Only one non-voided SETTLEMENT bill per contract per billing month
CREATE UNIQUE INDEX idx_bills_unique_settlement
    ON bills(contract_id, billing_month)
    WHERE bill_type = 'SETTLEMENT' AND status != 'VOID' AND deleted_at IS NULL;

CREATE TABLE bill_line_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    bill_id UUID NOT NULL REFERENCES bills(id) ON DELETE CASCADE,
    line_type VARCHAR(30) NOT NULL CHECK (line_type IN (
        'ROOM_RENT', 'ELECTRICITY', 'WATER',
        'CLEANING_FEE', 'KEY_SERVICE',
        'PRORATE_RENT', 'PENALTY', 'PREPAID_CREDIT', 'OTHER'
    )),
    description TEXT NOT NULL DEFAULT '',
    amount BIGINT NOT NULL,
    quantity INT NOT NULL DEFAULT 0,
    unit_price BIGINT NOT NULL DEFAULT 0,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_bill_line_items_bill_id ON bill_line_items(bill_id);

-- +goose Down
DROP TABLE IF EXISTS bill_line_items;
DROP TABLE IF EXISTS bills;
