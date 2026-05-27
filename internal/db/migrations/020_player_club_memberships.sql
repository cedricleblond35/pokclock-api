-- +goose Up
-- +goose StatementBegin
-- Phase 0.F.a : adhésion d'un joueur à un club.
--
-- Un joueur peut être membre de plusieurs clubs (UNIQUE par paire). Permet de
-- restreindre des tournois "members_only" (cf. migration 021).
--
-- Flow standard :
--   1. Joueur authentifié POST /api/players/me/clubs/:slug/join → row status='pending'
--   2. Admin club voit la demande dans WPF Mon club > MEMBRES JOUEURS
--   3. Admin approve → status='active' (moderated_by_license = sa license_key)
--      ou reject → status='rejected' (avec moderation_reason optionnel)
--   4. Plus tard l'admin peut révoquer une membership active → status='revoked'
--
-- Distinction avec la table `members` existante : `members` = staff du club
-- (croupiers, dealers, admins) géré à la main par l'admin. `player_club_memberships`
-- = adhésion d'un compte joueur (player_id), un joueur peut être membre sans
-- jamais avoir été dans `members`.
--
-- CASCADE sur player_id et club_id : si on supprime le joueur ou le club,
-- la membership disparaît. Pas d'historique conservé (différent de
-- tournament_registrations qui garde une trace).

CREATE TABLE player_club_memberships (
    id                      uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    player_id               uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    club_id                 uuid NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    status                  text NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'active', 'rejected', 'revoked')),
    requested_at            timestamptz NOT NULL DEFAULT now(),
    moderated_by_license    text,
    moderated_at            timestamptz,
    moderation_reason       text,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (player_id, club_id)
);

CREATE INDEX player_club_memberships_player_idx ON player_club_memberships (player_id);
CREATE INDEX player_club_memberships_club_status_idx ON player_club_memberships (club_id, status);

CREATE TRIGGER trg_player_club_memberships_updated_at
    BEFORE UPDATE ON player_club_memberships
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS player_club_memberships;
-- +goose StatementEnd
