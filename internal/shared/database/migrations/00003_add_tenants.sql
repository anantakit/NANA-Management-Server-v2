-- +goose Up
-- Alter existing tenants table: rename/add/drop columns to match new schema

ALTER TABLE tenants RENAME COLUMN id_card_number TO id_card;
ALTER TABLE tenants ALTER COLUMN id_card SET NOT NULL;
ALTER TABLE tenants ALTER COLUMN id_card TYPE VARCHAR(13);
ALTER TABLE tenants ADD CONSTRAINT tenants_id_card_unique UNIQUE (id_card);

ALTER TABLE tenants ALTER COLUMN phone SET NOT NULL;

ALTER TABLE tenants ADD COLUMN address TEXT NOT NULL DEFAULT '';

ALTER TABLE tenants ALTER COLUMN emergency_contact SET NOT NULL;
ALTER TABLE tenants ALTER COLUMN emergency_contact SET DEFAULT '';
ALTER TABLE tenants DROP COLUMN IF EXISTS emergency_phone;
ALTER TABLE tenants DROP COLUMN IF EXISTS email;

CREATE INDEX IF NOT EXISTS idx_tenants_id_card ON tenants(id_card);
CREATE INDEX IF NOT EXISTS idx_tenants_full_name ON tenants(full_name);

-- +goose Down
DROP INDEX IF EXISTS idx_tenants_full_name;
DROP INDEX IF EXISTS idx_tenants_id_card;
ALTER TABLE tenants DROP COLUMN IF EXISTS address;
ALTER TABLE tenants ADD COLUMN email VARCHAR(255);
ALTER TABLE tenants ADD COLUMN emergency_phone VARCHAR(20);
ALTER TABLE tenants ALTER COLUMN emergency_contact DROP NOT NULL;
ALTER TABLE tenants ALTER COLUMN emergency_contact DROP DEFAULT;
ALTER TABLE tenants ALTER COLUMN phone DROP NOT NULL;
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_id_card_unique;
ALTER TABLE tenants ALTER COLUMN id_card TYPE VARCHAR(20);
ALTER TABLE tenants ALTER COLUMN id_card DROP NOT NULL;
ALTER TABLE tenants RENAME COLUMN id_card TO id_card_number;
