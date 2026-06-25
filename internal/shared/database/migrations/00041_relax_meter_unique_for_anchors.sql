-- +goose Up
-- Reading Recovery Phase 6 — close the doctrine gap caught by browser smoke
-- 2026-06-24: the original (room_id, billing_month) WHERE reading_type='MONTHLY'
-- unique index (00013) rejected recovery anchor rows that landed on the same
-- (room, current_month) as the operational MONTHLY reading produced by the
-- monthly-batch flow. Phase 5 integration tests (which seeded a DRAFT bill
-- WITHOUT a current-month MONTHLY reading) missed this because they took a
-- shortcut around the real monthly-batch ordering.
--
-- Doctrine resolution: recovery rows (anchor_reason='READING_RECOVERY') are
-- conceptually distinct from consumption rows per doctrine line 36-37
-- ("The recovery month is NOT a consumption month — it is a re-anchor event").
-- The uniqueness predicate should constrain consumption rows only.
--
-- The lineage / analytics invariants are unchanged:
--   - FindLatestByRoomID orders by (billing_month, created_at) DESC, so the
--     recovery row (created later) wins as the new latest anchor in the
--     same cycle (Phase 2 B2 still passes — next-month inheritance reads
--     recovery's prev=curr).
--   - Analytics pool excludes anchor_reason IS NOT NULL rows, so the
--     consumption row stays in baseline computation and the recovery row
--     does not (Phase 2 B3/B4 unchanged).
--
-- See feedback_reading_recovery_doctrine.md + the matching integration
-- anchor B6 (new) that pins the coexistence shape in
-- service_recovery_coexists_with_current_monthly_integration_test.go.

DROP INDEX IF EXISTS idx_meter_readings_room_billing_month;

-- Two-pronged unique surface:
--   1. consumption rows: one per (room, month). anchor rows excluded so a
--      RECOVERY can land on the same cycle as the consumption reading.
--   2. anchor rows: at most one RECOVERY per (room, month) — the doctrine's
--      "one re-anchor per cycle" rule (operator double-clicking still
--      gets a unique-violation rather than a silent dupe row).
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month
ON meter_readings (room_id, billing_month)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL AND anchor_reason IS NULL;

CREATE UNIQUE INDEX idx_meter_readings_room_billing_month_anchor
ON meter_readings (room_id, billing_month, anchor_reason)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL AND anchor_reason IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_meter_readings_room_billing_month_anchor;
DROP INDEX IF EXISTS idx_meter_readings_room_billing_month;

-- Restore original (00013) shape — rejects anchor coexistence again.
CREATE UNIQUE INDEX idx_meter_readings_room_billing_month
ON meter_readings (room_id, billing_month)
WHERE reading_type = 'MONTHLY' AND deleted_at IS NULL;
