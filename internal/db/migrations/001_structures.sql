-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS structures (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug        text NOT NULL UNIQUE,
    name        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('club', 'casino', 'home', 'streamer', 'other')),
    settings    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_structures_kind ON structures(kind);

-- Trigger pour updated_at automatique
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_structures_updated_at
    BEFORE UPDATE ON structures
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_structures_updated_at ON structures;
DROP FUNCTION IF EXISTS set_updated_at();
DROP TABLE IF EXISTS structures;
-- +goose StatementEnd
