-- +goose Up

-- Expand move_out_notices status from 3 values to 6 for V2 workflow.
-- PENDING → PENDING_METER (existing rows waiting for exit meter reading).
ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_move_out_status;

UPDATE move_out_notices
   SET status = 'PENDING_METER'
 WHERE status = 'PENDING';

ALTER TABLE move_out_notices
  ADD CONSTRAINT chk_move_out_status CHECK (
    status IN (
      'PENDING_METER',
      'PENDING_SETTLEMENT',
      'PENDING_PAYMENT',
      'READY_TO_CLOSE',
      'COMPLETED',
      'CANCELLED'
    )
  );

-- Update the unique partial index to cover all non-terminal statuses.
DROP INDEX IF EXISTS idx_move_out_notices_active_contract;
CREATE UNIQUE INDEX idx_move_out_notices_active_contract
    ON move_out_notices (contract_id)
    WHERE status NOT IN ('COMPLETED', 'CANCELLED') AND deleted_at IS NULL;

-- +goose Down

-- Revert: collapse new statuses back to PENDING.
ALTER TABLE move_out_notices DROP CONSTRAINT IF EXISTS chk_move_out_status;

UPDATE move_out_notices
   SET status = 'PENDING'
 WHERE status IN ('PENDING_METER', 'PENDING_SETTLEMENT', 'PENDING_PAYMENT', 'READY_TO_CLOSE');

ALTER TABLE move_out_notices
  ADD CONSTRAINT chk_move_out_status CHECK (
    status IN ('PENDING', 'COMPLETED', 'CANCELLED')
  );

DROP INDEX IF EXISTS idx_move_out_notices_active_contract;
CREATE UNIQUE INDEX idx_move_out_notices_active_contract
    ON move_out_notices (contract_id)
    WHERE status != 'CANCELLED' AND deleted_at IS NULL;
