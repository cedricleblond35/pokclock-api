package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// clubPointSchemesHandler : Phase 0.D-γ.1. Lecture/écriture du barème de
// points d'un club. Un club = un barème, par construction (PRIMARY KEY club_id).
type clubPointSchemesHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type pointSchemeResponse struct {
	ClubID    string          `json:"clubId"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Params    json.RawMessage `json:"params"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type pointSchemeUpsertRequest struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

var validPointSchemeTypes = map[string]struct{}{
	"linear":       {},
	"fixed":        {},
	"proportional": {},
}

// GET /api/club/point-scheme — retourne le barème du club, 404 si pas configuré.
func (h *clubPointSchemesHandler) get(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var resp pointSchemeResponse
	var paramsRaw []byte
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT club_id, name, type, params, updated_at
		 FROM point_schemes WHERE club_id = $1`,
		claims.ClubID,
	).Scan(&resp.ClubID, &resp.Name, &resp.Type, &paramsRaw, &resp.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_configured"})
		}
		h.logger.Error("point scheme get", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	resp.Params = paramsRaw
	return c.JSON(http.StatusOK, resp)
}

// PUT /api/club/point-scheme — upsert du barème.
func (h *clubPointSchemesHandler) upsert(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var req pointSchemeUpsertRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name_required"})
	}
	if _, ok := validPointSchemeTypes[req.Type]; !ok {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_type"})
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("{}")
	} else if !json.Valid(req.Params) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_params"})
	}

	var resp pointSchemeResponse
	var paramsRaw []byte
	err := h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO point_schemes (club_id, name, type, params, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (club_id) DO UPDATE
		   SET name = EXCLUDED.name,
		       type = EXCLUDED.type,
		       params = EXCLUDED.params,
		       updated_at = now()
		 RETURNING club_id, name, type, params, updated_at`,
		claims.ClubID, req.Name, req.Type, []byte(req.Params),
	).Scan(&resp.ClubID, &resp.Name, &resp.Type, &paramsRaw, &resp.UpdatedAt)
	if err != nil {
		h.logger.Error("point scheme upsert", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	resp.Params = paramsRaw
	return c.JSON(http.StatusOK, resp)
}

// DELETE /api/club/point-scheme — retire le barème (rare, mais utile si l'admin
// veut "ne pas afficher de points" temporairement).
func (h *clubPointSchemesHandler) delete(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	_, err := h.pool.Exec(c.Request().Context(),
		`DELETE FROM point_schemes WHERE club_id = $1`,
		claims.ClubID,
	)
	if err != nil {
		h.logger.Error("point scheme delete", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.NoContent(http.StatusNoContent)
}
