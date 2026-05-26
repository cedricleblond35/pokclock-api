package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

type structuresHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type structure struct {
	ID        string          `json:"id"`
	ClubID    string          `json:"clubId"`
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type createStructureRequest struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Settings json.RawMessage `json:"settings"`
}

type updateStructureRequest struct {
	Name     *string         `json:"name,omitempty"`
	Kind     *string         `json:"kind,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

var allowedKinds = map[string]bool{
	"club":     true,
	"casino":   true,
	"home":     true,
	"streamer": true,
	"other":    true,
}

const (
	defaultStructuresLimit = 50
	maxStructuresLimit     = 200
)

// list retourne les structures du club du caller, paginées.
// Le filtre par club_id est implicite via claims.ClubID — aucun param query
// ne permet de le contourner, c'est le principe du club scope.
func (h *structuresHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultStructuresLimit, maxStructuresLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, club_id, slug, name, kind, settings, created_at, updated_at
		 FROM structures
		 WHERE club_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		claims.ClubID, limit, offset,
	)
	if err != nil {
		h.logger.Error("list structures", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]structure, 0, limit)
	for rows.Next() {
		var s structure
		if err := rows.Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Kind, &s.Settings,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			h.logger.Error("scan structure", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, s)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"structures": out,
		"limit":      limit,
		"offset":     offset,
	})
}

func (h *structuresHandler) create(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var req createStructureRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	req.Name = strings.TrimSpace(req.Name)
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))

	if req.Slug == "" || req.Name == "" || req.Kind == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_fields"})
	}
	if !allowedKinds[req.Kind] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_kind"})
	}
	if len(req.Settings) == 0 {
		req.Settings = []byte("{}")
	}

	ctx := c.Request().Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.logger.Error("begin tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_unavailable"})
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var s structure
	err = tx.QueryRow(ctx,
		`INSERT INTO structures (club_id, slug, name, kind, settings)
		 VALUES ($1, $2, $3, $4, $5::jsonb)
		 RETURNING id, club_id, slug, name, kind, settings, created_at, updated_at`,
		claims.ClubID, req.Slug, req.Name, req.Kind, string(req.Settings),
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			// (club_id, slug) collision — un slug doublon dans le même club
			return c.JSON(http.StatusConflict, map[string]string{"error": "slug_taken"})
		}
		h.logger.Error("insert structure", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if err := writeAudit(ctx, tx, s.ID, "created", req, claims.Subject); err != nil {
		h.logger.Error("audit write", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "audit_failed"})
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("commit tx", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit_failed"})
	}
	return c.JSON(http.StatusCreated, s)
}

func (h *structuresHandler) get(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	var s structure
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, club_id, slug, name, kind, settings, created_at, updated_at
		 FROM structures WHERE club_id = $1 AND slug = $2`,
		claims.ClubID, slug,
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("get structure", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	return c.JSON(http.StatusOK, s)
}

func (h *structuresHandler) update(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	var req updateStructureRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	if req.Kind != nil {
		k := strings.ToLower(strings.TrimSpace(*req.Kind))
		if !allowedKinds[k] {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_kind"})
		}
		*req.Kind = k
	}

	ctx := c.Request().Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_unavailable"})
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var s structure
	err = tx.QueryRow(ctx,
		`UPDATE structures
		 SET name = COALESCE($3, name),
		     kind = COALESCE($4, kind),
		     settings = COALESCE($5::jsonb, settings)
		 WHERE club_id = $1 AND slug = $2
		 RETURNING id, club_id, slug, name, kind, settings, created_at, updated_at`,
		claims.ClubID, slug, req.Name, req.Kind, nullableJSON(req.Settings),
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("update structure", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if err := writeAudit(ctx, tx, s.ID, "updated", req, claims.Subject); err != nil {
		h.logger.Error("audit write", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "audit_failed"})
	}
	if err := tx.Commit(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit_failed"})
	}
	return c.JSON(http.StatusOK, s)
}

// delete supprime une structure (hard delete). L'audit est écrit avant le
// DELETE en transaction pour conserver la trace même après la disparition
// de la structure (la ligne structures_audit a structure_id en FK CASCADE,
// donc le DELETE structure supprime aussi ses lignes audit — on garde
// quand même une trace dans l'audit en ajoutant l'entrée APRÈS suppression
// est impossible. À la place, on capture la structure dans le payload).
func (h *structuresHandler) delete(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	ctx := c.Request().Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_unavailable"})
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Snapshot avant suppression — utile pour l'audit log (la ligne audit
	// elle-même sera CASCADE-supprimée, mais on garde le snapshot dans le
	// payload via writeAudit).
	var s structure
	err = tx.QueryRow(ctx,
		`SELECT id, club_id, slug, name, kind, settings, created_at, updated_at
		 FROM structures WHERE club_id = $1 AND slug = $2`,
		claims.ClubID, slug,
	).Scan(&s.ID, &s.ClubID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("snapshot structure for delete", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}

	// Audit AVANT delete (sinon le CASCADE détruit l'enregistrement audit).
	// L'audit reste cohérent : structure_id pointe sur un UUID qui n'existe
	// plus, mais on capture le snapshot dans `changes` pour reconstruire.
	if err := writeAudit(ctx, tx, s.ID, "deleted", s, claims.Subject); err != nil {
		h.logger.Error("audit write delete", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "audit_failed"})
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM structures WHERE club_id = $1 AND slug = $2`,
		claims.ClubID, slug,
	); err != nil {
		h.logger.Error("delete structure", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	if err := tx.Commit(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit_failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

// writeAudit insère une ligne dans structures_audit.
func writeAudit(ctx context.Context, tx pgx.Tx, structureID, action string, changes any, actor string) error {
	payload, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO structures_audit (structure_id, action, changes, actor)
		 VALUES ($1, $2, $3::jsonb, $4)`,
		structureID, action, string(payload), actor)
	return err
}

// nullableJSON retourne nil si le payload est vide, sinon la chaîne JSON.
// Postgres convertit nil en NULL et donc COALESCE garde l'ancienne valeur.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// isUniqueViolation détecte la violation de contrainte UNIQUE.
// Code Postgres 23505.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key value")
}

