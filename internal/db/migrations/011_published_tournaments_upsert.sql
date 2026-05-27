-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-α.4 : contrainte d'unicité (club_id, local_tournament_id) pour
-- permettre l'upsert depuis l'app desktop.
--
-- Avant cette migration, un re-clic sur "Publier en ligne" créait une
-- nouvelle ligne à chaque fois → doublons sur le site public.
--
-- Étape 1 : nettoyer les doublons existants en cancelant tout sauf la
-- publication la plus récente (status='cancelled' garde la ligne pour audit
-- mais elle disparaît du site public). On dédupe par (club_id, local_id)
-- pour ne pas perdre les publications de tournois différents qui auraient
-- partagé un local_id (improbable mais on est strict).
--
-- Étape 2 : index unique PARTIEL sur les actifs uniquement.
-- WHERE status != 'cancelled' permet à un user de Dépublier puis Republier
-- sans collision (la ligne cancelled reste pour audit, une nouvelle active
-- est créée). WHERE local_tournament_id IS NOT NULL autorise des
-- publications cloud sans contre-partie locale (cas hypothétique).

UPDATE published_tournaments
SET status = 'cancelled', updated_at = now()
WHERE id IN (
    SELECT id FROM (
        SELECT id, ROW_NUMBER() OVER (
            PARTITION BY club_id, local_tournament_id
            ORDER BY published_at DESC
        ) AS rn
        FROM published_tournaments
        WHERE local_tournament_id IS NOT NULL
          AND status <> 'cancelled'
    ) ranked
    WHERE ranked.rn > 1
);

CREATE UNIQUE INDEX idx_published_tournaments_local_active
    ON published_tournaments(club_id, local_tournament_id)
    WHERE local_tournament_id IS NOT NULL AND status <> 'cancelled';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_published_tournaments_local_active;
-- Note : le UPDATE de dédupe n'est pas réversible (pas d'undo des cancels)
-- car on a perdu l'info de l'état initial. Acceptable car migration mostly
-- forward-only en pratique.
-- +goose StatementEnd
