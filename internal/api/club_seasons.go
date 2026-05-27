package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// clubSeasonsHandler : Phase 0.G. CRUD saisons côté admin club.
type clubSeasonsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type season struct {
	ID          string    `json:"id"`
	ClubID      string    `json:"clubId"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	StartsAt    time.Time `json:"startsAt"`
	EndsAt      time.Time `json:"endsAt"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Compteurs pratiques pour la liste, populés via sous-requête.
	TournamentsCount int `json:"tournamentsCount"`
}

type createSeasonRequest struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	StartsAt    string  `json:"startsAt"` // RFC3339
	EndsAt      string  `json:"endsAt"`
}

type updateSeasonRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	StartsAt    *string `json:"startsAt,omitempty"`
	EndsAt      *string `json:"endsAt,omitempty"`
	Status      *string `json:"status,omitempty"`
}

var validSeasonStatuses = map[string]struct{}{
	"upcoming": {},
	"active":   {},
	"finished": {},
	"archived": {},
}

// list : GET /api/club/seasons
func (h *clubSeasonsHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT s.id, s.club_id, s.slug, s.name, s.description, s.starts_at, s.ends_at,
		        s.status, s.created_at, s.updated_at,
		        COALESCE((SELECT COUNT(*) FROM published_tournaments p WHERE p.season_id = s.id), 0)
		 FROM seasons s
		 WHERE s.club_id = $1
		 ORDER BY s.starts_at DESC`,
		claims.ClubID,
	)
	if err != nil {
		h.logger.Error("list seasons", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]season, 0, 16)
	for rows.Next() {
		var s season
		if err := rows.Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Description,
			&s.StartsAt, &s.EndsAt, &s.Status, &s.CreatedAt, &s.UpdatedAt,
			&s.TournamentsCount); err != nil {
			h.logger.Error("scan season", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, s)
	}
	return c.JSON(http.StatusOK, map[string]any{"seasons": out})
}

// create : POST /api/club/seasons
func (h *clubSeasonsHandler) create(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var req createSeasonRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Slug = slugify(req.Slug)
	req.Name = strings.TrimSpace(req.Name)
	if req.Slug == "" || req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_fields"})
	}
	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_starts_at"})
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_ends_at"})
	}
	if !endsAt.After(startsAt) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "ends_before_starts"})
	}

	var s season
	err = h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO seasons (club_id, slug, name, description, starts_at, ends_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, club_id, slug, name, description, starts_at, ends_at,
		           status, created_at, updated_at`,
		claims.ClubID, req.Slug, req.Name, req.Description, startsAt, endsAt,
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Description,
		&s.StartsAt, &s.EndsAt, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		// Détecte la collision sur UNIQUE (club_id, slug)
		if strings.Contains(err.Error(), "duplicate key") {
			return c.JSON(http.StatusConflict, map[string]string{"error": "slug_already_used"})
		}
		h.logger.Error("create season", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusCreated, s)
}

// update : PATCH /api/club/seasons/:id
func (h *clubSeasonsHandler) update(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var req updateSeasonRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}

	sets := []string{}
	args := []any{id, claims.ClubID}
	if req.Name != nil {
		args = append(args, strings.TrimSpace(*req.Name))
		sets = append(sets, "name = $"+strconv.Itoa(len(args)))
	}
	if req.Description != nil {
		args = append(args, *req.Description)
		sets = append(sets, "description = $"+strconv.Itoa(len(args)))
	}
	if req.StartsAt != nil {
		t, err := time.Parse(time.RFC3339, *req.StartsAt)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_starts_at"})
		}
		args = append(args, t)
		sets = append(sets, "starts_at = $"+strconv.Itoa(len(args)))
	}
	if req.EndsAt != nil {
		t, err := time.Parse(time.RFC3339, *req.EndsAt)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_ends_at"})
		}
		args = append(args, t)
		sets = append(sets, "ends_at = $"+strconv.Itoa(len(args)))
	}
	if req.Status != nil {
		st := strings.TrimSpace(*req.Status)
		if _, ok := validSeasonStatuses[st]; !ok {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_status"})
		}
		args = append(args, st)
		sets = append(sets, "status = $"+strconv.Itoa(len(args)))
	}
	if len(sets) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "no_fields"})
	}

	var s season
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE seasons SET `+strings.Join(sets, ", ")+`
		 WHERE id = $1 AND club_id = $2
		 RETURNING id, club_id, slug, name, description, starts_at, ends_at,
		           status, created_at, updated_at`,
		args...,
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Description,
		&s.StartsAt, &s.EndsAt, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("update season", "err", err, "season_id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, s)
}

// delete : DELETE /api/club/seasons/:id
// CASCADE coupe le lien sur published_tournaments (season_id passe à NULL),
// les tournois historiques restent.
func (h *clubSeasonsHandler) delete(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	tag, err := h.pool.Exec(c.Request().Context(),
		`DELETE FROM seasons WHERE id = $1 AND club_id = $2`,
		id, claims.ClubID)
	if err != nil {
		h.logger.Error("delete season", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
	}
	return c.NoContent(http.StatusNoContent)
}

// slugify : minimal — lowercase, trim, replace non-alphanum par tirets.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	out := make([]rune, 0, len(s))
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out = append(out, r)
			prevDash = false
		case r == '-' || r == ' ' || r == '_':
			if !prevDash && len(out) > 0 {
				out = append(out, '-')
				prevDash = true
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}
