-- +goose Up
-- +goose StatementBegin
-- Phase 0.C — rend club_id obligatoire sur structures et passe en unicité
-- composée (club_id, slug) : différents clubs peuvent partager le même slug.
--
-- Les structures existantes sont des données de test (cf. user confirmation
-- 2026-05-26). On les wipe pour éviter d'avoir à backfiller un club_id sur
-- des données qui n'ont pas de sens business.

TRUNCATE TABLE structures_audit;
TRUNCATE TABLE structures CASCADE;

ALTER TABLE structures
    ALTER COLUMN club_id SET NOT NULL;

-- Drop l'ancienne unicité globale sur slug (créée en 001_structures.sql via
-- la déclaration "slug text NOT NULL UNIQUE")
ALTER TABLE structures DROP CONSTRAINT IF EXISTS structures_slug_key;

-- Nouveau : unicité par club. Un slug "main" peut exister dans plusieurs clubs.
ALTER TABLE structures
    ADD CONSTRAINT structures_club_slug_unique UNIQUE (club_id, slug);

-- L'index partiel idx_structures_club_id créé en 005 (WHERE club_id IS NOT NULL)
-- devient redondant maintenant que club_id est NOT NULL : on recrée un index plein.
DROP INDEX IF EXISTS idx_structures_club_id;
CREATE INDEX idx_structures_club_id ON structures(club_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_structures_club_id;
CREATE INDEX idx_structures_club_id ON structures(club_id) WHERE club_id IS NOT NULL;

ALTER TABLE structures DROP CONSTRAINT IF EXISTS structures_club_slug_unique;
ALTER TABLE structures ADD CONSTRAINT structures_slug_key UNIQUE (slug);

ALTER TABLE structures ALTER COLUMN club_id DROP NOT NULL;
-- +goose StatementEnd
