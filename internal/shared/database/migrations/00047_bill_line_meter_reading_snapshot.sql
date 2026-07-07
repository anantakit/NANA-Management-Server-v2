-- +goose Up
-- Snapshot the meter reading (previous → current) on the ELECTRICITY / WATER
-- bill line so every surface (tenant PNG, delivery, admin) can show the reading
-- that produced the charge — parallel to the recovery refund line's evidence.
--
-- A bill is a frozen financial document: persisting the reading on the line (vs
-- a read-time JOIN to meter_readings) keeps the displayed figures stable even if
-- the meter row is later edited (e.g. a baseline correction). Nullable — only
-- metered AUTO lines carry them; ROOM_RENT / MANUAL / ADJUSTMENT stay NULL.
--
-- Unpushed branch → dev DBs reset; no data migration. Bills generated before
-- this simply render without the reading line (fields NULL).

ALTER TABLE bill_line_items
    ADD COLUMN meter_previous int,
    ADD COLUMN meter_current  int;

-- +goose Down
ALTER TABLE bill_line_items DROP COLUMN IF EXISTS meter_current;
ALTER TABLE bill_line_items DROP COLUMN IF EXISTS meter_previous;
