package api

import (
	"context"
	"encoding/json"
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

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// playersGoogleOAuthHandler : Phase 0.E.1.e. Login Google OAuth pour les
// comptes joueurs, en alternative au magic link email.
//
// Flow OAuth 2.0 standard avec state CSRF :
//   1. GET /api/players/auth/google/start[?return_to=/path]
//      → génère state random, le stocke en cookie HttpOnly court (10 min),
//        redirige vers Google authorize URL avec state + scope openid email profile
//   2. Google redirige vers /api/players/auth/google/callback?code=…&state=…
//      → valide state vs cookie, échange code contre access_token + id_token,
//        fetch userinfo, UPSERT player, émet JWT joueur, redirige sur le frontend.
//
// Le client OAuth GCP doit être configuré avec :
//   - Authorized redirect URI : https://api.pokclock.com/api/players/auth/google/callback
//   - Scopes acceptés : openid, email, profile
type playersGoogleOAuthHandler struct {
	pool          *pgxpool.Pool
	signer        *auth.Signer
	logger        *slog.Logger
	clientID      string
	clientSecret  string
	publicSiteURL string
	apiBaseURL    string
	cookieDomain  string
}

const (
	googleStateCookieName = "pokclock_google_state"
	googleStateTTL        = 10 * time.Minute
	googleUserinfoURL     = "https://www.googleapis.com/oauth2/v3/userinfo"
)

func (h *playersGoogleOAuthHandler) isConfigured() bool {
	return h.clientID != "" && h.clientSecret != ""
}

func (h *playersGoogleOAuthHandler) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     h.clientID,
		ClientSecret: h.clientSecret,
		RedirectURL:  h.apiBaseURL + "/api/players/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     googleoauth2.Endpoint,
	}
}

// start : GET /api/players/auth/google/start
func (h *playersGoogleOAuthHandler) start(c echo.Context) error {
	if !h.isConfigured() {
		// Redirige vers la page de login avec un code d'erreur lisible plutôt
		// que de renvoyer du JSON brut au navigateur.
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=google_oauth_disabled")
	}

	state, err := generatePlayerToken()
	if err != nil {
		h.logger.Error("generate google state", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}

	// Optionnel : return_to encodé dans le state via séparateur. On garde
	// simple, sans return_to pour l'instant — toujours redirect vers
	// /fr/joueurs/dashboard après login.
	stateCookie := &http.Cookie{
		Name:     googleStateCookieName,
		Value:    state,
		Path:     "/",
		Expires:  time.Now().Add(googleStateTTL),
		MaxAge:   int(googleStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode, // Lax pour que le cookie revienne au callback Google
	}
	if h.cookieDomain != "" {
		stateCookie.Domain = h.cookieDomain
	}
	c.SetCookie(stateCookie)

	authURL := h.oauthConfig().AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("prompt", "select_account"))
	return c.Redirect(http.StatusFound, authURL)
}

// callback : GET /api/players/auth/google/callback?code=…&state=…
func (h *playersGoogleOAuthHandler) callback(c echo.Context) error {
	if !h.isConfigured() {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "google_oauth_disabled"})
	}

	// Erreur côté Google (user a refusé, ou autre) : redirige sur login avec un code d'erreur.
	if errParam := c.QueryParam("error"); errParam != "" {
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error="+url.QueryEscape("google_"+errParam))
	}

	state := strings.TrimSpace(c.QueryParam("state"))
	code := strings.TrimSpace(c.QueryParam("code"))
	if state == "" || code == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	cookie, err := c.Cookie(googleStateCookieName)
	if err != nil || cookie == nil || cookie.Value == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_state_cookie"})
	}
	if cookie.Value != state {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "state_mismatch"})
	}

	// Clear state cookie (single-use)
	h.clearStateCookie(c)

	// Exchange code → tokens
	tok, err := h.oauthConfig().Exchange(c.Request().Context(), code)
	if err != nil {
		h.logger.Error("google oauth exchange", "err", err)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=google_exchange_failed")
	}

	// Fetch userinfo
	userInfo, err := h.fetchUserInfo(c.Request().Context(), tok.AccessToken)
	if err != nil {
		h.logger.Error("google fetch userinfo", "err", err)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=google_userinfo_failed")
	}
	if userInfo.Sub == "" || userInfo.Email == "" {
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=google_incomplete_profile")
	}

	// UPSERT player : on cherche d'abord par google_id, puis par email.
	// Si trouvé par email sans google_id, on link les deux.
	// Si nouveau, on crée.
	playerID, err := h.upsertPlayerFromGoogle(c.Request().Context(), userInfo)
	if err != nil {
		h.logger.Error("upsert player from google", "err", err, "google_id", userInfo.Sub)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=google_db_failed")
	}

	jwt, err := h.signer.IssuePlayer(auth.PlayerIssueParams{
		PlayerID: playerID,
		Email:    userInfo.Email,
		TTL:      playerSessionTTL,
	})
	if err != nil {
		h.logger.Error("issue player jwt (google)", "err", err)
		return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/login?error=issue_failed")
	}

	h.setPlayerSessionCookie(c, jwt)
	return c.Redirect(http.StatusFound, h.publicSiteURL+"/fr/joueurs/dashboard")
}

