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
		`INSERT INTO structures (slug, name, kind, settings)
		 VALUES ($1, $2, $3, $4::jsonb)
		 RETURNING id, slug, name, kind, settings, created_at, updated_at`,
		req.Slug, req.Name, req.Kind, string(req.Settings),
	).Scan(&s.ID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
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
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_slug"})
	}

	var s structure
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, slug, name, kind, settings, created_at, updated_at
		 FROM structures WHERE slug = $1`,
		slug,
	).Scan(&s.ID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
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
		 SET name = COALESCE($2, name),
		     kind = COALESCE($3, kind),
		     settings = COALESCE($4::jsonb, settings)
		 WHERE slug = $1
		 RETURNING id, slug, name, kind, settings, created_at, updated_at`,
		slug, req.Name, req.Kind, nullableJSON(req.Settings),
	).Scan(&s.ID, &s.Slug, &s.Name, &s.Kind, &s.Settings, &s.CreatedAt, &s.UpdatedAt)
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

// isUniqueViolation détecte la violation de contrainte UNIQUE sur structures.slug.
// Code Postgres 23505.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key value")
}
