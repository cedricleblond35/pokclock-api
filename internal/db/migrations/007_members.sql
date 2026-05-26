-- +goose Up
-- +goose StatementBegin
-- Phase 0.C-γ partie 3 — table members + FK license → member.
--
-- Sémantique :
--   - members : individus rattachés à un club (croupiers, dealers, admin de
--     club). Un club a 0..N membres. Indépendant de licenses : un membre peut
--     exister sans license (pré-création), une license peut exister sans
--     membre (rétro-compat / license non attribuée).
--   - license.member_id : FK optionnelle. ON DELETE SET NULL pour ne pas
--     révoquer automatiquement une license quand un membre part — l'admin
--     décide explicitement (réassigner à un nouvel arrivant ou révoquer).
--
-- L'email d'activation reste l'email du club (cf. décision Phase 0.C-γ-3).
-- member.email est purement business : contact direct, notifs futures.

CREATE TABLE IF NOT EXISTS members (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    club_id     uuid NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    email       text,
    notes       text,
    status      text NOT NULL DEFAULT 'active'
                CHECK (status IN ('active', 'inactive')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_members_club_id ON members(club_id);
CREATE INDEX idx_members_status ON members(status);

-- Trigger updated_at (réutilise la fonction set_updated_at créée en 001)
CREATE TRIGGER trg_members_updated_at
    BEFORE UPDATE ON members
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS member_id uuid REFERENCES members(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_licenses_member_id ON licenses(member_id)
    WHERE member_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_licenses_member_id;
ALTER TABLE licenses DROP COLUMN IF EXISTS member_id;

DROP TRIGGER IF EXISTS trg_members_updated_at ON members;
DROP INDEX IF EXISTS idx_members_status;
DROP INDEX IF EXISTS idx_members_club_id;
DROP TABLE IF EXISTS members;
-- +goose StatementEnd
