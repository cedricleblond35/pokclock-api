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

// playersMeHandler : Phase 0.E.1. Endpoints /api/players/me sur le compte
// du joueur authentifié (via playerAuthMiddleware).
type playersMeHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type playerProfile struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	FirstName     string    `json:"firstName"`
	LastName      string    `json:"lastName"`
	EmailVerified bool      `json:"emailVerified"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// getMe : GET /api/players/me — retourne le profil du joueur authentifié.
func (h *playersMeHandler) getMe(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	var p playerProfile
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, email, first_name, last_name, email_verified, status, created_at, updated_at
		 FROM players WHERE id = $1`,
		claims.PlayerID,
	).Scan(&p.ID, &p.Email, &p.FirstName, &p.LastName, &p.EmailVerified, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusGone, map[string]string{"error": "player_deleted"})
		}
		h.logger.Error("fetch player", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if p.Status != "active" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "account_" + p.Status})
	}
	return c.JSON(http.StatusOK, p)
}

type updatePlayerRequest struct {
	FirstName *string `json:"firstName"`
	LastName  *string `json:"lastName"`
}

// updateMe : PATCH /api/players/me — met à jour first_name / last_name.
// Email non modifiable ici (devra passer par un flow de re-vérification).
func (h *playersMeHandler) updateMe(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	var req updatePlayerRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	if req.FirstName == nil && req.LastName == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "no_fields"})
	}

	sets := []string{}
	args := []any{claims.PlayerID}
	if req.FirstName != nil {
		args = append(args, strings.TrimSpace(*req.FirstName))
		sets = append(sets, "first_name = $"+strconv.Itoa(len(args)))
	}
	if req.LastName != nil {
		args = append(args, strings.TrimSpace(*req.LastName))
		sets = append(sets, "last_name = $"+strconv.Itoa(len(args)))
	}

	var p playerProfile
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE players SET `+strings.Join(sets, ", ")+`, updated_at = now()
		 WHERE id = $1
		 RETURNING id, email, first_name, last_name, email_verified, status, created_at, updated_at`,
		args...,
	).Scan(&p.ID, &p.Email, &p.FirstName, &p.LastName, &p.EmailVerified, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		h.logger.Error("update player", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, p)
}
