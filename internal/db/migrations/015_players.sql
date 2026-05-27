-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.1 : comptes joueurs.
--
-- Un player = un compte créé via magic link email ou Google OAuth. Indépendant
-- des `clubs.email` (qui sert à l'activation license desktop) et des
-- `tournament_registrations` historiques (qui restent anonymes par cancel_token
-- pour rétro-compat). 0.E.2 reliera tournament_registrations.player_id.
--
-- google_id : sub renvoyé par Google OAuth (stable, ne change pas si l'email
-- change). Nullable car possible de créer un compte uniquement par magic link.
-- L'UNIQUE garantit qu'un compte Google ne crée pas de doublon.
--
-- email_verified : true automatiquement après magic link consommée OU après
-- Google OAuth (Google vérifie l'email). False par défaut (un PATCH /me peut
-- changer l'email mais re-déclenche une vérification).

CREATE TABLE players (
    id              uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    email           text NOT NULL UNIQUE,
    google_id       text UNIQUE,
    first_name      text NOT NULL DEFAULT '',
    last_name       text NOT NULL DEFAULT '',
    email_verified  bool NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'banned', 'deleted')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX players_email_lower_idx ON players (lower(email));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS players;
-- +goose StatementEnd
