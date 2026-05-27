package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// publicResultsHandler : Phase 0.E.4. Endpoints anonymes pour résultats et
// leaderboard. AUCUNE auth, rate-limit en amont.
type publicResultsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type publicTournamentResult struct {
	Position    int     `json:"position"`
	FirstName   string  `json:"firstName"`
	LastName    string  `json:"lastName"`
	Points      int     `json:"points"`
	PrizeAmount *string `json:"prizeAmount,omitempty"`
}

// getResults : GET /public/clubs/:slug/tournaments/:id/results
// Liste les résultats d'un tournoi publié. Nom complet exposé (les joueurs
// classés acceptent implicitement la publication via la publication des
// résultats par l'admin du club).
func (h *publicResultsHandler) getResults(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	tid := strings.TrimSpace(c.Param("id"))
	if slug == "" || tid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, _, ok, err := pt.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT r.position, r.first_name, r.last_name, r.points, r.prize_amount
		 FROM tournament_results r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 WHERE t.id = $1 AND t.club_id = $2
		 ORDER BY r.position ASC`,
		tid, clubID,
	)
	if err != nil {
		h.logger.Error("public results query", "err", err, "slug", slug, "tid", tid)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]publicTournamentResult, 0, 32)
	for rows.Next() {
		var r publicTournamentResult
		if err := rows.Scan(&r.Position, &r.FirstName, &r.LastName, &r.Points, &r.PrizeAmount); err != nil {
			h.logger.Error("scan public result", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, map[string]any{"results": out})
}

type leaderboardEntry struct {
	Rank         int    `json:"rank"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	TotalPoints  int    `json:"totalPoints"`
	TournamentsPlayed int `json:"tournamentsPlayed"`
	Wins         int    `json:"wins"`
}

// getLeaderboard : GET /public/clubs/:slug/leaderboard?limit=50
// Agrège les résultats de tous les tournois publiés du club. Tri par
// total_points DESC, tie-break sur tournaments_played DESC.
//
// On groupe par (lower(first_name), lower(last_name)) pour fusionner les
// inscriptions anonymes du même joueur. Quand 0.E.4 sera plus mature, on
// pourra grouper par player_id pour les joueurs avec compte (et fallback
// nom pour les anonymes).
func (h *publicResultsHandler) getLeaderboard(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, clubName, ok, err := pt.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), 50, 200)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT
		   MIN(r.first_name) AS first_name,
		   MIN(r.last_name)  AS last_name,
		   SUM(r.points)::int AS total_points,
		   COUNT(*)::int     AS tournaments_played,
		   COUNT(*) FILTER (WHERE r.position = 1)::int AS wins
		 FROM tournament_results r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 WHERE t.club_id = $1
		 GROUP BY lower(r.first_name), lower(r.last_name)
		 ORDER BY total_points DESC, tournaments_played DESC, first_name ASC
		 LIMIT $2`,
		clubID, limit,
	)
	if err != nil {
		h.logger.Error("leaderboard query", "err", err, "slug", slug)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]leaderboardEntry, 0, limit)
	rank := 0
	for rows.Next() {
		rank++
		var e leaderboardEntry
		e.Rank = rank
		if err := rows.Scan(&e.FirstName, &e.LastName, &e.TotalPoints, &e.TournamentsPlayed, &e.Wins); err != nil {
			h.logger.Error("scan leaderboard", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, e)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"club":        map[string]any{"id": clubID, "name": clubName, "slug": slug},
		"entries":     out,
	})
}
