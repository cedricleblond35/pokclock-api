-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-α.4 : contrainte d'unicité (club_id, local_tournament_id) pour
-- permettre l'upsert depuis l'app desktop.
--
-- Avant cette migration, un re-clic sur "Publier en ligne" créait une
-- nouvelle ligne à chaque fois → doublons sur le site public.
-- Maintenant POST devient idempotent : si une ligne existe déjà pour
-- (club_id, local_tournament_id), on UPDATE au lieu de INSERT.
--
-- WHERE local_tournament_id IS NOT NULL : on autorise plusieurs publications
-- sans local_id (cas hypothétique de tournois créés directement via API
-- sans contre-partie locale).

CREATE UNIQUE INDEX idx_published_tournaments_local_unique
    ON published_tournaments(club_id, local_tournament_id)
    WHERE local_tournament_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_published_tournaments_local_unique;
-- +goose StatementEnd
