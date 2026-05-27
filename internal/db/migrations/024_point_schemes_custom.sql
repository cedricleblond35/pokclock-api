-- +goose Up
-- +goose StatementBegin
-- Phase 0.D-γ.3 : ajoute le type 'custom' aux barèmes de points.
--
-- params attendus côté Go pour ce type : { "formula": "expression NCalc" }
--
-- Le calcul reste côté WPF (NCalc déjà utilisé pour les leaderboards perso
-- de l'app desktop) pour éviter la divergence d'évaluateurs cross-platform.
-- Côté backend, on stocke la formule comme metadata et on refuse l'auto-calc
-- via point_scheme : POST /tournaments/:id/results retourne une erreur
-- demandant au caller d'envoyer les points explicites.
--
-- Variables disponibles dans la formule (côté WPF NCalc) :
--   position    : position du joueur (1 = vainqueur)
--   players     : nombre total de joueurs au tournoi
--   buyIn       : montant du buy-in en €
--   prizePool   : prize pool total après rake en €

ALTER TABLE point_schemes
    DROP CONSTRAINT point_schemes_type_check;

ALTER TABLE point_schemes
    ADD CONSTRAINT point_schemes_type_check
    CHECK (type IN ('linear', 'fixed', 'proportional', 'custom'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE point_schemes
    DROP CONSTRAINT point_schemes_type_check;

ALTER TABLE point_schemes
    ADD CONSTRAINT point_schemes_type_check
    CHECK (type IN ('linear', 'fixed', 'proportional'));
-- +goose StatementEnd
