package api

import (
	"context"
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
	pool         *pgxpool.Pool
	logger       *slog.Logger
	emails       *emailContext
	cookieDomain string
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

// deleteMe : DELETE /api/players/me — droit à l'effacement RGPD.
//
// Transaction :
//   1. Anonymise tournament_registrations du joueur (first_name='Anonyme',
//      last_name='', email='', phone=NULL, player_id=NULL). On garde la ligne
//      pour que les capacités historiques restent cohérentes.
//   2. Anonymise tournament_results idem. Préserve les classements/leaderboards
//      historiques sans identifier le joueur.
//   3. DELETE player_magic_links par email (le FK n'est pas posé sur players,
//      les liens sont keyés par email).
//   4. DELETE FROM players. Le DB CASCADE/SET NULL nettoie les autres FK.
//
// Effet de bord : clear le cookie de session côté navigateur. Email best-effort
// de confirmation pour traçabilité utilisateur.
func (h *playersMeHandler) deleteMe(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	// Charge l'email avant suppression pour l'utiliser dans l'email de confirmation.
	var email, firstName string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT email, first_name FROM players WHERE id = $1`,
		claims.PlayerID,
	).Scan(&email, &firstName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Déjà supprimé : on clear quand même le cookie pour réinitialiser le browser.
			h.clearPlayerCookieDirect(c)
			return c.JSON(http.StatusOK, map[string]string{"status": "already_deleted"})
		}
		h.logger.Error("fetch player for delete", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}

	tx, err := h.pool.Begin(c.Request().Context())
	if err != nil {
		h.logger.Error("begin delete tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_tx"})
	}
	defer tx.Rollback(c.Request().Context()) //nolint:errcheck

	if _, err := tx.Exec(c.Request().Context(),
		`UPDATE tournament_registrations
		 SET first_name = 'Anonyme', last_name = '', email = '', phone = NULL, player_id = NULL
		 WHERE player_id = $1`,
		claims.PlayerID,
	); err != nil {
		h.logger.Error("anonymize registrations", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if _, err := tx.Exec(c.Request().Context(),
		`UPDATE tournament_results
		 SET first_name = 'Anonyme', last_name = '', player_id = NULL
		 WHERE player_id = $1`,
		claims.PlayerID,
	); err != nil {
		h.logger.Error("anonymize results", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if _, err := tx.Exec(c.Request().Context(),
		`DELETE FROM player_magic_links WHERE lower(email) = lower($1)`,
		email,
	); err != nil {
		h.logger.Error("delete magic links", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if _, err := tx.Exec(c.Request().Context(),
		`DELETE FROM players WHERE id = $1`,
		claims.PlayerID,
	); err != nil {
		h.logger.Error("delete player", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		h.logger.Error("commit delete tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_commit"})
	}

	// Email best-effort (le compte est déjà supprimé en DB, peu importe si Resend échoue).
	if h.emails != nil {
		go h.emails.sendAccountDeleted(context.Background(), accountDeletedData{
			Email:     email,
			FirstName: firstName,
		})
	}

	h.clearPlayerCookieDirect(c)
	return c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

// clearPlayerCookieDirect : duplicat de playersAuthHandler.clearPlayerCookie
// pour éviter de coupler les deux handlers. La config du cookie doit rester
// identique entre les deux.
func (h *playersMeHandler) clearPlayerCookieDirect(c echo.Context) {
	cookie := &http.Cookie{
		Name:     playerCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	}
	if h.cookieDomain != "" {
		cookie.Domain = h.cookieDomain
	}
	c.SetCookie(cookie)
}
