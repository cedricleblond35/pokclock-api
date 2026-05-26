package api

import (
	"encoding/json"
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

// clubTournamentsHandler porte le CRUD des tournois publiés en ligne par
// les apps PokClock du club. Routes /api/club/tournaments/* — auth admin+
// + club scope vérifié en amont par le router.
type clubTournamentsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type publishedTournament struct {
	ID                  string          `json:"id"`
	ClubID              string          `json:"clubId"`
	Name                string          `json:"name"`
	Description         *string         `json:"description,omitempty"`
	StartAt             time.Time       `json:"startAt"`
	BuyInAmount         string          `json:"buyInAmount"` // numeric → string pour précision décimale
	BuyInFees           string          `json:"buyInFees"`
	StartingStack       int             `json:"startingStack"`
	MaxPlayers          int             `json:"maxPlayers"`
	Format              string          `json:"format"`
	LateRegEnabled      bool            `json:"lateRegEnabled"`
	LateRegUntilLevel   *int            `json:"lateRegUntilLevel,omitempty"`
	LocalTournamentID   *int            `json:"localTournamentId,omitempty"`
	Status              string          `json:"status"`
	BlindsSummary       json.RawMessage `json:"blindsSummary,omitempty"`
	PublishedByLicense  string          `json:"publishedByLicense"`
	PublishedAt         time.Time       `json:"publishedAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	// Compteur d'inscriptions par status — populé via sous-requête dans list/get
	PendingCount        int             `json:"pendingCount"`
	ConfirmedCount      int             `json:"confirmedCount"`
}

type publishTournamentRequest struct {
	Name                string          `json:"name"`
	Description         *string         `json:"description,omitempty"`
	StartAt             string          `json:"startAt"` // RFC3339
	BuyInAmount         string          `json:"buyInAmount"`
	BuyInFees           string          `json:"buyInFees"`
	StartingStack       int             `json:"startingStack"`
	MaxPlayers          int             `json:"maxPlayers"`
	Format              string          `json:"format"`
	LateRegEnabled      bool            `json:"lateRegEnabled"`
	LateRegUntilLevel   *int            `json:"lateRegUntilLevel,omitempty"`
	LocalTournamentID   *int            `json:"localTournamentId,omitempty"`
	BlindsSummary       json.RawMessage `json:"blindsSummary,omitempty"`
}

const (
	defaultTournamentsLimit = 100
	maxTournamentsLimit     = 500
)

func (h *clubTournamentsHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultTournamentsLimit, maxTournamentsLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)
	status := strings.TrimSpace(c.QueryParam("status"))

	conds := []string{"t.club_id = $1"}
	args := []any{claims.ClubID}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "t.status = $2")
	}
	args = append(args, limit, offset)

	q := `SELECT t.id, t.club_id, t.name, t.description, t.start_at,
	             t.buy_in_amount::text, t.buy_in_fees::text,
	             t.starting_stack, t.max_players, t.format,
	             t.late_reg_enabled, t.late_reg_until_level,
	             t.local_tournament_id, t.status, t.blinds_summary,
	             t.published_by_license, t.published_at, t.updated_at,
	             COALESCE((SELECT COUNT(*) FROM tournament_registrations r WHERE r.published_tournament_id = t.id AND r.status = 'pending'), 0) AS pending_count,
	             COALESCE((SELECT COUNT(*) FROM tournament_registrations r WHERE r.published_tournament_id = t.id AND r.status = 'confirmed'), 0) AS confirmed_count
	      FROM published_tournaments t
	      WHERE ` + strings.Join(conds, " AND ") + `
	      ORDER BY t.start_at DESC
	      LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(), q, args...)
	if err != nil {
		h.logger.Error("list published tournaments", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]publishedTournament, 0, limit)
	for rows.Next() {
		t, err := scanPublishedTournament(rows)
		if err != nil {
			h.logger.Error("scan tournament", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, t)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"tournaments": out,
		"limit":       limit,
		"offset":      offset,
	})
}

