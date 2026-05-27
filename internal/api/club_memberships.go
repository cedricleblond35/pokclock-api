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

// clubMembershipsHandler : Phase 0.F. Côté admin club, modération des
// demandes d'adhésion joueurs et gestion des memberships actives.
type clubMembershipsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type clubMembershipRow struct {
	ID                  string     `json:"id"`
	PlayerID            string     `json:"playerId"`
	PlayerEmail         string     `json:"playerEmail"`
	PlayerFirstName     string     `json:"playerFirstName"`
	PlayerLastName      string     `json:"playerLastName"`
	Status              string     `json:"status"`
	RequestedAt         time.Time  `json:"requestedAt"`
	ModeratedAt         *time.Time `json:"moderatedAt,omitempty"`
	ModeratedByLicense  *string    `json:"moderatedByLicense,omitempty"`
	ModerationReason    *string    `json:"moderationReason,omitempty"`
}

// list : GET /api/club/memberships?status=pending&limit=…
// Liste les demandes/membres du club. Filtre status optionnel.
func (h *clubMembershipsHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), 100, 500)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 100_000)

	conds := []string{"m.club_id = $1"}
	args := []any{claims.ClubID}
	if s := strings.TrimSpace(c.QueryParam("status")); s != "" {
		args = append(args, s)
		conds = append(conds, "m.status = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT m.id, m.player_id, p.email, p.first_name, p.last_name, m.status,
		        m.requested_at, m.moderated_at, m.moderated_by_license, m.moderation_reason
		 FROM player_club_memberships m
		 JOIN players p ON p.id = m.player_id
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY m.requested_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("list club memberships", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]clubMembershipRow, 0, limit)
	for rows.Next() {
		var r clubMembershipRow
		if err := rows.Scan(&r.ID, &r.PlayerID, &r.PlayerEmail, &r.PlayerFirstName, &r.PlayerLastName,
			&r.Status, &r.RequestedAt, &r.ModeratedAt, &r.ModeratedByLicense, &r.ModerationReason); err != nil {
			h.logger.Error("scan club membership", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"memberships": out,
		"limit":       limit,
		"offset":      offset,
	})
}

// approve : POST /api/club/memberships/:id/approve
// Passe pending → active.
func (h *clubMembershipsHandler) approve(c echo.Context) error {
	return h.moderate(c, "pending", "active", nil)
}

// reject : POST /api/club/memberships/:id/reject (body { reason? })
// Passe pending → rejected.
func (h *clubMembershipsHandler) reject(c echo.Context) error {
	var body ModerationRequest
	_ = c.Bind(&body)
	return h.moderate(c, "pending", "rejected", body.Reason)
}

// revoke : POST /api/club/memberships/:id/revoke (body { reason? })
// Passe active → revoked. Pour exclure un membre déjà actif.
func (h *clubMembershipsHandler) revoke(c echo.Context) error {
	var body ModerationRequest
	_ = c.Bind(&body)
	return h.moderate(c, "active", "revoked", body.Reason)
}

// ModerationRequest est partagé entre les handlers de modération.
type ModerationRequest struct {
	Reason *string `json:"reason,omitempty"`
}

// moderate : helper commun. Vérifie que la row appartient au club + que le
// status courant matche, puis update.
func (h *clubMembershipsHandler) moderate(c echo.Context, expectedFrom, to string, reason *string) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var currentStatus string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT status FROM player_club_memberships
		 WHERE id = $1 AND club_id = $2`,
		id, claims.ClubID,
	).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("fetch membership", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if currentStatus != expectedFrom {
		return c.JSON(http.StatusConflict, map[string]string{
			"error":  "invalid_transition",
			"detail": "Statut actuel = " + currentStatus + ", transition vers " + to + " impossible.",
		})
	}

	_, err = h.pool.Exec(c.Request().Context(),
		`UPDATE player_club_memberships
		 SET status = $1,
		     moderated_at = now(),
		     moderated_by_license = $2,
		     moderation_reason = $3,
		     updated_at = now()
		 WHERE id = $4 AND club_id = $5`,
		to, claims.LicenseKey, reason, id, claims.ClubID,
	)
	if err != nil {
		h.logger.Error("moderate membership", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": to})
}
