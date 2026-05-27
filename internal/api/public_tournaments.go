package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
	emails *emailContext // best-effort, peut être nil si Resend non configuré
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
	/// <summary>Inscriptions online confirmées (modérées par admin).</summary>
	ConfirmedCount int `json:"confirmedCount"`
	/// <summary>Joueurs locaux poussés depuis l'app (TournamentPlayer snapshots).</summary>
	LocalPlayersCount int `json:"localPlayersCount"`
	/// <summary>Total = confirmedCount + localPlayersCount. Utilisé pour Places X/max.</summary>
	TotalPlayerCount int `json:"totalPlayerCount"`
	/// <summary>null si MaxPlayers = 0 (illimité). Calculé sur totalPlayerCount.</summary>
	SpotsLeft *int `json:"spotsLeft,omitempty"`
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
		        COALESCE((SELECT COUNT(*) FROM tournament_registrations r WHERE r.published_tournament_id = t.id AND r.status = 'confirmed'), 0),
		        COALESCE(jsonb_array_length(t.local_players), 0)
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
			&t.LateRegEnabled, &t.LateRegUntilLevel, &t.Status,
			&t.ConfirmedCount, &t.LocalPlayersCount); err != nil {
			h.logger.Error("scan public tournament", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		t.ClubName = clubName
		t.TotalPlayerCount = t.ConfirmedCount + t.LocalPlayersCount
		if t.MaxPlayers > 0 {
			spots := t.MaxPlayers - t.TotalPlayerCount
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

// publicTournamentDetail : version "détail" pour la page d'un tournoi publié.
// Inclut un display name optionnel des joueurs selon les settings d'affichage
// du club (show_players + players_display_mode), et la structure des prix
// (calculée côté app, retournée telle quelle).
type publicTournamentDetail struct {
	publicTournament
	// Players : liste anonymisée selon le display mode. Vide si show_players=false.
	Players []string `json:"players"`
	// PrizeStructure : payouts snapshot poussé par l'app au publish (peut être null
	// si pas encore configuré côté admin). Schéma cf. migration 013.
	PrizeStructure json.RawMessage `json:"prizeStructure,omitempty"`
}

// localPlayerRecord : élément du JSON local_players stocké côté backend.
// Mirror exact du payload poussé par WPF — pas de validation, JSONB opaque.
type localPlayerRecord struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Nickname  string `json:"nickname"`
}

// getByClubSlug : GET /public/clubs/:slug/tournaments/:id
// Retourne le détail d'un tournoi + (si show_players) la liste anonymisée
// selon le display mode choisi par l'admin.
//
// Sources fusionnées :
//   - tournament_registrations status='confirmed' (online sign-ups)
//   - published_tournaments.local_players (joueurs locaux poussés depuis app)
//
// Privacy : la liste est filtrée AVANT serialization JSON. Le client public
// n'a jamais accès aux noms complets si le mode ne le permet pas.
func (h *publicTournamentsHandler) getByClubSlug(c echo.Context) error {
	slug := strings.TrimSpace(c.Param("slug"))
	id := strings.TrimSpace(c.Param("id"))
	if slug == "" || id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	clubID, clubName, ok, err := h.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	var (
		t                  publicTournament
		showPlayers        bool
		displayMode        string
		localPlayersJSON   []byte
		prizeStructureJSON []byte
	)
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT t.id, t.club_id, t.name, t.description, t.start_at,
		        t.buy_in_amount::text, t.buy_in_fees::text,
		        t.starting_stack, t.max_players, t.format,
		        t.late_reg_enabled, t.late_reg_until_level, t.status,
		        COALESCE((SELECT COUNT(*) FROM tournament_registrations r WHERE r.published_tournament_id = t.id AND r.status = 'confirmed'), 0),
		        COALESCE(jsonb_array_length(t.local_players), 0),
		        t.show_players, t.players_display_mode, t.local_players, t.prize_structure
		 FROM published_tournaments t
		 WHERE t.id = $1 AND t.club_id = $2
		   AND t.status IN ('open', 'closed')`,
		id, clubID,
	).Scan(&t.ID, &t.ClubID, &t.Name, &t.Description, &t.StartAt,
		&t.BuyInAmount, &t.BuyInFees,
		&t.StartingStack, &t.MaxPlayers, &t.Format,
		&t.LateRegEnabled, &t.LateRegUntilLevel, &t.Status,
		&t.ConfirmedCount, &t.LocalPlayersCount,
		&showPlayers, &displayMode, &localPlayersJSON, &prizeStructureJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "tournament_not_found"})
		}
		h.logger.Error("public get tournament", "err", err, "slug", slug, "id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	t.ClubName = clubName
	t.TotalPlayerCount = t.ConfirmedCount + t.LocalPlayersCount
	if t.MaxPlayers > 0 {
		spots := t.MaxPlayers - t.TotalPlayerCount
		if spots < 0 {
			spots = 0
		}
		t.SpotsLeft = &spots
	}

	detail := publicTournamentDetail{publicTournament: t, Players: []string{}}
	if len(prizeStructureJSON) > 0 {
		detail.PrizeStructure = json.RawMessage(prizeStructureJSON)
	}
	if !showPlayers || displayMode == "hidden" {
		return c.JSON(http.StatusOK, detail)
	}

	// Collecte les joueurs : online confirmed + local snapshot.
	players, err := h.collectVisiblePlayers(c, id, localPlayersJSON, displayMode)
	if err != nil {
		// Best-effort : on retourne le tournoi sans players plutôt que 500
		h.logger.Error("collect visible players", "err", err)
	} else {
		detail.Players = players
	}
	return c.JSON(http.StatusOK, detail)
}

// collectVisiblePlayers fusionne les sources et applique le filtre display mode.
func (h *publicTournamentsHandler) collectVisiblePlayers(c echo.Context, tournamentID string,
	localPlayersJSON []byte, mode string) ([]string, error) {

	out := make([]string, 0, 32)

	// 1. Online confirmed registrations
	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT first_name, last_name FROM tournament_registrations
		 WHERE published_tournament_id = $1 AND status = 'confirmed'
		 ORDER BY registered_at`,
		tournamentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fn, ln string
		if err := rows.Scan(&fn, &ln); err != nil {
			return nil, err
		}
		// Pas de nickname côté online (le form n'en demande pas) → mode pseudo
		// retombe sur "Prénom" pour ces inscriptions.
		out = append(out, formatPlayerName(fn, ln, "" /* no nickname */, mode))
	}

	// 2. Local players snapshot
	if len(localPlayersJSON) > 0 {
		var locals []localPlayerRecord
		if err := json.Unmarshal(localPlayersJSON, &locals); err == nil {
			for _, p := range locals {
				out = append(out, formatPlayerName(p.FirstName, p.LastName, p.Nickname, mode))
			}
		}
	}
	return out, nil
}

// formatPlayerName applique le display mode à un joueur. Si le champ requis
// est vide, fallback sensé (ex: mode='pseudo' sans nickname → "Prénom").
func formatPlayerName(firstName, lastName, nickname, mode string) string {
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	nickname = strings.TrimSpace(nickname)
	switch mode {
	case "full_name":
		if firstName != "" && lastName != "" {
			return firstName + " " + lastName
		}
		if firstName != "" {
			return firstName
		}
		return lastName
	case "first_initial_last":
		if firstName != "" && lastName != "" {
			return firstName + " " + lastName[:1] + "."
		}
		return firstName + lastName
	case "pseudo":
		if nickname != "" {
			return nickname
		}
		// Fallback pour les inscriptions online qui n'ont pas de pseudo : prénom seul
		return firstName
	}
	// hidden ou inconnu → on retourne anonyme (devrait pas arriver, on filtre avant)
	return "Anonyme"
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

	clubID, clubName, ok, err := h.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	// État + capacité du tournoi (+ name + buy_in pour l'email).
	// activeCount inclut online (pending+confirmed) ET local_players pour
	// que la limite max_players ne soit pas contournable via inscription
	// online quand l'admin a déjà rempli avec ses joueurs locaux.
	var (
		status         string
		startAt        time.Time
		maxPlayers     int
		activeCount    int
		tournamentName string
		buyInAmount    string
	)
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT t.status, t.start_at, t.max_players,
		        (SELECT COUNT(*) FROM tournament_registrations r
		         WHERE r.published_tournament_id = t.id
		           AND r.status IN ('pending', 'confirmed'))
		        + COALESCE(jsonb_array_length(t.local_players), 0),
		        t.name, t.buy_in_amount::text
		 FROM published_tournaments t
		 WHERE t.id = $1 AND t.club_id = $2`,
		tid, clubID,
	).Scan(&status, &startAt, &maxPlayers, &activeCount, &tournamentName, &buyInAmount)
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

	// Email best-effort de confirmation (avec lien magique d'annulation).
	// Fire-and-forget : on n'attend pas l'envoi pour répondre au caller.
	if h.emails != nil {
		go h.emails.sendRegisterConfirmation(context.Background(), registerConfirmationData{
			FirstName:      req.FirstName,
			LastName:       req.LastName,
			Email:          req.Email,
			TournamentName: tournamentName,
			TournamentDate: formatFRDateTime(startAt),
			ClubName:       clubName,
			BuyIn:          formatEuroAmount(buyInAmount),
			CancelToken:    token,
		})
	}

	return c.JSON(http.StatusCreated, resp)
}

// formatEuroAmount : "50.00" → "50,00 €". Helper local pour ne pas réimporter
// golang.org/x/text/currency.
func formatEuroAmount(raw string) string {
	// Normalise "." en "," pour FR. Pas de parsing/format strict.
	v := strings.ReplaceAll(raw, ".", ",")
	return v + " €"
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

