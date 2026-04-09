-- +goose Up

CREATE TABLE bill_generation_batches (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    apartment_id UUID NOT NULL REFERENCES apartments(id),
    billing_month VARCHAR(7) NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('COMPLETED', 'PARTIAL', 'FAILED')),
    total_contracts INT NOT NULL DEFAULT 0,
    created_count INT NOT NULL DEFAULT 0,
    already_exists_count INT NOT NULL DEFAULT 0,
    skipped_count INT NOT NULL DEFAULT 0,
    failed_count INT NOT NULL DEFAULT 0,
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_batches_apartment_month ON bill_generation_batches(apartment_id, billing_month);
CREATE INDEX idx_batches_created_at ON bill_generation_batches(created_at DESC);

CREATE TABLE bill_generation_batch_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    batch_id UUID NOT NULL REFERENCES bill_generation_batches(id) ON DELETE CASCADE,
    contract_id UUID NOT NULL,
    room_id UUID NOT NULL,
    room_number VARCHAR(20) NOT NULL,
    room_floor INT NOT NULL DEFAULT 0,
    result_type VARCHAR(20) NOT NULL CHECK (result_type IN ('CREATED', 'ALREADY_EXISTS', 'SKIPPED', 'FAILED')),
    reason_code VARCHAR(40) NOT NULL DEFAULT '',
    reason_text TEXT NOT NULL DEFAULT '',
    bill_id UUID REFERENCES bills(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_batch_items_batch_id ON bill_generation_batch_items(batch_id);

ALTER TABLE bills ADD COLUMN batch_id UUID REFERENCES bill_generation_batches(id);
CREATE INDEX idx_bills_batch_id ON bills(batch_id) WHERE batch_id IS NOT NULL;

-- +goose Down
ALTER TABLE bills DROP COLUMN IF EXISTS batch_id;
DROP TABLE IF EXISTS bill_generation_batch_items;
DROP TABLE IF EXISTS bill_generation_batches;
