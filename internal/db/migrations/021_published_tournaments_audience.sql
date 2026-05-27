-- +goose Up
-- +goose StatementBegin
-- Phase 0.F.a : audience d'un tournoi publié.
--
-- 'public'        : ouvert à tous (anonymes via cancel_token + comptes joueurs)
--                   = comportement par défaut, rétro-compat des tournois existants.
-- 'members_only'  : réservé aux joueurs ayant une membership active dans le club.
--                   Refuse les inscriptions anonymes (impossible de vérifier la
--                   membership sans player_id). Refuse les comptes joueurs sans
--                   membership active.

ALTER TABLE published_tournaments
    ADD COLUMN audience text NOT NULL DEFAULT 'public'
    CHECK (audience IN ('public', 'members_only'));

CREATE INDEX idx_published_tournaments_audience ON published_tournaments(audience)
    WHERE audience <> 'public';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_published_tournaments_audience;
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS audience;
-- +goose StatementEnd
