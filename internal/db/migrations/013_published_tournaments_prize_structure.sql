-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-β.2 : structure des prix affichée publiquement.
--
-- Snapshot calculé côté app PokClock au moment de la publication (utilise
-- IBlindService.CalculatePayouts existant) puis poussé en JSONB opaque.
-- L'app est source de vérité sur la formule de payout — backend juste persiste.
--
-- Schéma attendu :
--   {
--     "prizePool": "500.00",
--     "currency": "EUR",
--     "rakeAmount": "20.00",
--     "places": [
--       {"position": 1, "amount": "250.00", "percentage": 50.0},
--       {"position": 2, "amount": "150.00", "percentage": 30.0},
--       {"position": 3, "amount": "100.00", "percentage": 20.0}
--     ]
--   }
--
-- Nullable (NULL = pas encore poussé, l'admin a un vieux client ou n'a pas
-- configuré de payout). Le site public skip la section Prix dans ce cas.

ALTER TABLE published_tournaments
    ADD COLUMN prize_structure jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE published_tournaments DROP COLUMN IF EXISTS prize_structure;
-- +goose StatementEnd
