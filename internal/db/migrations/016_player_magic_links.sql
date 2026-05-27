-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.1 : magic links pour login passwordless.
--
-- token = 32 bytes random base64url (256 bits), généré côté Go.
-- expires_at = now() + 15 min. Single-use : used_at est setté au verify et
-- bloque la réutilisation (anti-replay).
--
-- email stocké en clair pour pouvoir résoudre/créer le player au verify.
-- Le token lui n'est pas hashé : non bruteforçable (256 bits), à vie courte.
-- Si on voulait défense en profondeur on hasherait (SHA-256 côté DB), mais
-- en cas de leak DB l'attaquant aurait aussi accès à `players` donc inutile.

CREATE TABLE player_magic_links (
    token       text PRIMARY KEY,
    email       text NOT NULL,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX player_magic_links_email_idx ON player_magic_links (lower(email), created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS player_magic_links;
-- +goose StatementEnd
