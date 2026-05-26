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

// clubMembersHandler porte le CRUD members. Toutes les routes sont scopées
// par claims.ClubID — aucune fuite cross-club possible (même protection que
// clubLicensesHandler).
type clubMembersHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type member struct {
	ID        string     `json:"id"`
	ClubID    string     `json:"clubId"`
	FirstName string     `json:"firstName"`
	LastName  string     `json:"lastName"`
	Email     *string    `json:"email,omitempty"`
	Notes     *string    `json:"notes,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

type createMemberRequest struct {
	FirstName string  `json:"firstName"`
	LastName  string  `json:"lastName"`
	Email     *string `json:"email,omitempty"`
	Notes     *string `json:"notes,omitempty"`
}

type updateMemberRequest struct {
	FirstName *string `json:"firstName,omitempty"`
	LastName  *string `json:"lastName,omitempty"`
	Email     *string `json:"email,omitempty"`
	Notes     *string `json:"notes,omitempty"`
	Status    *string `json:"status,omitempty"`
}

const (
	defaultMembersLimit = 100
	maxMembersLimit     = 500
)

var allowedMemberStatuses = map[string]bool{
	"active":   true,
	"inactive": true,
}

func (h *clubMembersHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultMembersLimit, maxMembersLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, club_id, first_name, last_name, email, notes, status,
		        created_at, updated_at
		 FROM members
		 WHERE club_id = $1
		 ORDER BY last_name, first_name
		 LIMIT $2 OFFSET $3`,
		claims.ClubID, limit, offset,
	)
	if err != nil {
		h.logger.Error("club list members", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]member, 0, limit)
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.ID, &m.ClubID, &m.FirstName, &m.LastName, &m.Email,
			&m.Notes, &m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			h.logger.Error("scan member", "err", err)
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

func (h *clubMembersHandler) create(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var req createMemberRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	if req.FirstName == "" || req.LastName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":  "missing_fields",
			"detail": "firstName et lastName sont requis",
		})
	}

	var m member
	err := h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO members (club_id, first_name, last_name, email, notes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, club_id, first_name, last_name, email, notes, status,
		           created_at, updated_at`,
		claims.ClubID, req.FirstName, req.LastName, req.Email, req.Notes,
	).Scan(&m.ID, &m.ClubID, &m.FirstName, &m.LastName, &m.Email, &m.Notes,
		&m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		h.logger.Error("insert member", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusCreated, m)
}

func (h *clubMembersHandler) update(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var req updateMemberRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	if req.Status != nil {
		s := strings.ToLower(strings.TrimSpace(*req.Status))
		if !allowedMemberStatuses[s] {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_status"})
		}
		*req.Status = s
	}
	if req.FirstName != nil {
		t := strings.TrimSpace(*req.FirstName)
		if t == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "empty_first_name"})
		}
		*req.FirstName = t
	}
	if req.LastName != nil {
		t := strings.TrimSpace(*req.LastName)
		if t == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "empty_last_name"})
		}
		*req.LastName = t
	}

	var m member
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE members
		 SET first_name = COALESCE($3, first_name),
		     last_name  = COALESCE($4, last_name),
		     email      = COALESCE($5, email),
		     notes      = COALESCE($6, notes),
		     status     = COALESCE($7, status)
		 WHERE id = $1 AND club_id = $2
		 RETURNING id, club_id, first_name, last_name, email, notes, status,
		           created_at, updated_at`,
		id, claims.ClubID, req.FirstName, req.LastName, req.Email, req.Notes, req.Status,
	).Scan(&m.ID, &m.ClubID, &m.FirstName, &m.LastName, &m.Email, &m.Notes,
		&m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("update member", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, m)
}

// delete supprime un membre. Cascade ON DELETE SET NULL côté licenses :
// les licenses du membre passent member_id=NULL mais ne sont PAS révoquées.
// L'admin doit explicitement réassigner ou révoquer ces licenses.
func (h *clubMembersHandler) delete(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	tag, err := h.pool.Exec(c.Request().Context(),
		`DELETE FROM members WHERE id = $1 AND club_id = $2`,
		id, claims.ClubID,
	)
	if err != nil {
		h.logger.Error("delete member", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
	}
	return c.NoContent(http.StatusNoContent)
}

// assignLicense lie ou délie une license d'un membre. Body : { memberId } —
// null pour désassigner. La license et le membre doivent appartenir au
// même club que le caller (vérifié dans la requête en filtrant club_id).
type assignMemberRequest struct {
	MemberID *string `json:"memberId"`
}

func (h *clubMembersHandler) assignLicense(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	licenseID := strings.TrimSpace(c.Param("id"))
	if licenseID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var req assignMemberRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}

	// Si memberId fourni, vérifier qu'il appartient au même club.
	if req.MemberID != nil && *req.MemberID != "" {
		var count int
		err := h.pool.QueryRow(c.Request().Context(),
			`SELECT COUNT(*) FROM members WHERE id = $1 AND club_id = $2`,
			*req.MemberID, claims.ClubID,
		).Scan(&count)
		if err != nil {
			h.logger.Error("verify member club", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
		}
		if count == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "member_not_found"})
		}
	}

	// memberId = nil ou "" → désassigner (NULL en SQL).
	var memberIDArg any = nil
	if req.MemberID != nil && strings.TrimSpace(*req.MemberID) != "" {
		memberIDArg = strings.TrimSpace(*req.MemberID)
	}

	tag, err := h.pool.Exec(c.Request().Context(),
		`UPDATE licenses SET member_id = $3 WHERE id = $1 AND club_id = $2`,
		licenseID, claims.ClubID, memberIDArg,
	)
	if err != nil {
		h.logger.Error("assign license to member", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	if tag.RowsAffected() == 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "license_not_found"})
	}
	return c.NoContent(http.StatusNoContent)
}

