-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-β.1 : affichage public de la liste des joueurs sur un tournoi publié.
--
-- show_players (bool, default false) : opt-in par tournoi. Si false, le site
-- public n'affiche que le compteur, pas les noms.
--
-- players_display_mode (text) : comment afficher les noms. Valeurs supportées :
--   - 'hidden'              : pas de noms (équivalent à show_players=false)
--   - 'pseudo'              : "PokerKing92" (anonyme, friendly)
--   - 'first_initial_last'  : "Jean D." (semi-anonyme)
--   - 'full_name'           : "Jean Dupont" (complet, demande consentement implicite)
--
-- local_players (jsonb) : snapshot des TournamentPlayer locaux poussés depuis
-- l'app PokClock à chaque publication. Schéma :
--   [{"firstName": "Jean", "lastName": "Dupont", "nickname": "PokerKing92"}, ...]
-- On stocke en JSONB plutôt que dans une table dédiée parce que :
--   1. Les joueurs locaux sont push one-shot avec le tournoi (pas de modif partielle)
--   2. Pas de besoin de query "tournois où Jean Dupont a joué" pour MVP
--   3. Évite une table + une jointure + une fk
-- Les inscriptions online restent dans tournament_registrations (séparé).

ALTER TABLE published_tournaments
    ADD COLUMN show_players bool NOT NULL DEFAULT false;

ALTER TABLE published_tournaments
    ADD COLUMN players_display_mode text NOT NULL DEFAULT 'hidden'
        CHECK (players_display_mode IN ('hidden', 'pseudo', 'first_initial_last', 'full_name'));

ALTER TABLE published_tournaments
    ADD COLUMN local_players jsonb NOT NULL DEFAULT '[]'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS local_players;
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS players_display_mode;
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS show_players;
-- +goose StatementEnd
