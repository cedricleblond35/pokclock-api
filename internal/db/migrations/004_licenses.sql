-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS licenses (
    id                uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    club_id           uuid NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    license_key       text NOT NULL UNIQUE,
    -- hash SHA-256 du worker_token (HMAC secret), jamais en clair
    worker_token_hash text NOT NULL,
    -- bind à un hardware_id côté client (NULL avant 1ère activation)
    hardware_id       text,
    role              text NOT NULL DEFAULT 'member'
                      CHECK (role IN ('member', 'admin', 'superadmin')),
    status            text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'revoked', 'expired')),
    expires_at        timestamptz,
    last_seen_at      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz
);

CREATE INDEX IF NOT EXISTS idx_licenses_club_id ON licenses(club_id);
CREATE INDEX IF NOT EXISTS idx_licenses_status ON licenses(status);
CREATE INDEX IF NOT EXISTS idx_licenses_hardware_id ON licenses(hardware_id)
    WHERE hardware_id IS NOT NULL;

CREATE TRIGGER trg_licenses_updated_at
    BEFORE UPDATE ON licenses
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_licenses_updated_at ON licenses;
DROP TABLE IF EXISTS licenses;
-- +goose StatementEnd
