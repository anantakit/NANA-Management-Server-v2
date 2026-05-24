-- +goose Up

-- Make the supersede self-FK DEFERRABLE INITIALLY DEFERRED so the
-- correction service can void the old bill BEFORE creating the new one
-- (required to clear the partial UNIQUE INDEX idx_bills_unique_monthly
-- which only allows ONE non-VOID monthly bill per (contract_id,
-- billing_month)) without the FK rejecting the intermediate
-- "superseded_by_bill_id points at a row that doesn't exist yet" state.
--
-- The FK is still enforced at COMMIT — invalid chains never escape a
-- transaction. This only relaxes the WITHIN-TX check.

ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_superseded_by_bill_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_superseded_by_bill_id_fkey
    FOREIGN KEY (superseded_by_bill_id) REFERENCES bills(id)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose Down
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_superseded_by_bill_id_fkey;
ALTER TABLE bills ADD CONSTRAINT bills_superseded_by_bill_id_fkey
    FOREIGN KEY (superseded_by_bill_id) REFERENCES bills(id);
