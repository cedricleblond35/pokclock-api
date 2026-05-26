-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-α — flags d'inscription en ligne + slug public par club.
--
-- slug : utilisé pour les URLs publiques pokclock.com/clubs/:slug/tournois.
--        Auto-généré depuis le nom au moment de la migration (kebab-case).
--        Modifiable par le super-admin ensuite via PATCH /api/admin/clubs/:id.
-- online_registrations_enabled : opt-in par club. Si false, les endpoints
--        publics retournent 403 et l'app n'affiche pas le bouton "Publier en ligne".

ALTER TABLE clubs ADD COLUMN slug text;
ALTER TABLE clubs ADD COLUMN online_registrations_enabled boolean NOT NULL DEFAULT false;

-- Backfill slug depuis name : minuscules, non-alphanum → '-', trim
UPDATE clubs SET slug = TRIM(BOTH '-' FROM LOWER(REGEXP_REPLACE(name, '[^a-zA-Z0-9]+', '-', 'g')));

-- Dédupe si plusieurs clubs ont le même slug après backfill (improbable mais robuste).
-- On suffixe avec les 4 premiers chars de l'UUID pour les doublons.
UPDATE clubs SET slug = slug || '-' || SUBSTR(id::text, 1, 4)
WHERE id IN (
    SELECT id FROM (
        SELECT id, ROW_NUMBER() OVER (PARTITION BY slug ORDER BY created_at) AS rn
        FROM clubs
    ) ranked
    WHERE ranked.rn > 1
);

ALTER TABLE clubs ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX idx_clubs_slug ON clubs(slug);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_clubs_slug;
ALTER TABLE clubs DROP COLUMN IF EXISTS online_registrations_enabled;
ALTER TABLE clubs DROP COLUMN IF EXISTS slug;
-- +goose StatementEnd
