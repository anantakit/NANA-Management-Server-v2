-- +goose Up
-- Phase 1B (Decide the month) — stores the operator's per-room decision
-- whether a PENDING_DECISION room should be included in or skipped from
-- the monthly billing cycle. Per pre-spec walk locks:
--
--  * Storage = INCLUDE / SKIP / absence (no UNDECIDED enum — absence is the
--    Undecided state). Reverting to Undecided = DELETE the row.
--  * UNIQUE (room_id, billing_month) — one decision per (room, month).
--  * Reversal boundary = bill existence for (room, month). Enforced in the
--    service layer (the FK target is the room, not the bill).
--  * Decided_by FK → users so attribution can be joined for inline display
--    on the row ("เพิ่มโดยคุณ · 15 มิ.ย."). ON DELETE SET NULL so removing a
--    user account doesn't cascade-delete history.
--
-- No soft-delete: revert IS the deletion; the workspace shows the decision
-- by joining at read time (1A's GET endpoint reads this table — PD shrinks
-- as decisions land per phasing doc).

CREATE TABLE room_billing_decisions (
    id            uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    room_id       uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    billing_month varchar(7) NOT NULL,
    decision      varchar(20) NOT NULL,
    decided_at    timestamptz NOT NULL DEFAULT now(),
    decided_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT room_billing_decisions_decision_check
        CHECK (decision IN ('INCLUDE', 'SKIP')),
    CONSTRAINT room_billing_decisions_month_check
        CHECK (billing_month ~ '^[0-9]{4}-(0[1-9]|1[0-2])$')
);

-- One decision per (room, month) — primary access pattern + upsert anchor.
CREATE UNIQUE INDEX uq_room_billing_decisions_room_month
    ON room_billing_decisions(room_id, billing_month);

-- Apartment-month bulk fetch in classification path filters by billing_month
-- before joining rooms; this index makes that scan cheap.
CREATE INDEX idx_room_billing_decisions_month
    ON room_billing_decisions(billing_month);

-- +goose Down
DROP INDEX IF EXISTS idx_room_billing_decisions_month;
DROP INDEX IF EXISTS uq_room_billing_decisions_room_month;
DROP TABLE IF EXISTS room_billing_decisions;
