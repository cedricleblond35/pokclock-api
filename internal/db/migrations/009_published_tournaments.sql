-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-α — tournois publiés en ligne par les apps PokClock.
--
-- Un tournoi "publié" est une copie statique d'un tournoi local de l'app
-- desktop. La sync est one-shot via le bouton "Publier en ligne" : l'app
-- POST les champs business du tournoi vers ce table, et l'objet vit
-- ensuite sa propre vie (pas de re-sync auto).
--
-- local_tournament_id : id Sqlite côté app, informatif pour permettre à
-- l'app de retrouver son tournoi local pour re-publier (PATCH).

CREATE TABLE published_tournaments (
    id                    uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    club_id               uuid NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    name                  text NOT NULL,
    description           text,
    start_at              timestamptz NOT NULL,
    buy_in_amount         numeric(10, 2) NOT NULL DEFAULT 0,
    buy_in_fees           numeric(10, 2) NOT NULL DEFAULT 0,
    starting_stack        integer NOT NULL DEFAULT 0,
    max_players           integer NOT NULL DEFAULT 0,
    format                text NOT NULL DEFAULT 'Freezeout',
    late_reg_enabled      boolean NOT NULL DEFAULT true,
    late_reg_until_level  integer,
    local_tournament_id   integer,
    -- État du tournoi côté public :
    --   open      : inscriptions ouvertes (avant start_at)
    --   closed    : inscriptions fermées (auto-fermées à start_at ou manuel)
    --   cancelled : annulé par l'admin (visible mais barré, plus d'inscriptions)
    status                text NOT NULL DEFAULT 'open'
                          CHECK (status IN ('open', 'closed', 'cancelled')),
    -- Snapshot des blinds pour affichage public éventuel (résumé level 1 + durée niveau)
    blinds_summary        jsonb,
    -- Audit : qui a publié, quand
    published_by_license  text NOT NULL,
    published_at          timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_published_tournaments_club_id ON published_tournaments(club_id);
CREATE INDEX idx_published_tournaments_status ON published_tournaments(status);
CREATE INDEX idx_published_tournaments_start_at ON published_tournaments(start_at);

-- Trigger updated_at (réutilise set_updated_at créé en 001)
CREATE TRIGGER trg_published_tournaments_updated_at
    BEFORE UPDATE ON published_tournaments
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_published_tournaments_updated_at ON published_tournaments;
DROP INDEX IF EXISTS idx_published_tournaments_start_at;
DROP INDEX IF EXISTS idx_published_tournaments_status;
DROP INDEX IF EXISTS idx_published_tournaments_club_id;
DROP TABLE IF EXISTS published_tournaments;
-- +goose StatementEnd
