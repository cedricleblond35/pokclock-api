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

// clubAuditHandler : version club-scoped de adminAuditHandler. Forcage du
// filtre WHERE s.club_id = claims.ClubID, aucun cross-club possible.
type clubAuditHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func (h *clubAuditHandler) search(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultAuditLimit, maxAuditLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)

	conds := []string{"s.club_id = $1"}
	args := []any{claims.ClubID}

	if actor := strings.TrimSpace(c.QueryParam("actor")); actor != "" {
		args = append(args, actor)
		conds = append(conds, "sa.actor = $"+strconv.Itoa(len(args)))
	}
	if action := strings.TrimSpace(c.QueryParam("action")); action != "" {
		args = append(args, action)
		conds = append(conds, "sa.action = $"+strconv.Itoa(len(args)))
	}
	if structureID := strings.TrimSpace(c.QueryParam("structureId")); structureID != "" {
		args = append(args, structureID)
		conds = append(conds, "sa.structure_id = $"+strconv.Itoa(len(args)))
	}
	if fromStr := strings.TrimSpace(c.QueryParam("from")); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_from"})
		}
		args = append(args, t)
		conds = append(conds, "sa.created_at >= $"+strconv.Itoa(len(args)))
	}
	if toStr := strings.TrimSpace(c.QueryParam("to")); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_to"})
		}
		args = append(args, t)
		conds = append(conds, "sa.created_at <= $"+strconv.Itoa(len(args)))
	}

	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT sa.id, sa.structure_id, sa.action, sa.changes, sa.actor, sa.created_at,
		        s.slug, s.name, s.club_id
		 FROM structures_audit sa
		 LEFT JOIN structures s ON s.id = sa.structure_id
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY sa.created_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("club audit search", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	entries := make([]auditEntry, 0, limit)
	for rows.Next() {
		var e auditEntry
		var slug, name *string
		if err := rows.Scan(&e.ID, &e.StructureID, &e.Action, &e.Changes, &e.Actor, &e.CreatedAt,
			&slug, &name, &e.ClubID); err != nil {
			h.logger.Error("scan audit", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		if slug != nil {
			e.StructureSlug = *slug
		}
		if name != nil {
			e.StructureName = *name
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate audit", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_iterate"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}

// clubInfoHandler : GET /api/club/info — retourne les méta du club du caller.
// Lecture seule, pas de mutation (les modifs club restent /api/admin/clubs/:id).
type clubInfoHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type clubInfoResponse struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Plan      string          `json:"plan"`
	Email     *string         `json:"email,omitempty"`
	Status    string          `json:"status"`
	Notes     *string         `json:"notes,omitempty"`
	Settings  json.RawMessage `json:"settings,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (h *clubInfoHandler) get(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var info clubInfoResponse
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, name, plan, email, status, notes, created_at, updated_at
		 FROM clubs WHERE id = $1`,
		claims.ClubID,
	).Scan(&info.ID, &info.Name, &info.Plan, &info.Email, &info.Status, &info.Notes,
		&info.CreatedAt, &info.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// claims.ClubID pointe vers un club inexistant — incohérence DB,
			// remonte une erreur explicite plutôt qu'un 404.
			h.logger.Error("club info: club referenced by claims not found",
				"club_id", claims.ClubID, "subject", claims.Subject)
			return c.JSON(http.StatusGone, map[string]string{
				"error":  "club_gone",
				"detail": "Le club rattaché à ta license n'existe plus. Contacte le support.",
			})
		}
		h.logger.Error("club info", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	return c.JSON(http.StatusOK, info)
}
