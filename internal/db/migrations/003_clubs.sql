-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS clubs (
    id            uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    name          text NOT NULL UNIQUE,
    plan          text NOT NULL DEFAULT 'solo'
                  CHECK (plan IN ('solo', 'club', 'pro')),
    email         text,
    status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'suspended', 'deleted')),
    notes         text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    suspended_at  timestamptz
);

CREATE INDEX IF NOT EXISTS idx_clubs_status ON clubs(status);
CREATE INDEX IF NOT EXISTS idx_clubs_plan ON clubs(plan);

-- Réutilise la fonction set_updated_at() créée dans 001_structures.sql
CREATE TRIGGER trg_clubs_updated_at
    BEFORE UPDATE ON clubs
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_clubs_updated_at ON clubs;
DROP TABLE IF EXISTS clubs;
-- +goose StatementEnd
