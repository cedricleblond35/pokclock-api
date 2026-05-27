package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// playersAuthHandler : Phase 0.E.1. Auth joueur via magic link email
// (passwordless) ou Google OAuth (à venir dans 0.E.1.e).
//
// Flow magic link :
//   1. POST /api/players/auth/magic-link  { email }
//      → insère player_magic_links (token, email, expires now+15min)
//      → envoie un email Resend avec lien /fr/joueurs/login/verify?token=…
//      → réponse 202 toujours (même si email inconnu, anti-énumération)
//   2. GET /api/players/auth/magic-link/verify?token=…
//      → marque le link used_at, crée/update player, set cookie JWT
//      → redirige vers /fr/joueurs/dashboard (ou JSON si Accept JSON)
type playersAuthHandler struct {
	pool          *pgxpool.Pool
	signer        *auth.Signer
	emails        *emailContext
	logger        *slog.Logger
	publicSiteURL string
	cookieDomain  string // ex: ".pokclock.com" en prod, vide en dev
}

const (
	playerCookieName    = "pokclock_player"
	magicLinkValidity   = 15 * time.Minute
	playerSessionTTL    = 30 * 24 * time.Hour
)

type magicLinkRequest struct {
	Email string `json:"email"`
}

// requestMagicLink : POST /api/players/auth/magic-link
// Toujours 202 — anti-énumération d'emails.
func (h *playersAuthHandler) requestMagicLink(c echo.Context) error {
	var req magicLinkRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_email"})
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_email"})
	}

	token, err := generatePlayerToken()
	if err != nil {
		h.logger.Error("generate magic link token", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}

	expires := time.Now().Add(magicLinkValidity)
	_, err = h.pool.Exec(c.Request().Context(),
		`INSERT INTO player_magic_links (token, email, expires_at)
		 VALUES ($1, $2, $3)`,
		token, email, expires,
	)
	if err != nil {
		h.logger.Error("insert magic link", "err", err, "email", email)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if h.emails != nil {
		go h.emails.sendPlayerMagicLink(context.Background(), playerMagicLinkData{
			Email: email,
			Token: token,
		})
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"status":  "sent",
		"message": "Si un compte existe ou peut être créé pour cet email, tu vas recevoir un lien de connexion.",
	})
}

// verifyMagicLink : GET /api/players/auth/magic-link/verify?token=…
// Consomme le token, upsert le player, émet le JWT, set le cookie.
//
// Renvoie toujours du JSON ici. Le frontend (RSC ou route handler) gère
// la redirection vers /joueurs/dashboard après avoir reçu la réponse.
func (h *playersAuthHandler) verifyMagicLink(c echo.Context) error {
	token := strings.TrimSpace(c.QueryParam("token"))
	if token == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_token"})
	}

	// Tx pour atomicité : marquer used_at + insérer/update player.
	tx, err := h.pool.Begin(c.Request().Context())
	if err != nil {
		h.logger.Error("begin tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_tx"})
	}
	defer tx.Rollback(c.Request().Context())

	var email string
	var expiresAt time.Time
	var usedAt *time.Time
	err = tx.QueryRow(c.Request().Context(),
		`SELECT email, expires_at, used_at FROM player_magic_links WHERE token = $1`,
		token,
	).Scan(&email, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "invalid_token"})
		}
		h.logger.Error("fetch magic link", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if usedAt != nil {
		return c.JSON(http.StatusGone, map[string]string{"error": "token_used"})
	}
	if time.Now().After(expiresAt) {
		return c.JSON(http.StatusGone, map[string]string{"error": "token_expired"})
	}

	_, err = tx.Exec(c.Request().Context(),
		`UPDATE player_magic_links SET used_at = now() WHERE token = $1`,
		token,
	)
	if err != nil {
		h.logger.Error("mark magic link used", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	var playerID string
	err = tx.QueryRow(c.Request().Context(),
		`INSERT INTO players (email, email_verified, status)
		 VALUES ($1, true, 'active')
		 ON CONFLICT (email) DO UPDATE
		   SET email_verified = true, updated_at = now()
		 RETURNING id`,
		email,
	).Scan(&playerID)
	if err != nil {
		h.logger.Error("upsert player", "err", err, "email", email)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		h.logger.Error("commit tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_commit"})
	}

	jwt, err := h.signer.IssuePlayer(auth.PlayerIssueParams{
		PlayerID: playerID,
		Email:    email,
		TTL:      playerSessionTTL,
	})
	if err != nil {
		h.logger.Error("issue player jwt", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "issue_failed"})
	}

	h.setPlayerCookie(c, jwt)

	// L'email envoie le user directement sur cet endpoint d'API. On le redirige
	// vers le frontend une fois le cookie posé. Le caller peut override via
	// ?return_to=/path (vérifié pour rester sur le site public — pas
	// d'open redirect).
	returnTo := strings.TrimSpace(c.QueryParam("return_to"))
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/fr/joueurs/dashboard"
	}
	return c.Redirect(http.StatusFound, h.publicSiteURL+returnTo)
}

// logout : POST /api/players/auth/logout
// Clear le cookie. Pas besoin d'invalider le JWT côté serveur (TTL court +
// stateless). En 0.E.5 si on stocke un refresh token, on l'invalidera ici.
func (h *playersAuthHandler) logout(c echo.Context) error {
	h.clearPlayerCookie(c)
	return c.JSON(http.StatusOK, map[string]string{"status": "logged_out"})
}

func (h *playersAuthHandler) setPlayerCookie(c echo.Context, jwt string) {
	cookie := &http.Cookie{
		Name:     playerCookieName,
		Value:    jwt,
		Path:     "/",
		Expires:  time.Now().Add(playerSessionTTL),
		MaxAge:   int(playerSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode, // cross-subdomain pokclock.com → api.pokclock.com
	}
	if h.cookieDomain != "" {
		cookie.Domain = h.cookieDomain
	}
	c.SetCookie(cookie)
}

func (h *playersAuthHandler) clearPlayerCookie(c echo.Context) {
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

// generatePlayerToken : 32 bytes random encodés en base64 URL-safe (≈ 43 chars).
// Réutilise la même entropie que generateCancelToken — non bruteforçable.
func generatePlayerToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
