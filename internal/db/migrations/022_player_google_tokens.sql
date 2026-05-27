-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.5 : tokens Google Calendar par joueur.
--
-- Un joueur = une connexion Google Calendar (1-to-1, PK player_id). Permet
-- d'auto-créer les events tournoi dans son agenda à l'inscription, et de
-- les supprimer/mettre à jour selon la lifecycle.
--
-- access_token : court (1h Google). refresh_token : long terme, utilisé pour
-- rafraîchir access_token quand expiré. Stockés en clair : la DB est sur
-- réseau privé Swarm, l'attaquant qui y accède a déjà accès à tout. Si on
-- veut une couche supplémentaire plus tard, pgcrypto + secret KMS.
--
-- scope : on stocke la liste de scopes accordés (en pratique
-- "openid email profile https://www.googleapis.com/auth/calendar.events").
-- Permet de détecter une révocation côté Google quand l'API renvoie 401/403.

CREATE TABLE player_google_tokens (
    player_id      uuid PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    access_token   text NOT NULL,
    refresh_token  text NOT NULL,
    token_type     text NOT NULL DEFAULT 'Bearer',
    expires_at     timestamptz NOT NULL,
    scope          text NOT NULL DEFAULT '',
    connected_at   timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_player_google_tokens_updated_at
    BEFORE UPDATE ON player_google_tokens
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS player_google_tokens;
-- +goose StatementEnd
