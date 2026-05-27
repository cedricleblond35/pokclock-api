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

// clubRegistrationsHandler : modération des inscriptions en ligne.
// Routes /api/club/tournaments/:tid/registrations + actions confirm/reject.
type clubRegistrationsHandler struct {
	pool    *pgxpool.Pool
	logger  *slog.Logger
	emails  *emailContext // best-effort, peut être nil si Resend non configuré
	calSync *calendarSync // best-effort, peut être nil si Google OAuth non configuré
}

type registration struct {
	ID                    string     `json:"id"`
	PublishedTournamentID string     `json:"publishedTournamentId"`
	FirstName             string     `json:"firstName"`
	LastName              string     `json:"lastName"`
	Email                 string     `json:"email"`
	Phone                 *string    `json:"phone,omitempty"`
	Status                string     `json:"status"`
	ModeratedByLicense    *string    `json:"moderatedByLicense,omitempty"`
	ModeratedAt           *time.Time `json:"moderatedAt,omitempty"`
	ModerationReason      *string    `json:"moderationReason,omitempty"`
	RegisteredAt          time.Time  `json:"registeredAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

const (
	defaultRegistrationsLimit = 200
	maxRegistrationsLimit     = 1000
)

// list inscriptions d'un tournoi, filtrable par status. Vérifie en préalable
// que le tournoi appartient au club du caller (404 sinon, pas de leak cross-club).
func (h *clubRegistrationsHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	tid := strings.TrimSpace(c.Param("tid"))
	if tid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_tournament_id"})
	}

	// Précheck ownership tournoi
	if err := h.assertTournamentInClub(c, tid, claims.ClubID); err != nil {
		return err
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultRegistrationsLimit, maxRegistrationsLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)
	status := strings.TrimSpace(c.QueryParam("status"))

	conds := []string{"published_tournament_id = $1"}
	args := []any{tid}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "status = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit, offset)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, published_tournament_id, first_name, last_name, email, phone,
		        status, moderated_by_license, moderated_at, moderation_reason,
		        registered_at, updated_at
		 FROM tournament_registrations
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY registered_at DESC
		 LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...,
	)
	if err != nil {
		h.logger.Error("list registrations", "err", err, "tournament_id", tid)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]registration, 0, limit)
	for rows.Next() {
		var r registration
		if err := rows.Scan(&r.ID, &r.PublishedTournamentID, &r.FirstName, &r.LastName,
			&r.Email, &r.Phone, &r.Status, &r.ModeratedByLicense, &r.ModeratedAt,
			&r.ModerationReason, &r.RegisteredAt, &r.UpdatedAt); err != nil {
			h.logger.Error("scan registration", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"registrations": out,
		"limit":         limit,
		"offset":        offset,
	})
}

type moderateRequest struct {
	Reason *string `json:"reason,omitempty"`
}

// confirm : passe une inscription 'pending' en 'confirmed'.
// Action irréversible côté API (l'app peut "annuler" un confirm en
// révoquant le Player local — pas de retour à pending).
func (h *clubRegistrationsHandler) confirm(c echo.Context) error {
	return h.moderate(c, "confirmed", "pending")
}

// reject : passe une inscription 'pending' en 'rejected' avec motif optionnel.
func (h *clubRegistrationsHandler) reject(c echo.Context) error {
	return h.moderate(c, "rejected", "pending")
}

func (h *clubRegistrationsHandler) moderate(c echo.Context, target, expected string) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	rid := strings.TrimSpace(c.Param("id"))
	if rid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var req moderateRequest
	_ = c.Bind(&req) // body optionnel pour confirm, attendu pour reject

	// On UPDATE en filtrant aussi par club_id du tournoi parent (JOIN) — pas
	// d'accès cross-club même en connaissant l'UUID de la registration.
	// On rapporte aussi le nom du tournoi et du club pour l'email.
	var (
		r              registration
		tournamentName string
		tournamentDate time.Time
		clubName       string
	)
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE tournament_registrations r
		 SET status = $3,
		     moderated_by_license = $4,
		     moderated_at = now(),
		     moderation_reason = $5
		 FROM published_tournaments t, clubs cl
		 WHERE r.id = $1
		   AND r.published_tournament_id = t.id
		   AND t.club_id = $2
		   AND cl.id = t.club_id
		   AND r.status = $6
		 RETURNING r.id, r.published_tournament_id, r.first_name, r.last_name,
		           r.email, r.phone, r.status, r.moderated_by_license,
		           r.moderated_at, r.moderation_reason, r.registered_at, r.updated_at,
		           t.name, t.start_at, cl.name`,
		rid, claims.ClubID, target, claims.Subject, req.Reason, expected,
	).Scan(&r.ID, &r.PublishedTournamentID, &r.FirstName, &r.LastName,
		&r.Email, &r.Phone, &r.Status, &r.ModeratedByLicense, &r.ModeratedAt,
		&r.ModerationReason, &r.RegisteredAt, &r.UpdatedAt,
		&tournamentName, &tournamentDate, &clubName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Soit la registration n'existe pas, soit le tournoi n'est pas
			// du club du caller, soit la registration n'est pas en pending.
			// On confond les trois cas en 404 pour ne pas leaker.
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":  "not_found_or_not_pending",
				"detail": "Inscription introuvable ou déjà modérée.",
			})
		}
		h.logger.Error("moderate registration", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	// Email best-effort post-modération.
	if h.emails != nil {
		reason := ""
		if req.Reason != nil {
			reason = *req.Reason
		}
		go h.emails.sendModerationDecision(context.Background(), moderationDecisionData{
			FirstName:      r.FirstName,
			LastName:       r.LastName,
			Email:          r.Email,
			TournamentName: tournamentName,
			TournamentDate: formatFRDateTime(tournamentDate),
			ClubName:       clubName,
			Confirmed:      target == "confirmed",
			Reason:         reason,
		})
	}

	// Phase 0.E.5 : sync Calendar (update event si confirmed, delete si rejected).
	if h.calSync != nil {
		go h.calSync.OnStatusChange(context.Background(), r.ID, target)
	}

	return c.JSON(http.StatusOK, r)
}

// formatFRDateTime : "lun. 26 mai 2026 à 18:00" — local time du serveur.
// Garde le format simple pour rester portable cross-OS.
func formatFRDateTime(t time.Time) string {
	loc, _ := time.LoadLocation("Europe/Paris")
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format("02/01/2006 à 15:04")
}

// assertTournamentInClub : retourne nil + écrit la réponse 404 si le tournoi
// n'appartient pas au club. Le caller doit immédiatement return l'erreur.
func (h *clubRegistrationsHandler) assertTournamentInClub(c echo.Context, tournamentID, clubID string) error {
	var exists bool
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM published_tournaments WHERE id = $1 AND club_id = $2)`,
		tournamentID, clubID,
	).Scan(&exists)
	if err != nil {
		h.logger.Error("check tournament club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tournament_not_found"})
	}
	return nil
}
