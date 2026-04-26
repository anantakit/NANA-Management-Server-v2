-- +goose Up

-- Phase 1 of move-out Step 3 (การเงิน): record HOW payment moved (cash vs transfer).
-- Nullable to support skipped settlements (PaymentOutcome=null) and ZERO_BALANCE
-- (no money movement → no method).
ALTER TABLE move_out_notices
  ADD COLUMN payment_method VARCHAR(20);

ALTER TABLE move_out_notices
  ADD CONSTRAINT chk_payment_method CHECK (
    payment_method IS NULL
    OR payment_method IN ('CASH', 'TRANSFER')
  );

-- +goose Down

ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_payment_method;

ALTER TABLE move_out_notices
  DROP COLUMN IF EXISTS payment_method;
