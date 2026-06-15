-- +goose Up

CREATE TABLE bill_payments (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    bill_id     UUID NOT NULL REFERENCES bills(id),
    amount      BIGINT NOT NULL,
    method      VARCHAR(20) NOT NULL CHECK (method IN ('CASH', 'TRANSFER')),
    note        TEXT NOT NULL DEFAULT '',
    paid_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    received_by UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- v1 invariant: one monthly bill = one payment (atomic, no partial).
-- Also serves as a concurrency guard — concurrent payment attempts on the
-- same bill produce a unique constraint violation, caught as 409.
CREATE UNIQUE INDEX bill_payments_bill_id_uidx ON bill_payments(bill_id);

-- +goose Down

DROP INDEX IF EXISTS bill_payments_bill_id_uidx;
DROP TABLE IF EXISTS bill_payments;
