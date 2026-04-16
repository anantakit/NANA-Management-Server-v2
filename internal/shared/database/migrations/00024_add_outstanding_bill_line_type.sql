-- +goose Up
-- Add OUTSTANDING_BILL to bill_line_items.line_type CHECK.
-- Used by settlement absorption to represent bills carried into the settlement.
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_line_type_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_line_type_check
    CHECK (line_type IN (
        'ROOM_RENT', 'ELECTRICITY', 'WATER',
        'CLEANING_FEE', 'KEY_SERVICE',
        'PRORATE_RENT', 'PENALTY', 'PREPAID_CREDIT', 'OTHER',
        'OUTSTANDING_BILL'
    ));

-- +goose Down
ALTER TABLE bill_line_items DROP CONSTRAINT IF EXISTS bill_line_items_line_type_check;
ALTER TABLE bill_line_items ADD CONSTRAINT bill_line_items_line_type_check
    CHECK (line_type IN (
        'ROOM_RENT', 'ELECTRICITY', 'WATER',
        'CLEANING_FEE', 'KEY_SERVICE',
        'PRORATE_RENT', 'PENALTY', 'PREPAID_CREDIT', 'OTHER'
    ));
