-- +goose Up
-- +goose StatementBegin
-- Phase 0.E (hygiène) : purge les magic links déjà expirés ou consommés.
--
-- Les magic links sont à usage unique avec TTL 15 min. Au déploiement de cette
-- migration on supprime tout ce qui est :
--   - used_at NOT NULL (déjà consommé, plus jamais utile)
--   - expires_at < now() (expiré, ne peut plus être consommé)
--
-- Le cleanup continu est fait côté Go par une goroutine periodicMagicLinkCleanup
-- (cf. internal/api/cleanup.go) qui tourne toutes les 6h. Cette migration est
-- juste une remise à zéro propre à l'instant du déploiement.

DELETE FROM player_magic_links
 WHERE used_at IS NOT NULL
    OR expires_at < now();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Pas de rollback possible : les rows supprimées étaient mortes de toute façon.
SELECT 1;
-- +goose StatementEnd
