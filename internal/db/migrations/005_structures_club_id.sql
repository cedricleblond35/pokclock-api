-- +goose Up
-- +goose StatementBegin
-- Ajoute le rattachement structure → club.
-- Nullable au début pour ne pas casser les structures existantes (Phase 0.A
-- avant l'introduction des clubs). À passer en NOT NULL dans une future
-- migration une fois le backfill effectué (Phase 0.C ou 1.0).
ALTER TABLE structures
    ADD COLUMN IF NOT EXISTS club_id uuid REFERENCES clubs(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_structures_club_id ON structures(club_id)
    WHERE club_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_structures_club_id;
ALTER TABLE structures DROP COLUMN IF EXISTS club_id;
-- +goose StatementEnd
