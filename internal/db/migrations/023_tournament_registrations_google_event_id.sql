-- +goose Up
-- +goose StatementBegin
-- Phase 0.E.5 : ID de l'event Google Calendar lié à une inscription.
--
-- Renseigné côté backend après création réussie de l'event via l'API Calendar.
-- Utilisé pour update (changement de titre/description quand confirmed) et
-- delete (quand rejected/cancelled).
--
-- NULL = pas d'event créé. Soit le joueur n'avait pas Calendar connecté au
-- moment de l'inscription, soit l'API a échoué (best-effort silencieux —
-- l'inscription DB est prioritaire sur la sync agenda).

ALTER TABLE tournament_registrations
    ADD COLUMN google_event_id text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tournament_registrations DROP COLUMN IF EXISTS google_event_id;
-- +goose StatementEnd
