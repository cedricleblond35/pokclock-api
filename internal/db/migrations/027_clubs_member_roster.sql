-- +goose Up
-- +goose StatementBegin
-- Phase 0.J : roster des membres permanents d'un club pousse depuis WPF.
--
-- Avant cette phase, l'auto-link Phase 0.H ne pouvait se faire que via le
-- snapshot local_players des tournois publies. Probleme : un nouveau membre
-- ajoute en debut de saison (avant tout tournoi publie) ne pouvait pas etre
-- auto-lie a son compte cloud.
--
-- Cette colonne stocke le roster complet des Players WPF avec MembershipType=Member
-- (les Guests sont exclus). L'app WPF push ce roster a chaque save d'un Member
-- via POST /api/club/roster. L'auto-link cloud scanne ce roster en plus de
-- local_players pour creer les pending memberships.
--
-- Structure : JSONB array de { firstName, lastName, nickname?, emailHash? }.
-- Les entries sans emailHash sont ignorees par l'auto-link.

ALTER TABLE clubs
    ADD COLUMN member_roster jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Index GIN pour les queries jsonb_array_elements rapides.
CREATE INDEX clubs_member_roster_gin_idx ON clubs USING GIN (member_roster);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS clubs_member_roster_gin_idx;
ALTER TABLE clubs DROP COLUMN IF EXISTS member_roster;
-- +goose StatementEnd
