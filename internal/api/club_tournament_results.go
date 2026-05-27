package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// clubTournamentResultsHandler : Phase 0.E.4. Publication des résultats finaux
// d'un tournoi clos. Réservé à admin de club (héritage du middleware sur
// /api/club/*).
type clubTournamentResultsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type resultEntry struct {
	Position    int     `json:"position"`
	FirstName   string  `json:"firstName"`
	LastName    string  `json:"lastName"`
	PlayerID    *string `json:"playerId,omitempty"`
	Points      *int    `json:"points,omitempty"`      // si fourni, override le calcul auto
	PrizeAmount *string `json:"prizeAmount,omitempty"` // "100.00"
}

type publishResultsRequest struct {
	Results []resultEntry `json:"results"`
	// Si true, on calcule les points via le point_scheme du club au lieu
	// d'utiliser ceux fournis dans l'entrée. Permet de garantir cohérence
	// avec le barème actuel.
	ApplyPointScheme bool `json:"applyPointScheme"`
	// Si true, marque le tournoi en "closed". Par défaut on ne change pas
	// le status (au cas où l'admin republie pour corriger).
	CloseTournament bool `json:"closeTournament"`
}

// publishResults : POST /api/club/tournaments/:id/results
// Stratégie : DELETE des anciens résultats + INSERT en bulk dans une tx.
// Plus simple que UPSERT par (tournament, position) et garantit l'atomicité.
func (h *clubTournamentResultsHandler) publishResults(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	tid := strings.TrimSpace(c.Param("id"))
	if tid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var req publishResultsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	if len(req.Results) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "empty_results"})
	}

	// Vérifie que le tournoi appartient bien au club du caller.
	var clubMatch bool
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM published_tournaments WHERE id = $1 AND club_id = $2)`,
		tid, claims.ClubID,
	).Scan(&clubMatch)
	if err != nil {
		h.logger.Error("check tournament ownership", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if !clubMatch {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tournament_not_found"})
	}

	// Charge le point_scheme du club si on doit calculer auto.
	var schemeType string
	var schemeParams []byte
	if req.ApplyPointScheme {
		err := h.pool.QueryRow(c.Request().Context(),
			`SELECT type, params FROM point_schemes WHERE club_id = $1`,
			claims.ClubID,
		).Scan(&schemeType, &schemeParams)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("fetch point scheme", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
		}
	}
	totalPlayers := len(req.Results)

	tx, err := h.pool.Begin(c.Request().Context())
	if err != nil {
		h.logger.Error("begin tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_tx"})
	}
	defer tx.Rollback(c.Request().Context())

	if _, err := tx.Exec(c.Request().Context(),
		`DELETE FROM tournament_results WHERE published_tournament_id = $1`, tid); err != nil {
		h.logger.Error("delete old results", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	for _, r := range req.Results {
		if r.Position < 1 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_position"})
		}
		first := strings.TrimSpace(r.FirstName)
		last := strings.TrimSpace(r.LastName)
		if first == "" && last == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_name"})
		}

		points := 0
		if req.ApplyPointScheme && schemeType != "" {
			points = calculatePointsForPosition(schemeType, schemeParams, r.Position, totalPlayers)
		} else if r.Points != nil {
			points = *r.Points
		}

		_, err := tx.Exec(c.Request().Context(),
			`INSERT INTO tournament_results
			   (published_tournament_id, position, first_name, last_name, player_id, points, prize_amount)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			tid, r.Position, first, last, r.PlayerID, points, r.PrizeAmount,
		)
		if err != nil {
			h.logger.Error("insert result", "err", err, "position", r.Position)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
		}
	}

	if req.CloseTournament {
		if _, err := tx.Exec(c.Request().Context(),
			`UPDATE published_tournaments SET status = 'closed', updated_at = now()
			 WHERE id = $1 AND status = 'open'`, tid); err != nil {
			h.logger.Error("close tournament", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
		}
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		h.logger.Error("commit tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_commit"})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"status":  "published",
		"count":   len(req.Results),
		"applied": req.ApplyPointScheme && schemeType != "",
	})
}

// calculatePointsForPosition : pendant Go du PointsCalculator côté WPF.
// Doublonner la logique évite que le backend dépende d'un push WPF pour
// avoir les bons points (utile si on veut un endpoint qui recalcule
// rétroactivement après changement de barème).
func calculatePointsForPosition(schemeType string, paramsJSON []byte, position, totalPlayers int) int {
	if position < 1 || totalPlayers < 1 || len(paramsJSON) == 0 {
		return 0
	}
	var p map[string]any
	if err := json.Unmarshal(paramsJSON, &p); err != nil {
		return 0
	}
	switch strings.ToLower(schemeType) {
	case "linear":
		max := getIntFromMap(p, "maxPoints", 0)
		cutoff := getIntFromMap(p, "cutoffPosition", 0)
		if max <= 0 || cutoff <= 0 || position > cutoff {
			return 0
		}
		t := float64(position-1) / float64(cutoff)
		return int(math.Round(float64(max) * (1.0 - t)))
	case "fixed":
		arr, ok := p["points"].([]any)
		if !ok || position > len(arr) {
			return 0
		}
		v, ok := arr[position-1].(float64)
		if !ok {
			return 0
		}
		return int(v)
	case "proportional":
		base := getIntFromMap(p, "basePoints", 0)
		mult := getIntFromMap(p, "multiplier", 0)
		first := base + mult*totalPlayers
		if first <= 0 || position > totalPlayers {
			return 0
		}
		if totalPlayers <= 1 {
			return first
		}
		t := float64(position-1) / float64(totalPlayers-1)
		return int(math.Round(float64(first) * (1.0 - t)))
	}
	return 0
}

func getIntFromMap(m map[string]any, key string, fallback int) int {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	f, ok := v.(float64)
	if !ok {
		return fallback
	}
	return int(f)
}
