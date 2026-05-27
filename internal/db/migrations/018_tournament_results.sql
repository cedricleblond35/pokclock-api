-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.4 (γ.2 + leaderboard) : résultats finaux d'un tournoi publié.
--
-- Une ligne = un joueur classé. Position 1 = vainqueur. Permet le leaderboard
-- club (somme des points cross-tournois) via simple agrégation SQL.
--
-- player_id nullable : si l'admin publie une position pour un joueur "Jean D."
-- sans compte joueur lié, on garde le nom. Si player_id est renseigné, on
-- pourra cumuler les points sur son compte (tableau persoa).
--
-- points : calculé côté backend au moment du push selon le point_scheme du
-- club. Stocké pour figer la valeur (un changement de barème plus tard ne
-- réécrit pas les classements passés).
--
-- prize_amount : optionnel, montant remporté en string décimal ("250.00").
-- Remplit la table prizes si l'admin a publié la prize_structure.
--
-- UNIQUE (published_tournament_id, position) : pas deux 1ers, pas de doublon.

CREATE TABLE tournament_results (
    id                       uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    published_tournament_id  uuid NOT NULL REFERENCES published_tournaments(id) ON DELETE CASCADE,
    position                 int  NOT NULL CHECK (position >= 1),
    first_name               text NOT NULL,
    last_name                text NOT NULL,
    player_id                uuid REFERENCES players(id) ON DELETE SET NULL,
    points                   int  NOT NULL DEFAULT 0,
    prize_amount             text,
    created_at               timestamptz NOT NULL DEFAULT now(),
    UNIQUE (published_tournament_id, position)
);

CREATE INDEX tournament_results_tournament_idx ON tournament_results (published_tournament_id);
CREATE INDEX tournament_results_player_idx ON tournament_results (player_id) WHERE player_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tournament_results;
-- +goose StatementEnd
