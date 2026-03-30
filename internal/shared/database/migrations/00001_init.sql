-- +goose Up

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Users
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    full_name VARCHAR(255) NOT NULL DEFAULT '',
    role VARCHAR(20) NOT NULL CHECK (role IN ('admin', 'manager')),
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- Refresh Tokens
CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id),
    token_hash VARCHAR(255) NOT NULL,
    family_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Apartments
CREATE TABLE apartments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    electricity_rate_per_unit BIGINT NOT NULL DEFAULT 0,
    water_rate_per_unit BIGINT NOT NULL DEFAULT 0,
    address_details TEXT NOT NULL DEFAULT '',
    province_id INT NOT NULL DEFAULT 0,
    district_id INT NOT NULL DEFAULT 0,
    subdistrict_id INT NOT NULL DEFAULT 0,
    tax_id VARCHAR(20) NOT NULL DEFAULT '',
    bank_name VARCHAR(100) NOT NULL DEFAULT '',
    bank_account_name VARCHAR(255) NOT NULL DEFAULT '',
    bank_account_number VARCHAR(50) NOT NULL DEFAULT '',
    promptpay_id VARCHAR(20),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- Rooms
CREATE TABLE rooms (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    apartment_id UUID NOT NULL REFERENCES apartments(id),
    number VARCHAR(20) NOT NULL,
    type VARCHAR(10) NOT NULL CHECK (type IN ('air', 'fan')),
    floor INT NOT NULL DEFAULT 1,
    base_rent BIGINT NOT NULL DEFAULT 0,
    base_deposit BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'VACANT',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE (apartment_id, number)
);

CREATE INDEX idx_rooms_apartment_id ON rooms(apartment_id);

-- Tenants
CREATE TABLE tenants (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    full_name VARCHAR(255) NOT NULL,
    id_card_number VARCHAR(20),
    phone VARCHAR(20),
    email VARCHAR(255),
    emergency_contact VARCHAR(255),
    emergency_phone VARCHAR(20),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- Contracts
CREATE TABLE contracts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    room_id UUID NOT NULL REFERENCES rooms(id),
    start_date DATE NOT NULL,
    min_months INT NOT NULL DEFAULT 6,
    monthly_rent BIGINT NOT NULL DEFAULT 0,
    deposit_amount BIGINT NOT NULL DEFAULT 0,
    deposit_status VARCHAR(20) NOT NULL DEFAULT 'COLLECTED',
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    end_date DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_contracts_tenant_id ON contracts(tenant_id);
CREATE INDEX idx_contracts_room_id ON contracts(room_id);
CREATE INDEX idx_contracts_status ON contracts(status);

-- Meter Readings
CREATE TABLE meter_readings (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    room_id UUID NOT NULL REFERENCES rooms(id),
    reading_date DATE NOT NULL,
    electricity_previous INT NOT NULL DEFAULT 0,
    electricity_current INT NOT NULL DEFAULT 0,
    water_previous INT NOT NULL DEFAULT 0,
    water_current INT NOT NULL DEFAULT 0,
    read_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_meter_readings_room_id ON meter_readings(room_id);
CREATE INDEX idx_meter_readings_reading_date ON meter_readings(reading_date);

-- Bills
CREATE TABLE bills (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    contract_id UUID NOT NULL REFERENCES contracts(id),
    room_id UUID NOT NULL REFERENCES rooms(id),
    billing_month VARCHAR(7) NOT NULL,
    bill_date DATE NOT NULL,
    type VARCHAR(20) NOT NULL CHECK (type IN ('MONTHLY', 'SETTLEMENT')),
    room_charge BIGINT NOT NULL DEFAULT 0,
    electricity_units INT NOT NULL DEFAULT 0,
    electricity_charge BIGINT NOT NULL DEFAULT 0,
    water_units INT NOT NULL DEFAULT 0,
    water_charge BIGINT NOT NULL DEFAULT 0,
    total_amount BIGINT NOT NULL DEFAULT 0,
    due_date DATE NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    deposit_deduction BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_bills_contract_id ON bills(contract_id);
CREATE INDEX idx_bills_billing_month ON bills(billing_month);
CREATE INDEX idx_bills_status ON bills(status);

-- Payments
CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    bill_id UUID NOT NULL REFERENCES bills(id),
    amount BIGINT NOT NULL DEFAULT 0,
    method VARCHAR(20) NOT NULL CHECK (method IN ('CASH', 'TRANSFER')),
    paid_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    received_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payments_bill_id ON payments(bill_id);

-- +goose Down

DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS bills;
DROP TABLE IF EXISTS meter_readings;
DROP TABLE IF EXISTS contracts;
DROP TABLE IF EXISTS tenants;
DROP TABLE IF EXISTS rooms;
DROP TABLE IF EXISTS apartments;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
DROP EXTENSION IF EXISTS "uuid-ossp";
