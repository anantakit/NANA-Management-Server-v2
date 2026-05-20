-- +goose Up
CREATE TABLE bill_audit_log (
    id uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    bill_id uuid NOT NULL REFERENCES bills(id) ON DELETE CASCADE,
    action varchar(30) NOT NULL,
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_bill_audit_bill_id ON bill_audit_log(bill_id, created_at DESC);
CREATE INDEX idx_bill_audit_actor_id ON bill_audit_log(actor_id, created_at DESC) WHERE actor_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_bill_audit_actor_id;
DROP INDEX IF EXISTS idx_bill_audit_bill_id;
DROP TABLE IF EXISTS bill_audit_log;
