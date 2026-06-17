-- +goose Up
CREATE TABLE bill_deliveries (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    bill_id      UUID        NOT NULL REFERENCES bills(id),
    actor_id     UUID        REFERENCES users(id) ON DELETE SET NULL,
    channel      TEXT        NOT NULL DEFAULT 'LINE_MANUAL',
    delivered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    note         TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX idx_bill_deliveries_bill_id      ON bill_deliveries(bill_id);
CREATE INDEX idx_bill_deliveries_delivered_at ON bill_deliveries(delivered_at DESC);

-- +goose Down
DROP TABLE IF EXISTS bill_deliveries;
