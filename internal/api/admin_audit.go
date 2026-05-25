package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// adminAuditHandler expose la recherche dans structures_audit cross-clubs.
// Protégé en amont par jwtAuth + requireRole(superadmin).
type adminAuditHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type auditEntry struct {
	ID          string          `json:"id"`
	StructureID string          `json:"structureId"`
	Action      string          `json:"action"`
	Changes     json.RawMessage `json:"changes"`
	Actor       string          `json:"actor"`
	CreatedAt   time.Time       `json:"createdAt"`
	// Jointure avec structures pour donner du contexte (slug, name, club_id)
	StructureSlug string  `json:"structureSlug,omitempty"`
	StructureName string  `json:"structureName,omitempty"`
	ClubID        *string `json:"clubId,omitempty"`
}

const (
	defaultAuditLimit = 100
	maxAuditLimit     = 500
)

// search renvoie les entrées d'audit, filtrables par :
//
//	?clubId=<uuid>        : seulement les actions sur structures de ce club
//	?actor=<text>         : filtre exact sur l'acteur (claim sub du JWT)
//	?action=<text>        : created | updated | deleted
//	?from=<RFC3339>       : borne basse créée >=
//	?to=<RFC3339>         : borne haute créée <=
//	?structureId=<uuid>   : filtre une structure précise
//	?limit, ?offset       : pagination (defaults 100/0, max 500/1M)
func (h *adminAuditHandler) search(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), defaultAuditLimit, maxAuditLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)

	conds := []string{}
	args := []any{}

	if clubID := strings.TrimSpace(c.QueryParam("clubId")); clubID != "" {
		args = append(args, clubID)
		conds = append(conds, "s.club_id = $"+strconv.Itoa(len(args)))
	}
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

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT sa.id, sa.structure_id, sa.action, sa.changes, sa.actor, sa.created_at,
		        s.slug, s.name, s.club_id
		 FROM structures_audit sa
		 LEFT JOIN structures s ON s.id = sa.structure_id`+where+`
		 ORDER BY sa.created_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("audit search", "err", err)
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
