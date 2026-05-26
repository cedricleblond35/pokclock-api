package api

import (
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
)

// publicTournamentsHandler porte les endpoints anonymes consommés par le
// site public pokclock.com/clubs/:slug/tournois. AUCUNE auth — l'audience
// est ouverte par design (Phase 0.D-α décision 1a : anonyme + lien magique).
//
// Sécurité :
//   - Pas de leak cross-club : on filtre par slug → club_id en interne
//   - Rate limit recommandé en amont (Cloudflare ou Traefik), pas géré ici
//   - Cancel token : URL-safe random 32 bytes (256 bits) — non bruteforçable
type publicTournamentsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// publicTournament : version publique d'un tournoi (info NON sensibles uniquement).
// Pas de license du publisher, pas de blinds détaillées, pas de liste nominale
// des inscrits — uniquement le nombre de confirmés.
type publicTournament struct {
	ID                string    `json:"id"`
	ClubID            string    `json:"clubId"`
	ClubName          string    `json:"clubName"`
	Name              string    `json:"name"`
	Description       *string   `json:"description,omitempty"`
	StartAt           time.Time `json:"startAt"`
	BuyInAmount       string    `json:"buyInAmount"`
	BuyInFees         string    `json:"buyInFees"`
	StartingStack     int       `json:"startingStack"`
	MaxPlayers        int       `json:"maxPlayers"`
	Format            string    `json:"format"`
	LateRegEnabled    bool      `json:"lateRegEnabled"`
	LateRegUntilLevel *int      `json:"lateRegUntilLevel,omitempty"`
	Status            string    `json:"status"`
	ConfirmedCount    int       `json:"confirmedCount"`
	SpotsLeft         *int      `json:"spotsLeft,omitempty"` // null si MaxPlayers = 0 (illimité)
}

