package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	googleoauth2 "golang.org/x/oauth2/google"

	"github.com/cedricleblond35/pokclock-api/internal/clients/googlecal"
)

// playersCalendarHandler : Phase 0.E.5. OAuth Calendar pour le joueur
// authentifié, séparé du login OAuth (incremental authorization).
//
// Flow :
//   1. Joueur connecté clique "Connecter mon agenda Google" → GET /api/players/me/google-calendar/start
//   2. Backend génère state, stocke en cookie HttpOnly+Lax 10min
//   3. Redirige vers Google authorize avec scope calendar.events
//      + access_type=offline + prompt=consent (pour avoir refresh_token)
//   4. Callback : exchange code, stocke access+refresh dans player_google_tokens
//   5. Redirect vers /fr/joueurs/dashboard?calendar_connected=1
//
// Le déconnect efface la row + révoque le refresh token côté Google.
type playersCalendarHandler struct {
	pool          *pgxpool.Pool
	logger        *slog.Logger
	clientID      string
	clientSecret  string
	publicSiteURL string
	apiBaseURL    string
	cookieDomain  string
	calClient     *googlecal.Client
}

const (
	calendarStateCookieName = "pokclock_cal_state"
	calendarStateTTL        = 10 * time.Minute
	calendarScope           = "https://www.googleapis.com/auth/calendar.events"
)

func (h *playersCalendarHandler) isConfigured() bool {
	return h.clientID != "" && h.clientSecret != ""
}

func (h *playersCalendarHandler) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     h.clientID,
		ClientSecret: h.clientSecret,
		RedirectURL:  h.apiBaseURL + "/api/players/me/google-calendar/callback",
		Scopes:       []string{calendarScope},
		Endpoint:     googleoauth2.Endpoint,
	}
}

// status : GET /api/players/me/google-calendar
// Retourne l'état de la connexion Calendar pour le joueur courant.
func (h *playersCalendarHandler) status(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	if !h.isConfigured() {
		return c.JSON(http.StatusOK, map[string]any{
			"available": false,
			"connected": false,
		})
	}
	var connectedAt time.Time
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT connected_at FROM player_google_tokens WHERE player_id = $1`,
		claims.PlayerID,
	).Scan(&connectedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusOK, map[string]any{"available": true, "connected": false})
		}
		h.logger.Error("calendar status", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"available":   true,
		"connected":   true,
		"connectedAt": connectedAt,
	})
}

// start : GET /api/players/me/google-calendar/start
func (h *playersCalendarHandler) start(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	if !h.isConfigured() {
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error=oauth_disabled")
	}

	state, err := generatePlayerToken()
	if err != nil {
		h.logger.Error("generate calendar state", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}
	// Encode player_id dans le state (séparé par .) pour pouvoir le récupérer
	// au callback même si la session change. Format : <playerId>.<random>
	combined := claims.PlayerID + "." + state

	stateCookie := &http.Cookie{
		Name:     calendarStateCookieName,
		Value:    combined,
		Path:     "/",
		Expires:  time.Now().Add(calendarStateTTL),
		MaxAge:   int(calendarStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if h.cookieDomain != "" {
		stateCookie.Domain = h.cookieDomain
	}
	c.SetCookie(stateCookie)

	// access_type=offline + prompt=consent : indispensable pour récupérer un
	// refresh_token. Sans ça Google renvoie juste un access_token de 1h sans
	// refresh, ce qui ne permet pas la sync en arrière-plan plus tard.
	authURL := h.oauthConfig().AuthCodeURL(combined,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"))
	return c.Redirect(http.StatusFound, authURL)
}

// callback : GET /api/players/me/google-calendar/callback
func (h *playersCalendarHandler) callback(c echo.Context) error {
	if !h.isConfigured() {
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error=oauth_disabled")
	}

	if errParam := c.QueryParam("error"); errParam != "" {
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error="+url.QueryEscape(errParam))
	}

	state := strings.TrimSpace(c.QueryParam("state"))
	code := strings.TrimSpace(c.QueryParam("code"))
	if state == "" || code == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	cookie, err := c.Cookie(calendarStateCookieName)
	if err != nil || cookie == nil || cookie.Value == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_state_cookie"})
	}
	if cookie.Value != state {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "state_mismatch"})
	}

	// Récupère player_id depuis le state combined
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 || parts[0] == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_state"})
	}
	playerID := parts[0]

	h.clearStateCookie(c)

	tok, err := h.oauthConfig().Exchange(c.Request().Context(), code)
	if err != nil {
		h.logger.Error("calendar oauth exchange", "err", err)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error=exchange_failed")
	}
	if tok.RefreshToken == "" {
		// Pas de refresh = on ne pourra pas sync en arrière-plan. C'est anormal
		// puisqu'on a demandé access_type=offline + prompt=consent. Probable
		// que le user a déjà consent à l'app sans révoquer entre temps.
		h.logger.Warn("calendar oauth no refresh token", "player_id", playerID)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error=no_refresh_token")
	}

	scope := ""
	if extra := tok.Extra("scope"); extra != nil {
		if s, ok := extra.(string); ok {
			scope = s
		}
	}

	_, err = h.pool.Exec(c.Request().Context(),
		`INSERT INTO player_google_tokens
		   (player_id, access_token, refresh_token, token_type, expires_at, scope)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (player_id) DO UPDATE
		   SET access_token = EXCLUDED.access_token,
		       refresh_token = EXCLUDED.refresh_token,
		       token_type = EXCLUDED.token_type,
		       expires_at = EXCLUDED.expires_at,
		       scope = EXCLUDED.scope,
		       updated_at = now()`,
		playerID, tok.AccessToken, tok.RefreshToken, tok.TokenType, tok.Expiry, scope)
	if err != nil {
		h.logger.Error("upsert calendar token", "err", err, "player_id", playerID)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_error=db_failed")
	}

	return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard?calendar_connected=1")
}

// disconnect : DELETE /api/players/me/google-calendar
func (h *playersCalendarHandler) disconnect(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	var refreshToken string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT refresh_token FROM player_google_tokens WHERE player_id = $1`,
		claims.PlayerID,
	).Scan(&refreshToken)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("fetch token for revoke", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}

	// Révoque côté Google best-effort. Si ça échoue on supprime quand même
	// la row locale : le user veut être déconnecté, on respecte ça.
	if refreshToken != "" && h.calClient != nil {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
		defer cancel()
		if err := h.calClient.RevokeToken(ctx, refreshToken); err != nil {
			h.logger.Warn("revoke google token", "err", err, "player_id", claims.PlayerID)
		}
	}

	_, err = h.pool.Exec(c.Request().Context(),
		`DELETE FROM player_google_tokens WHERE player_id = $1`,
		claims.PlayerID)
	if err != nil {
		h.logger.Error("delete token row", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "disconnected"})
}

func (h *playersCalendarHandler) clearStateCookie(c echo.Context) {
	cookie := &http.Cookie{
		Name:     calendarStateCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if h.cookieDomain != "" {
		cookie.Domain = h.cookieDomain
	}
	c.SetCookie(cookie)
}
