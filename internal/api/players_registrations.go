package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// playersRegistrationsHandler : Phase 0.E.2. Endpoints d'inscription / cancel
// liés à un compte joueur authentifié.
//
// Différences avec le flow anonyme (publicTournamentsHandler.register) :
//   - On utilise PlayerClaims.PlayerID + Email du JWT, pas un body
//   - On remplit `player_id` sur la registration pour pouvoir lister
//   - Anti-doublon par player_id ET par email (compat avec ancienne inscription
//     anonyme du même email avant qu'il crée son compte)
//   - first_name / last_name lus depuis players (ou body si fournis)
type playersRegistrationsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	emails *emailContext
}

type playerRegisterRequest struct {
	// Optionnels : si vides, on prend les valeurs du profil players.
	FirstName *string `json:"firstName"`
	LastName  *string `json:"lastName"`
	Phone     *string `json:"phone"`
}

// register : POST /api/players/me/clubs/:slug/tournaments/:id/register
// Inscription via compte. Reprend la logique du register anonyme côté
// publicTournamentsHandler mais en utilisant l'identité du JWT.
func (h *playersRegistrationsHandler) register(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	slug := strings.TrimSpace(c.Param("slug"))
	tid := strings.TrimSpace(c.Param("id"))
	if slug == "" || tid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_params"})
	}

	var req playerRegisterRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}

	// Charge le profil pour récupérer first/last name si pas fourni.
	var profileFirst, profileLast string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT first_name, last_name FROM players WHERE id = $1 AND status = 'active'`,
		claims.PlayerID,
	).Scan(&profileFirst, &profileLast)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusGone, map[string]string{"error": "player_gone"})
		}
		h.logger.Error("fetch player profile", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	firstName := strings.TrimSpace(profileFirst)
	lastName := strings.TrimSpace(profileLast)
	if req.FirstName != nil {
		firstName = strings.TrimSpace(*req.FirstName)
	}
	if req.LastName != nil {
		lastName = strings.TrimSpace(*req.LastName)
	}
	if firstName == "" || lastName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":  "missing_name",
			"detail": "Renseigne ton prénom et nom dans ton profil ou fournis-les dans la requête.",
		})
	}

	// Résolution du club (vérifie aussi opt-in inscriptions online).
	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, clubName, ok, err := pt.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	// État du tournoi + capacité + audience.
	var (
		status         string
		startAt        time.Time
		maxPlayers     int
		activeCount    int
		tournamentName string
		buyInAmount    string
		audience       string
	)
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT t.status, t.start_at, t.max_players,
		        (SELECT COUNT(*) FROM tournament_registrations r
		         WHERE r.published_tournament_id = t.id
		           AND r.status IN ('pending', 'confirmed'))
		        + COALESCE(jsonb_array_length(t.local_players), 0),
		        t.name, t.buy_in_amount::text, t.audience
		 FROM published_tournaments t
		 WHERE t.id = $1 AND t.club_id = $2`,
		tid, clubID,
	).Scan(&status, &startAt, &maxPlayers, &activeCount, &tournamentName, &buyInAmount, &audience)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "tournament_not_found"})
		}
		h.logger.Error("player register: fetch tournament", "err", err)
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

	// Phase 0.F : si tournoi members_only, vérifie que le joueur a une
	// membership active sur ce club.
	if audience == "members_only" {
		var hasActive bool
		err := h.pool.QueryRow(c.Request().Context(),
			`SELECT EXISTS(SELECT 1 FROM player_club_memberships
			               WHERE player_id = $1 AND club_id = $2 AND status = 'active')`,
			claims.PlayerID, clubID,
		).Scan(&hasActive)
		if err != nil {
			h.logger.Error("player register: check membership", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
		}
		if !hasActive {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error":  "membership_required",
				"detail": "Ce tournoi est réservé aux membres du club. Demande l'adhésion au club d'abord.",
			})
		}
	}

	// Anti-doublon : par player_id (index unique partiel) ou par email
	// (cas où le joueur s'était inscrit anonymement avant de créer son compte).
	var dupeExists bool
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM tournament_registrations
		               WHERE published_tournament_id = $1
		                 AND (player_id = $2 OR email = $3)
		                 AND status IN ('pending', 'confirmed'))`,
		tid, claims.PlayerID, claims.Email,
	).Scan(&dupeExists)
	if err != nil {
		h.logger.Error("player register: dupe check", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if dupeExists {
		return c.JSON(http.StatusConflict, map[string]string{"error": "already_registered"})
	}

	token, err := generateCancelToken()
	if err != nil {
		h.logger.Error("player register: generate token", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}

	var phone *string
	if req.Phone != nil {
		trimmed := strings.TrimSpace(*req.Phone)
		if trimmed != "" {
			phone = &trimmed
		}
	}

	var resp publicRegisterResponse
	err = h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO tournament_registrations
		   (published_tournament_id, first_name, last_name, email, phone, cancel_token, player_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, status, cancel_token, registered_at, published_tournament_id`,
		tid, firstName, lastName, claims.Email, phone, token, claims.PlayerID,
	).Scan(&resp.ID, &resp.Status, &resp.CancelToken, &resp.RegisteredAt, &resp.TournamentID)
	if err != nil {
		h.logger.Error("player register: insert", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	resp.Message = "Inscription reçue. L'organisateur du club doit la confirmer manuellement avant que ta place soit officielle."

	// Email best-effort (le cancel_token est aussi accessible via le dashboard,
	// mais on garde l'email pour la trace).
	if h.emails != nil {
		go h.emails.sendRegisterConfirmation(context.Background(), registerConfirmationData{
			FirstName:      firstName,
			LastName:       lastName,
			Email:          claims.Email,
			TournamentName: tournamentName,
			TournamentDate: formatFRDateTime(startAt),
			ClubName:       clubName,
			BuyIn:          formatEuroAmount(buyInAmount),
			CancelToken:    token,
		})
	}

	return c.JSON(http.StatusCreated, resp)
}

// cancelMyRegistration : DELETE /api/players/me/registrations/:id
// Annule une inscription appartenant au joueur authentifié. Refuse si la
// registration appartient à un autre player_id (pas de leak cross-joueur).
func (h *playersRegistrationsHandler) cancelMyRegistration(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	regID := strings.TrimSpace(c.Param("id"))
	if regID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	tag, err := h.pool.Exec(c.Request().Context(),
		`UPDATE tournament_registrations
		 SET status = 'cancelled'
		 WHERE id = $1
		   AND player_id = $2
		   AND status IN ('pending', 'confirmed')`,
		regID, claims.PlayerID,
	)
	if err != nil {
		h.logger.Error("player cancel registration", "err", err, "player_id", claims.PlayerID, "reg_id", regID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found_or_already_cancelled"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// listMyRegistrations : GET /api/players/me/registrations
// Inscriptions du joueur (pending + confirmed + cancelled), triées par date
// du tournoi décroissante. Inclut nom du club et du tournoi pour affichage.
type myRegistrationRow struct {
	ID             string    `json:"id"`
	TournamentID   string    `json:"tournamentId"`
	TournamentName string    `json:"tournamentName"`
	StartAt        time.Time `json:"startAt"`
	BuyInAmount    string    `json:"buyInAmount"`
	ClubName       string    `json:"clubName"`
	ClubSlug       string    `json:"clubSlug"`
	Status         string    `json:"status"`
	RegisteredAt   time.Time `json:"registeredAt"`
}

func (h *playersRegistrationsHandler) listMyRegistrations(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT r.id, r.published_tournament_id, t.name, t.start_at, t.buy_in_amount::text,
		        c.name, c.slug, r.status, r.registered_at
		 FROM tournament_registrations r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 JOIN clubs c ON c.id = t.club_id
		 WHERE r.player_id = $1
		 ORDER BY t.start_at DESC, r.registered_at DESC`,
		claims.PlayerID,
	)
	if err != nil {
		h.logger.Error("list my registrations", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]myRegistrationRow, 0, 32)
	for rows.Next() {
		var r myRegistrationRow
		if err := rows.Scan(&r.ID, &r.TournamentID, &r.TournamentName, &r.StartAt, &r.BuyInAmount,
			&r.ClubName, &r.ClubSlug, &r.Status, &r.RegisteredAt); err != nil {
			h.logger.Error("scan my registrations", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, map[string]any{"registrations": out})
}
