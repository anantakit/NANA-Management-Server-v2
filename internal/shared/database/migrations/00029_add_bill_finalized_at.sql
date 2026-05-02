-- +goose Up

-- Records when a bill transitioned DRAFT → FINALIZED.
-- Nullable: DRAFT bills have not been finalized yet.
-- Stays set through PAID and VOID transitions (audit trail of "when did
-- this become a real receivable").
ALTER TABLE bills ADD COLUMN finalized_at TIMESTAMPTZ;

-- Partial index keeps the index small — most queries that care about
-- finalized_at are scoped to bills that have a value.
CREATE INDEX idx_bills_finalized_at ON bills(finalized_at)
    WHERE status IN ('FINALIZED', 'PAID');

-- Backfill: best-guess from created_at for bills already past DRAFT.
-- Lossy for bills that sat in DRAFT before being finalized (uncommon for
-- MONTHLY which finalizes on creation; common for SETTLEMENT drafts that
-- never made it past DRAFT — those correctly get NULL because they're not
-- in the WHERE clause).
UPDATE bills
SET finalized_at = created_at
WHERE status IN ('FINALIZED', 'PAID')
  AND finalized_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_bills_finalized_at;
ALTER TABLE bills DROP COLUMN finalized_at;
