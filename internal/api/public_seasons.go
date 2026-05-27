package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// publicSeasonsHandler : Phase 0.G. Endpoints anonymes pour visualiser les
// saisons d'un club et leur leaderboard scopé.
type publicSeasonsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type publicSeason struct {
	ID               string    `json:"id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	Description      *string   `json:"description,omitempty"`
	StartsAt         time.Time `json:"startsAt"`
	EndsAt           time.Time `json:"endsAt"`
	Status           string    `json:"status"`
	TournamentsCount int       `json:"tournamentsCount"`
}

// list : GET /public/clubs/:slug/seasons
func (h *publicSeasonsHandler) list(c echo.Context) error {
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

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT s.id, s.slug, s.name, s.description, s.starts_at, s.ends_at, s.status,
		        COALESCE((SELECT COUNT(*) FROM published_tournaments p
		                   WHERE p.season_id = s.id AND p.status IN ('open','closed')), 0)
		 FROM seasons s
		 WHERE s.club_id = $1 AND s.status <> 'archived'
		 ORDER BY s.starts_at DESC`,
		clubID,
	)
	if err != nil {
		h.logger.Error("public list seasons", "err", err, "slug", slug)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]publicSeason, 0, 8)
	for rows.Next() {
		var s publicSeason
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Description,
			&s.StartsAt, &s.EndsAt, &s.Status, &s.TournamentsCount); err != nil {
			h.logger.Error("scan public season", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, s)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"club":    map[string]any{"id": clubID, "name": clubName, "slug": slug},
		"seasons": out,
	})
}

// getLeaderboard : GET /public/clubs/:slug/seasons/:season_slug
// Retourne le détail de la saison + leaderboard scopé aux tournois de cette
// saison (réutilise la même agrégation que publicResultsHandler.getLeaderboard
// mais avec un filtre sur season_id).
type seasonDetail struct {
	Season       publicSeason       `json:"season"`
	Entries      []leaderboardEntry `json:"entries"`
}

func (h *publicSeasonsHandler) getLeaderboard(c echo.Context) error {
	clubSlug := strings.TrimSpace(c.Param("slug"))
	seasonSlug := strings.TrimSpace(c.Param("season_slug"))
	if clubSlug == "" || seasonSlug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, _, ok, err := pt.resolveClub(c, clubSlug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	var s publicSeason
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT s.id, s.slug, s.name, s.description, s.starts_at, s.ends_at, s.status,
		        COALESCE((SELECT COUNT(*) FROM published_tournaments p
		                   WHERE p.season_id = s.id AND p.status IN ('open','closed')), 0)
		 FROM seasons s
		 WHERE s.club_id = $1 AND s.slug = $2`,
		clubID, seasonSlug,
	).Scan(&s.ID, &s.Slug, &s.Name, &s.Description, &s.StartsAt, &s.EndsAt, &s.Status, &s.TournamentsCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "season_not_found"})
		}
		h.logger.Error("public season fetch", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), 100, 500)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT MIN(r.first_name), MIN(r.last_name),
		        SUM(r.points)::int,
		        COUNT(*)::int,
		        COUNT(*) FILTER (WHERE r.position = 1)::int
		 FROM tournament_results r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 WHERE t.club_id = $1 AND t.season_id = $2
		 GROUP BY lower(r.first_name), lower(r.last_name)
		 ORDER BY SUM(r.points) DESC, COUNT(*) DESC, MIN(r.first_name) ASC
		 LIMIT $3`,
		clubID, s.ID, limit,
	)
	if err != nil {
		h.logger.Error("public season leaderboard", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	entries := make([]leaderboardEntry, 0, limit)
	rank := 0
	for rows.Next() {
		rank++
		var e leaderboardEntry
		e.Rank = rank
		if err := rows.Scan(&e.FirstName, &e.LastName, &e.TotalPoints,
			&e.TournamentsPlayed, &e.Wins); err != nil {
			h.logger.Error("scan season leaderboard", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		entries = append(entries, e)
	}
	return c.JSON(http.StatusOK, seasonDetail{Season: s, Entries: entries})
}
