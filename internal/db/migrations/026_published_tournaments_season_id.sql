-- +goose Up
-- +goose StatementBegin
-- Phase 0.G : rattachement optionnel d'un tournoi publié à une saison.
--
-- Nullable : un club peut publier un tournoi "hors-saison" (one-shot, special
-- event). Quand season_id est set, le tournoi compte pour le leaderboard
-- saison correspondant.
--
-- ON DELETE SET NULL : supprimer une saison ne supprime PAS les tournois qui
-- en faisaient partie. Ils deviennent "hors-saison" (season_id NULL). Permet
-- d'archiver une saison sans perdre l'historique des tournois.

ALTER TABLE published_tournaments
    ADD COLUMN season_id uuid REFERENCES seasons(id) ON DELETE SET NULL;

CREATE INDEX published_tournaments_season_idx ON published_tournaments(season_id)
    WHERE season_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS published_tournaments_season_idx;
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS season_id;
-- +goose StatementEnd
