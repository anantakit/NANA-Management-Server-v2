-- +goose Up

-- New columns for move-out V2 workflow tracking.
ALTER TABLE move_out_notices
  ADD COLUMN settlement_bill_id UUID REFERENCES bills(id),
  ADD COLUMN net_amount         BIGINT,
  ADD COLUMN payment_outcome    VARCHAR(20),
  ADD COLUMN payment_note       TEXT NOT NULL DEFAULT '',
  ADD COLUMN closed_at          TIMESTAMPTZ,
  ADD COLUMN last_action_by     UUID REFERENCES users(id),
  ADD COLUMN last_action_at     TIMESTAMPTZ;

ALTER TABLE move_out_notices
  ADD CONSTRAINT chk_payment_outcome CHECK (
    payment_outcome IS NULL
    OR payment_outcome IN ('PAID_EXTRA', 'REFUNDED', 'ZERO_BALANCE')
  );

CREATE INDEX idx_move_out_notices_settlement_bill
    ON move_out_notices (settlement_bill_id)
    WHERE settlement_bill_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_move_out_notices_settlement_bill;

ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_payment_outcome;

ALTER TABLE move_out_notices
  DROP COLUMN IF EXISTS last_action_at,
  DROP COLUMN IF EXISTS last_action_by,
  DROP COLUMN IF EXISTS closed_at,
  DROP COLUMN IF EXISTS payment_note,
  DROP COLUMN IF EXISTS payment_outcome,
  DROP COLUMN IF EXISTS net_amount,
  DROP COLUMN IF EXISTS settlement_bill_id;
