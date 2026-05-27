-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.2 : relier les inscriptions tournoi à un compte joueur.
--
-- Nullable car on garde la rétro-compat avec les inscriptions anonymes
-- (flow Phase 0.D-α qui utilise cancel_token via email). Quand le joueur
-- est connecté au moment de l'inscription, on remplit player_id pour pouvoir
-- lister "mes inscriptions" et permettre la désinscription via compte.
--
-- INDEX UNIQUE PARTIEL : empêche un même joueur de s'inscrire deux fois à un
-- même tournoi (status pending ou confirmed). Annulations OK : on peut se
-- réinscrire après avoir annulé.

ALTER TABLE tournament_registrations
    ADD COLUMN player_id uuid REFERENCES players(id) ON DELETE SET NULL;

CREATE INDEX tournament_registrations_player_id_idx
    ON tournament_registrations (player_id)
    WHERE player_id IS NOT NULL;

CREATE UNIQUE INDEX tournament_registrations_player_active_uniq
    ON tournament_registrations (published_tournament_id, player_id)
    WHERE player_id IS NOT NULL AND status IN ('pending', 'confirmed');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS tournament_registrations_player_active_uniq;
DROP INDEX IF EXISTS tournament_registrations_player_id_idx;
ALTER TABLE tournament_registrations DROP COLUMN IF EXISTS player_id;
-- +goose StatementEnd