// listByClubSlug : GET /public/clubs/:slug/tournaments
// Liste les tournois "open" (futurs et inscriptions ouvertes) + "closed" récents
// (les annulés sont filtrés). Anonymes uniquement.
func (h *publicTournamentsHandler) listByClubSlug(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	// Vérifier que le club existe ET a opt-in pour les inscriptions en ligne.
	clubID, clubName, ok, err := h.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT t.id, t.club_id, t.name, t.description, t.start_at,
		        t.buy_in_amount::text, t.buy_in_fees::text,
		        t.starting_stack, t.max_players, t.format,
		        t.late_reg_enabled, t.late_reg_until_level, t.status,
		        COALESCE((SELECT COUNT(*) FROM tournament_registrations r WHERE r.published_tournament_id = t.id AND r.status = 'confirmed'), 0)
		 FROM published_tournaments t
		 WHERE t.club_id = $1
		   AND t.status IN ('open', 'closed')
		 ORDER BY t.start_at ASC`,
		clubID,
	)
	if err != nil {
		h.logger.Error("public list tournaments", "err", err, "slug", slug)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]publicTournament, 0, 50)
	for rows.Next() {
		var t publicTournament
		if err := rows.Scan(&t.ID, &t.ClubID, &t.Name, &t.Description, &t.StartAt,
			&t.BuyInAmount, &t.BuyInFees,
			&t.StartingStack, &t.MaxPlayers, &t.Format,
			&t.LateRegEnabled, &t.LateRegUntilLevel, &t.Status, &t.ConfirmedCount); err != nil {
			h.logger.Error("scan public tournament", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		t.ClubName = clubName
		if t.MaxPlayers > 0 {
			spots := t.MaxPlayers - t.ConfirmedCount
			if spots < 0 {
				spots = 0
			}
			t.SpotsLeft = &spots
		}
		out = append(out, t)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"tournaments": out,
		"club":        map[string]any{"id": clubID, "name": clubName, "slug": slug},
	})
}

type publicRegisterRequest struct {
	FirstName string  `json:"firstName"`
	LastName  string  `json:"lastName"`
	Email     string  `json:"email"`
	Phone     *string `json:"phone,omitempty"`
}

type publicRegisterResponse struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	CancelToken   string    `json:"cancelToken"`
	RegisteredAt  time.Time `json:"registeredAt"`
	TournamentID  string    `json:"tournamentId"`
	Message       string    `json:"message"`
}

// register : POST /public/clubs/:slug/tournaments/:id/register
// Inscription anonyme. Crée une registration status='pending'. Retourne le
// cancel_token au caller (sera émailé en α.1.b, pour l'instant le caller doit
// le stocker côté front).
//
// Validations métier :
//   - club opt-in (online_registrations_enabled = true)
//   - tournoi appartient au club ET status = 'open' ET start_at > now
//   - max_players non dépassé (compte confirmed+pending)
//   - email déjà inscrit (même tournoi, status != cancelled) → 409
//   - format email rudimentaire (regex via net/mail)
func (h *publicTournamentsHandler) register(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	tid := strings.TrimSpace(c.Param("id"))
	if slug == "" || tid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	var req publicRegisterRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.FirstName == "" || req.LastName == "" || req.Email == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_fields"})
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_email"})
	}

	clubID, _, ok, err := h.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	// État + capacité du tournoi
	var (
		status       string
		startAt      time.Time
		maxPlayers   int
		activeCount  int
	)
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT t.status, t.start_at, t.max_players,
		        (SELECT COUNT(*) FROM tournament_registrations r
		         WHERE r.published_tournament_id = t.id
		           AND r.status IN ('pending', 'confirmed'))
		 FROM published_tournaments t
		 WHERE t.id = $1 AND t.club_id = $2`,
		tid, clubID,
	).Scan(&status, &startAt, &maxPlayers, &activeCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "tournament_not_found"})
		}
		h.logger.Error("public register fetch tournament", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if status != "open" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "registrations_closed"})
	}
	if startAt.Before(time.Now()) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "tournament_started"})
	}
	if maxPlayers > 0 && activeCount >= maxPlayers {
		return c.JSON(http.StatusConflict, map[string]string{"error": "tournament_full"})
	}

	// Anti-doublon (même email, même tournoi, pas annulé)
	var dupeExists bool
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM tournament_registrations
		               WHERE published_tournament_id = $1
		                 AND email = $2
		                 AND status IN ('pending', 'confirmed'))`,
		tid, req.Email,
	).Scan(&dupeExists)
	if err != nil {
		h.logger.Error("check dupe", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if dupeExists {
		return c.JSON(http.StatusConflict, map[string]string{"error": "already_registered"})
	}

	token, err := generateCancelToken()
	if err != nil {
		h.logger.Error("generate cancel token", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}

	var resp publicRegisterResponse
	err = h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO tournament_registrations
		   (published_tournament_id, first_name, last_name, email, phone, cancel_token)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, status, cancel_token, registered_at, published_tournament_id`,
		tid, req.FirstName, req.LastName, req.Email, req.Phone, token,
	).Scan(&resp.ID, &resp.Status, &resp.CancelToken, &resp.RegisteredAt, &resp.TournamentID)
	if err != nil {
		h.logger.Error("insert registration", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	resp.Message = "Inscription reçue. L'organisateur du club doit la confirmer manuellement avant que ta place soit officielle. Tu peux annuler à tout moment via le lien d'annulation."
	return c.JSON(http.StatusCreated, resp)
}

// cancelByToken : GET /public/registrations/:token/cancel
// Annule une inscription par son cancel_token (lien magique). Idempotent :
// déjà annulé renvoie 200 sans erreur. Ne révèle PAS si le token n'existe
// pas (404) — protection bruteforce.
func (h *publicTournamentsHandler) cancelByToken(c echo.Context) error {
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_token"})
	}

	tag, err := h.pool.Exec(c.Request().Context(),
		`UPDATE tournament_registrations
		 SET status = 'cancelled'
		 WHERE cancel_token = $1 AND status IN ('pending', 'confirmed')`,
		token,
	)
	if err != nil {
		h.logger.Error("cancel by token", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		// Soit token inconnu, soit déjà annulée/rejetée. On confond pour ne
		// pas révéler l'existence d'un token.
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "not_found_or_already_cancelled",
		})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// resolveClub : map slug → (clubID, name) si online_registrations_enabled = true.
// Retourne ok=false sans erreur DB si club inconnu ou opt-out (404 standardisé).
func (h *publicTournamentsHandler) resolveClub(c echo.Context, slug string) (string, string, bool, error) {
	var id, name string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, name FROM clubs
		 WHERE slug = $1 AND online_registrations_enabled = true AND status = 'active'`,
		slug,
	).Scan(&id, &name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		h.logger.Error("resolve club by slug", "err", err, "slug", slug)
		return "", "", false, c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	return id, name, true, nil
}

// generateCancelToken : 32 bytes random encodés en base64 URL-safe (≈ 43 chars).
// 256 bits d'entropie, non bruteforçable raisonnablement.
func generateCancelToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

