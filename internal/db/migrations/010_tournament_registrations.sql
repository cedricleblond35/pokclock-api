-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-α — inscriptions publiques sur les tournois publiés.
--
-- Lifecycle :
--   pending    : créée via /public/.../register, en attente de modération admin
--   confirmed  : admin a confirmé (l'app desktop importera en Player local au moment voulu)
--   rejected   : admin a refusé (motif optionnel renseigné)
--   cancelled  : l'inscrit a annulé via le lien magique (cancel_token)
--
-- cancel_token : token urlsafe random 32+ chars, transmis à l'inscrit dans
-- l'email de confirmation. GET /public/registrations/:token/cancel passe en
-- 'cancelled'. Unique + index pour lookup O(1).
--
-- Pas de FK explicite vers members (option a verrouillée : online registrant
-- = Player local, pas Member). Pas de notion de license non plus côté inscrit.

CREATE TABLE tournament_registrations (
    id                       uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    published_tournament_id  uuid NOT NULL REFERENCES published_tournaments(id) ON DELETE CASCADE,
    first_name               text NOT NULL,
    last_name                text NOT NULL,
    email                    text NOT NULL,
    phone                    text,
    status                   text NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'confirmed', 'rejected', 'cancelled')),
    moderated_by_license     text,
    moderated_at             timestamptz,
    moderation_reason        text,
    cancel_token             text NOT NULL UNIQUE,
    registered_at            timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_tournament_registrations_tournament ON tournament_registrations(published_tournament_id);
CREATE INDEX idx_tournament_registrations_status ON tournament_registrations(status);
CREATE INDEX idx_tournament_registrations_email ON tournament_registrations(email);

CREATE TRIGGER trg_tournament_registrations_updated_at
    BEFORE UPDATE ON tournament_registrations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_tournament_registrations_updated_at ON tournament_registrations;
DROP INDEX IF EXISTS idx_tournament_registrations_email;
DROP INDEX IF EXISTS idx_tournament_registrations_status;
DROP INDEX IF EXISTS idx_tournament_registrations_tournament;
DROP TABLE IF EXISTS tournament_registrations;
-- +goose StatementEnd
