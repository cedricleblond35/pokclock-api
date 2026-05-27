package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// playersMembershipsHandler : Phase 0.F. Endpoints côté joueur pour demander
// l'adhésion à un club et lister/quitter ses clubs.
type playersMembershipsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type myMembershipRow struct {
	ID           string    `json:"id"`
	ClubID       string    `json:"clubId"`
	ClubName     string    `json:"clubName"`
	ClubSlug     string    `json:"clubSlug"`
	Status       string    `json:"status"`
	RequestedAt  time.Time `json:"requestedAt"`
	ModeratedAt  *time.Time `json:"moderatedAt,omitempty"`
}

// listMyClubs : GET /api/players/me/clubs — toutes les adhésions du joueur.
func (h *playersMembershipsHandler) listMyClubs(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT m.id, c.id, c.name, c.slug, m.status, m.requested_at, m.moderated_at
		 FROM player_club_memberships m
		 JOIN clubs c ON c.id = m.club_id
		 WHERE m.player_id = $1
		 ORDER BY m.requested_at DESC`,
		claims.PlayerID,
	)
	if err != nil {
		h.logger.Error("list my clubs", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]myMembershipRow, 0, 8)
	for rows.Next() {
		var r myMembershipRow
		if err := rows.Scan(&r.ID, &r.ClubID, &r.ClubName, &r.ClubSlug, &r.Status,
			&r.RequestedAt, &r.ModeratedAt); err != nil {
			h.logger.Error("scan my clubs", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, map[string]any{"memberships": out})
}

// joinClub : POST /api/players/me/clubs/:slug/join — demande d'adhésion.
//
// Si une row existe déjà :
//   - status='pending' ou 'active' → 409 (déjà demandé/membre)
//   - status='rejected' ou 'revoked' → on remet à 'pending' (re-demande après bannissement = ok)
func (h *playersMembershipsHandler) joinClub(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, _, ok, err := pt.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	// Check existing membership
	var existingStatus *string
	err = h.pool.QueryRow(c.Request().Context(),
		`SELECT status FROM player_club_memberships
		 WHERE player_id = $1 AND club_id = $2`,
		claims.PlayerID, clubID,
	).Scan(&existingStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("check existing membership", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if existingStatus != nil {
		switch *existingStatus {
		case "pending":
			return c.JSON(http.StatusConflict, map[string]string{"error": "already_pending"})
		case "active":
			return c.JSON(http.StatusConflict, map[string]string{"error": "already_member"})
		case "rejected", "revoked":
			// Réinitialise la demande (le joueur retente)
			_, err := h.pool.Exec(c.Request().Context(),
				`UPDATE player_club_memberships
				 SET status = 'pending',
				     requested_at = now(),
				     moderated_at = NULL,
				     moderated_by_license = NULL,
				     moderation_reason = NULL,
				     updated_at = now()
				 WHERE player_id = $1 AND club_id = $2`,
				claims.PlayerID, clubID,
			)
			if err != nil {
				h.logger.Error("re-request membership", "err", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
			}
			return c.JSON(http.StatusOK, map[string]string{"status": "pending"})
		}
	}

	_, err = h.pool.Exec(c.Request().Context(),
		`INSERT INTO player_club_memberships (player_id, club_id, status)
		 VALUES ($1, $2, 'pending')`,
		claims.PlayerID, clubID,
	)
	if err != nil {
		h.logger.Error("insert membership", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusCreated, map[string]string{"status": "pending"})
}

// leaveClub : DELETE /api/players/me/clubs/:slug — quitte un club.
// Hard delete : pas d'historique conservé. Pour exclure un membre il faut
// passer par /api/club/memberships/:id/revoke (admin) qui set status='revoked'.
func (h *playersMembershipsHandler) leaveClub(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	pt := &publicTournamentsHandler{pool: h.pool, logger: h.logger}
	clubID, _, ok, err := pt.resolveClub(c, slug)
	if err != nil {
		return err
	}
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	tag, err := h.pool.Exec(c.Request().Context(),
		`DELETE FROM player_club_memberships
		 WHERE player_id = $1 AND club_id = $2`,
		claims.PlayerID, clubID,
	)
	if err != nil {
		h.logger.Error("leave club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not_a_member"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "left"})
}
