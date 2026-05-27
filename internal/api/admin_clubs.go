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

// adminClubsHandler porte les routes CRUD sur les clubs.
// Toutes les routes du groupe /api/admin/clubs requièrent role=superadmin
// (filtré en amont par requireRole dans router.go).
type adminClubsHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type club struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Plan         string     `json:"plan"`
	Email        *string    `json:"email,omitempty"`
	Status       string     `json:"status"`
	Notes        *string    `json:"notes,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	SuspendedAt  *time.Time `json:"suspendedAt,omitempty"`
}

type createClubRequest struct {
	Name  string  `json:"name"`
	Plan  string  `json:"plan"`
	Email *string `json:"email,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

type updateClubRequest struct {
	Name  *string `json:"name,omitempty"`
	Plan  *string `json:"plan,omitempty"`
	Email *string `json:"email,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

var allowedPlans = map[string]bool{
	"solo": true,
	"club": true,
	"pro":  true,
}

const (
	defaultClubsLimit = 50
	maxClubsLimit     = 200
)

// list renvoie la liste paginée des clubs, optionnellement filtrée par status
// (?status=active|suspended|deleted) et recherche par nom (?q=...).
func (h *adminClubsHandler) list(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), defaultClubsLimit, maxClubsLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)
	status := strings.TrimSpace(c.QueryParam("status"))
	q := strings.TrimSpace(c.QueryParam("q"))

	// Construction dynamique du WHERE en accumulant les conditions et args.
	conds := []string{}
	args := []any{}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "status = $"+strconv.Itoa(len(args)))
	}
	if q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		conds = append(conds, "LOWER(name) LIKE $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, name, plan, email, status, notes, created_at, updated_at, suspended_at
		 FROM clubs`+where+`
		 ORDER BY created_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("list clubs", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	clubs := make([]club, 0, limit)
	for rows.Next() {
		var cl club
		if err := rows.Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
			&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt); err != nil {
			h.logger.Error("scan club", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		clubs = append(clubs, cl)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate clubs", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_iterate"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"clubs":  clubs,
		"limit":  limit,
		"offset": offset,
	})
}

// get renvoie un club par son id (uuid).
func (h *adminClubsHandler) get(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	var cl club
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT id, name, plan, email, status, notes, created_at, updated_at, suspended_at
		 FROM clubs WHERE id = $1`, id,
	).Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
		&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("get club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	return c.JSON(http.StatusOK, cl)
}

// create insère un nouveau club. Le name doit être unique.
func (h *adminClubsHandler) create(c echo.Context) error {
	var req createClubRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Plan = strings.ToLower(strings.TrimSpace(req.Plan))
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_name"})
	}
	if req.Plan == "" {
		req.Plan = "solo"
	}
	if !allowedPlans[req.Plan] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_plan"})
	}

	var cl club
	err := h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO clubs (name, plan, email, notes)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, name, plan, email, status, notes, created_at, updated_at, suspended_at`,
		req.Name, req.Plan, req.Email, req.Notes,
	).Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
		&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "name_taken"})
		}
		h.logger.Error("insert club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusCreated, cl)
}

// update modifie les champs autorisés d'un club (name, plan, email, notes).
// Le status ne se modifie pas via cet endpoint : utiliser suspend / unsuspend.
func (h *adminClubsHandler) update(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	var req updateClubRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	if req.Plan != nil {
		p := strings.ToLower(strings.TrimSpace(*req.Plan))
		if !allowedPlans[p] {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_plan"})
		}
		*req.Plan = p
	}

	var cl club
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE clubs SET
		   name  = COALESCE($2, name),
		   plan  = COALESCE($3, plan),
		   email = COALESCE($4, email),
		   notes = COALESCE($5, notes)
		 WHERE id = $1
		 RETURNING id, name, plan, email, status, notes, created_at, updated_at, suspended_at`,
		id, req.Name, req.Plan, req.Email, req.Notes,
	).Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
		&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		if isUniqueViolation(err) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "name_taken"})
		}
		h.logger.Error("update club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, cl)
}

// suspend passe le club en status=suspended et set suspended_at=now().
// Idempotent : suspendre un club déjà suspendu ne fait rien.
func (h *adminClubsHandler) suspend(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	var cl club
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE clubs SET status = 'suspended', suspended_at = now()
		 WHERE id = $1 AND status <> 'deleted'
		 RETURNING id, name, plan, email, status, notes, created_at, updated_at, suspended_at`,
		id,
	).Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
		&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found_or_deleted"})
		}
		h.logger.Error("suspend club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, cl)
}

// unsuspend remet un club en status=active. suspended_at est conservé pour audit.
func (h *adminClubsHandler) unsuspend(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	var cl club
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE clubs SET status = 'active', suspended_at = NULL
		 WHERE id = $1 AND status <> 'deleted'
		 RETURNING id, name, plan, email, status, notes, created_at, updated_at, suspended_at`,
		id,
	).Scan(&cl.ID, &cl.Name, &cl.Plan, &cl.Email, &cl.Status,
		&cl.Notes, &cl.CreatedAt, &cl.UpdatedAt, &cl.SuspendedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found_or_deleted"})
		}
		h.logger.Error("unsuspend club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, cl)
}

// softDelete passe le club en status=deleted. Conserve la ligne pour audit ;
// les structures associées sont coupées par ON DELETE CASCADE *si* on faisait
// un DELETE réel — ici on garde tout en base pour l'historique.
func (h *adminClubsHandler) softDelete(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	tag, err := h.pool.Exec(c.Request().Context(),
		`UPDATE clubs SET status = 'deleted', suspended_at = now() WHERE id = $1`,
		id,
	)
	if err != nil {
		h.logger.Error("delete club", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
	}
	return c.NoContent(http.StatusNoContent)
}

// listMembers : GET /api/admin/clubs/:id/members.
// Version super-admin de l'endpoint /api/club/members (cross-clubs).
// Permet à Cédric de voir les membres de n'importe quel club depuis le
// dashboard ops, sans avoir une license dans chaque club.
func (h *adminClubsHandler) listMembers(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	// Sanity check : le club doit exister (sinon on confond 404 club et liste vide)
	var exists bool
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT EXISTS(SELECT 1 FROM clubs WHERE id = $1)`, id,
	).Scan(&exists)
	if err != nil {
		h.logger.Error("check club exists", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "club_not_found"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), 200, 1000)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, club_id, first_name, last_name, email, notes, status,
		        created_at, updated_at
		 FROM members
		 WHERE club_id = $1
		 ORDER BY last_name, first_name
		 LIMIT $2 OFFSET $3`,
		id, limit, offset,
	)
	if err != nil {
		h.logger.Error("admin list members", "err", err, "club_id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	type adminMember struct {
		ID        string    `json:"id"`
		ClubID    string    `json:"clubId"`
		FirstName string    `json:"firstName"`
		LastName  string    `json:"lastName"`
		Email     *string   `json:"email,omitempty"`
		Notes     *string   `json:"notes,omitempty"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"createdAt"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	out := make([]adminMember, 0, limit)
	for rows.Next() {
		var m adminMember
		if err := rows.Scan(&m.ID, &m.ClubID, &m.FirstName, &m.LastName, &m.Email, &m.Notes,
			&m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			h.logger.Error("scan admin member", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, m)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"members": out,
		"limit":   limit,
		"offset":  offset,
	})
}

// parsePositiveInt parse un query param en int positif borné par max.
// Retourne fallback si vide ou invalide.
func parsePositiveInt(v string, fallback, max int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}