// publish crée un nouveau tournoi publié. License key extraite des claims
// (subject = licenseKey du caller) pour audit.
func (h *clubTournamentsHandler) publish(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	// Vérifier que le club a online_registrations_enabled = true (le club a opt-in)
	var enabled bool
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT online_registrations_enabled FROM clubs WHERE id = $1`, claims.ClubID,
	).Scan(&enabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "club_not_found"})
		}
		h.logger.Error("club fetch enabled flag", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if !enabled {
		return c.JSON(http.StatusForbidden, map[string]string{
			"error":  "online_registrations_disabled",
			"detail": "Active les inscriptions en ligne pour ce club via les paramètres.",
		})
	}

	var req publishTournamentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_name"})
	}
	startAt, err := time.Parse(time.RFC3339, req.StartAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_start_at"})
	}

	var t publishedTournament
	err = h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO published_tournaments
		   (club_id, name, description, start_at, buy_in_amount, buy_in_fees,
		    starting_stack, max_players, format, late_reg_enabled, late_reg_until_level,
		    local_tournament_id, blinds_summary, published_by_license)
		 VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8, $9, $10, $11, $12, $13::jsonb, $14)
		 RETURNING id, club_id, name, description, start_at,
		           buy_in_amount::text, buy_in_fees::text,
		           starting_stack, max_players, format,
		           late_reg_enabled, late_reg_until_level,
		           local_tournament_id, status, blinds_summary,
		           published_by_license, published_at, updated_at`,
		claims.ClubID, req.Name, req.Description, startAt,
		req.BuyInAmount, req.BuyInFees,
		req.StartingStack, req.MaxPlayers, req.Format,
		req.LateRegEnabled, req.LateRegUntilLevel,
		req.LocalTournamentID, nullableRawJSON(req.BlindsSummary),
		claims.Subject,
	).Scan(&t.ID, &t.ClubID, &t.Name, &t.Description, &t.StartAt,
		&t.BuyInAmount, &t.BuyInFees,
		&t.StartingStack, &t.MaxPlayers, &t.Format,
		&t.LateRegEnabled, &t.LateRegUntilLevel,
		&t.LocalTournamentID, &t.Status, &t.BlindsSummary,
		&t.PublishedByLicense, &t.PublishedAt, &t.UpdatedAt)
	if err != nil {
		h.logger.Error("insert published tournament", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusCreated, t)
}

// setStatus transitionne le tournoi vers closed / cancelled. Pas de retour
// au status 'open' une fois fermé/annulé (transition one-way pour rester simple).
func (h *clubTournamentsHandler) setStatus(newStatus string) echo.HandlerFunc {
	return func(c echo.Context) error {
		claims := ClaimsFromContext(c)
		if claims == nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
		}
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
		}

		tag, err := h.pool.Exec(c.Request().Context(),
			`UPDATE published_tournaments SET status = $3
			 WHERE id = $1 AND club_id = $2 AND status = 'open'`,
			id, claims.ClubID, newStatus,
		)
		if err != nil {
			h.logger.Error("set tournament status", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
		}
		if tag.RowsAffected() == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":  "not_found_or_not_open",
				"detail": "Tournoi introuvable ou déjà fermé/annulé.",
			})
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// scanRowable abstraite pgx.Row / pgx.Rows pour réutiliser scanPublishedTournament.
type scanRowable interface {
	Scan(dest ...any) error
}

func scanPublishedTournament(r scanRowable) (publishedTournament, error) {
	var t publishedTournament
	err := r.Scan(&t.ID, &t.ClubID, &t.Name, &t.Description, &t.StartAt,
		&t.BuyInAmount, &t.BuyInFees,
		&t.StartingStack, &t.MaxPlayers, &t.Format,
		&t.LateRegEnabled, &t.LateRegUntilLevel,
		&t.LocalTournamentID, &t.Status, &t.BlindsSummary,
		&t.PublishedByLicense, &t.PublishedAt, &t.UpdatedAt,
		&t.PendingCount, &t.ConfirmedCount)
	return t, err
}

// nullableRawJSON retourne nil si le payload est vide (NULL en SQL via $N::jsonb),
// sinon la string JSON. Utile pour les colonnes jsonb nullable.
func nullableRawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

