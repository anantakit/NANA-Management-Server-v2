-- +goose Up
CREATE TABLE move_out_notices (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    contract_id UUID NOT NULL REFERENCES contracts(id),
    notice_date DATE NOT NULL,
    actual_move_out_date DATE NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,

    CONSTRAINT chk_move_out_status CHECK (status IN ('PENDING', 'COMPLETED', 'CANCELLED')),
    CONSTRAINT chk_move_out_date_order CHECK (actual_move_out_date >= notice_date)
);

-- One active (non-cancelled, non-deleted) notice per contract
CREATE UNIQUE INDEX idx_move_out_notices_active_contract
    ON move_out_notices (contract_id)
    WHERE status != 'CANCELLED' AND deleted_at IS NULL;

CREATE INDEX idx_move_out_notices_status ON move_out_notices (status) WHERE deleted_at IS NULL;
CREATE INDEX idx_move_out_notices_deleted_at ON move_out_notices (deleted_at);

-- +goose Down
DROP TABLE IF EXISTS move_out_notices;
