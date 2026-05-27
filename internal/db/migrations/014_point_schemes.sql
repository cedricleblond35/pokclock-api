-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-γ.1 : barème de points par club.
--
-- Un club = un barème (MVP). Le club admin configure son schéma de points dans
-- l'app desktop, on persiste ici, le site public l'affiche en lecture seule sur
-- la page détail d'un tournoi.
--
-- Trois types supportés en γ.1 :
--   - linear      : decroit linéairement de maxPoints à 0 sur cutoffPosition places
--                   params: { "maxPoints": 100, "cutoffPosition": 20 }
--   - fixed       : tableau fixe par position (jusqu'à length)
--                   params: { "points": [10, 7, 5, 3, 1] }
--   - proportional: 1er = basePoints + multiplier × totalPlayers, dégradation
--                   params: { "basePoints": 50, "multiplier": 2 }
--
-- γ.2 ajoutera "custom" (formule cellule de calcul) avec un sandbox safe.

CREATE TABLE point_schemes (
    club_id    uuid PRIMARY KEY REFERENCES clubs(id) ON DELETE CASCADE,
    name       text NOT NULL,
    type       text NOT NULL CHECK (type IN ('linear', 'fixed', 'proportional')),
    params     jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS point_schemes;
-- +goose StatementEnd