// googleUserInfo : payload userinfo OAuth standard. Champs alignés sur
// https://accounts.google.com/.well-known/openid-configuration.
type googleUserInfo struct {
	Sub           string `json:"sub"`            // identifiant Google stable
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Name          string `json:"name"`
	Locale        string `json:"locale"`
}

func (h *playersGoogleOAuthHandler) fetchUserInfo(ctx context.Context, accessToken string) (*googleUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("userinfo http " + res.Status)
	}
	var u googleUserInfo
	if err := json.NewDecoder(res.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (h *playersGoogleOAuthHandler) upsertPlayerFromGoogle(ctx context.Context, u *googleUserInfo) (string, error) {
	// 1. Tente par google_id
	var playerID string
	err := h.pool.QueryRow(ctx,
		`SELECT id FROM players WHERE google_id = $1`,
		u.Sub,
	).Scan(&playerID)
	if err == nil {
		// Login direct, met à jour last name si vide pour profiter du Google profile
		_, _ = h.pool.Exec(ctx,
			`UPDATE players
			 SET first_name = COALESCE(NULLIF(first_name, ''), $2),
			     last_name  = COALESCE(NULLIF(last_name, ''), $3),
			     email_verified = true,
			     updated_at = now()
			 WHERE id = $1`,
			playerID, u.GivenName, u.FamilyName)
		return playerID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	// 2. Tente par email (cas où le joueur avait déjà créé un compte via magic link)
	err = h.pool.QueryRow(ctx,
		`UPDATE players
		 SET google_id = $2,
		     first_name = COALESCE(NULLIF(first_name, ''), $3),
		     last_name  = COALESCE(NULLIF(last_name, ''), $4),
		     email_verified = true,
		     updated_at = now()
		 WHERE lower(email) = lower($1)
		 RETURNING id`,
		u.Email, u.Sub, u.GivenName, u.FamilyName,
	).Scan(&playerID)
	if err == nil {
		return playerID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	// 3. Création
	err = h.pool.QueryRow(ctx,
		`INSERT INTO players (email, google_id, first_name, last_name, email_verified, status)
		 VALUES ($1, $2, $3, $4, true, 'active')
		 RETURNING id`,
		u.Email, u.Sub, u.GivenName, u.FamilyName,
	).Scan(&playerID)
	if err != nil {
		return "", err
	}
	return playerID, nil
}

func (h *playersGoogleOAuthHandler) setPlayerSessionCookie(c echo.Context, jwt string) {
	cookie := &http.Cookie{
		Name:     playerCookieName,
		Value:    jwt,
		Path:     "/",
		Expires:  time.Now().Add(playerSessionTTL),
		MaxAge:   int(playerSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	}
	if h.cookieDomain != "" {
		cookie.Domain = h.cookieDomain
	}
	c.SetCookie(cookie)
}

func (h *playersGoogleOAuthHandler) clearStateCookie(c echo.Context) {
	cookie := &http.Cookie{
		Name:     googleStateCookieName,
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
