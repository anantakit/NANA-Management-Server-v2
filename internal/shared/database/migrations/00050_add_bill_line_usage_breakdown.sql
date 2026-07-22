-- +goose Up
-- Replace Meter — frozen per-line usage breakdown (bill-time evidence).
--
-- When a metered line's usage spans a meter replacement, its consumption comes
-- from >1 physical segment (old-meter tail + new-meter reading) and the single
-- meter_previous/meter_current pair (migration 00047) can no longer explain it.
-- usage_breakdown snapshots the segments {kind, previous, current, usage} that
-- CanonicalPeriodUsage produced AT generation, from the SAME result object as the
-- line's amount — so an already-FINALIZED/PAID bill keeps explaining its total
-- (120 + 80 = 200) even if the replacement event / meter lineage changes later.
--
-- Stable billing evidence schema (NOT a serialized domain object): billing owns
-- UsageBreakdownSegment; no internal IDs / lineage fields. Nullable — populated
-- only on metered lines whose usage spans a replacement; ordinary lines keep
-- meter_previous/meter_current and leave this NULL. One line = one charge = one
-- override authority is unchanged (the breakdown is explanation, not a 2nd line).

ALTER TABLE bill_line_items
    ADD COLUMN usage_breakdown jsonb;

-- +goose Down
ALTER TABLE bill_line_items DROP COLUMN IF EXISTS usage_breakdown;
