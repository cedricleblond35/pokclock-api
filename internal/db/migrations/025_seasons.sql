-- +goose Up
-- +goose StatementBegin
-- Phase 0.G : saisons / championnats club.
--
-- Une saison = une période (start_at → end_at) pendant laquelle un ensemble
-- de tournois publiés contribuent à un leaderboard agrégé. Permet aux clubs
-- de structurer leur année (saison hiver, championnat printemps, etc.) plutôt
-- que d'avoir un seul leaderboard cumulatif depuis le début des temps.
--
-- slug : URL-safe, unique par club. Utilisé dans /public/clubs/:slug/saisons/:season_slug
-- status : 'upcoming' (avant start_at) / 'active' (dans la période) / 'finished'
--          (après end_at). Calculé côté code mais on stocke aussi pour pouvoir
--          fermer une saison manuellement avant end_at.

CREATE TABLE seasons (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    club_id     uuid NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    slug        text NOT NULL,
    name        text NOT NULL,
    description text,
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    status      text NOT NULL DEFAULT 'upcoming'
                CHECK (status IN ('upcoming', 'active', 'finished', 'archived')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (club_id, slug),
    CHECK (ends_at > starts_at)
);

CREATE INDEX seasons_club_idx ON seasons (club_id);
CREATE INDEX seasons_status_idx ON seasons (club_id, status);

CREATE TRIGGER trg_seasons_updated_at
    BEFORE UPDATE ON seasons
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS seasons;
-- +goose StatementEnd
