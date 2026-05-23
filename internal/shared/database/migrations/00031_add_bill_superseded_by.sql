-- +goose Up

-- Forward link from old bill → new DRAFT created via the correction flow.
-- When admin corrects a FINALIZED bill (void+recreate), the old row is
-- voided with void_reason='CORRECTION' and superseded_by_bill_id is set
-- to the new bill's id. The new bill leaves this column NULL.
--
-- Reverse direction ("which bill replaced X") is served by indexed query
-- on this column OR the audit log CREATE_FROM_CORRECTION event — no
-- second column maintained (avoids sync risk + invariant complexity).
-- See project_billing_correction_arch_lock.md.
ALTER TABLE bills ADD COLUMN superseded_by_bill_id UUID;

-- FK self-reference. No cascade — preserve chain integrity (bills are
-- soft-deleted only, and the chain itself is part of the audit trail).
ALTER TABLE bills ADD CONSTRAINT bills_superseded_by_bill_id_fkey
    FOREIGN KEY (superseded_by_bill_id) REFERENCES bills(id);

-- Defensive guard against self-reference loops at the row level.
ALTER TABLE bills ADD CONSTRAINT chk_bills_no_self_supersede
    CHECK (superseded_by_bill_id IS NULL OR superseded_by_bill_id != id);

-- Partial index — only corrected bills carry the link. Used for the
-- reverse query "find the bill superseded by X" served by the bill
-- detail endpoint when the FE needs to render "ออกแทนใบเดิม".
CREATE INDEX idx_bills_superseded_by_bill_id
    ON bills(superseded_by_bill_id)
    WHERE superseded_by_bill_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_bills_superseded_by_bill_id;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS chk_bills_no_self_supersede;
ALTER TABLE bills DROP CONSTRAINT IF EXISTS bills_superseded_by_bill_id_fkey;
ALTER TABLE bills DROP COLUMN IF EXISTS superseded_by_bill_id;
