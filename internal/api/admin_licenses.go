package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
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

// adminLicensesHandler porte les routes CRUD sur les licenses.
// Protégé en amont par jwtAuth + requireRole(superadmin) dans router.go.
type adminLicensesHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type license struct {
	ID              string     `json:"id"`
	ClubID          string     `json:"clubId"`
	LicenseKey      string     `json:"licenseKey"`
	HardwareID      *string    `json:"hardwareId,omitempty"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	LastSeenAt      *time.Time `json:"lastSeenAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
}

// createLicenseRequest est le payload d'une création de license côté admin.
// Le license_key est généré côté serveur (jamais accepté en input), de même
// pour le worker_token. Seul le hash est stocké en base.
type createLicenseRequest struct {
	ClubID    string  `json:"clubId"`
	Role      string  `json:"role"`
	ExpiresAt *string `json:"expiresAt,omitempty"` // ISO-8601 (RFC3339)
}

// createLicenseResponse expose UNE SEULE FOIS les valeurs en clair des secrets
// générés (license_key + worker_token). Le caller (UI admin) doit les copier
// immédiatement, on ne pourra plus les régénérer.
type createLicenseResponse struct {
	License     license `json:"license"`
	LicenseKey  string  `json:"licenseKey"`  // identique à license.licenseKey (commodité)
	WorkerToken string  `json:"workerToken"` // affiché 1× puis perdu
}

const (
	defaultLicensesLimit = 50
	maxLicensesLimit     = 200
)

var allowedLicenseRoles = map[string]bool{
	"member":     true,
	"admin":      true,
	"superadmin": true,
}

// list renvoie les licenses, filtrées optionnellement par clubId/status/role.
func (h *adminLicensesHandler) list(c echo.Context) error {
	limit := parsePositiveInt(c.QueryParam("limit"), defaultLicensesLimit, maxLicensesLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)
	clubID := strings.TrimSpace(c.QueryParam("clubId"))
	status := strings.TrimSpace(c.QueryParam("status"))
	role := strings.TrimSpace(c.QueryParam("role"))

	conds := []string{}
	args := []any{}
	if clubID != "" {
		args = append(args, clubID)
		conds = append(conds, "club_id = $"+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "status = $"+strconv.Itoa(len(args)))
	}
	if role != "" {
		args = append(args, role)
		conds = append(conds, "role = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, club_id, license_key, hardware_id, role, status,
		        expires_at, last_seen_at, created_at, updated_at, revoked_at
		 FROM licenses`+where+`
		 ORDER BY created_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("list licenses", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	licenses := make([]license, 0, limit)
	for rows.Next() {
		var l license
		if err := rows.Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
			&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt); err != nil {
			h.logger.Error("scan license", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		licenses = append(licenses, l)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate licenses", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_iterate"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"licenses": licenses,
		"limit":    limit,
		"offset":   offset,
	})
}

// create génère une nouvelle license + worker_token, stocke uniquement le hash
// du token, et retourne UNE SEULE FOIS les valeurs en clair au caller.
func (h *adminLicensesHandler) create(c echo.Context) error {
	var req createLicenseRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.ClubID = strings.TrimSpace(req.ClubID)
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.ClubID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_club_id"})
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if !allowedLicenseRoles[req.Role] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_role"})
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_expires_at"})
		}
		expiresAt = &t
	}

	licenseKey, err := generateLicenseKey()
	if err != nil {
		h.logger.Error("generate license key", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}
	workerToken, err := generateWorkerToken()
	if err != nil {
		h.logger.Error("generate worker token", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gen_failed"})
	}
	tokenHash := sha256Hex(workerToken)

	var l license
	err = h.pool.QueryRow(c.Request().Context(),
		`INSERT INTO licenses (club_id, license_key, worker_token_hash, role, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, club_id, license_key, hardware_id, role, status,
		           expires_at, last_seen_at, created_at, updated_at, revoked_at`,
		req.ClubID, licenseKey, tokenHash, req.Role, expiresAt,
	).Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
		&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "club_not_found"})
		}
		if isUniqueViolation(err) {
			// Très rare avec 20 chars random, mais on traite proprement.
			return c.JSON(http.StatusConflict, map[string]string{"error": "license_key_collision"})
		}
		h.logger.Error("insert license", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	return c.JSON(http.StatusCreated, createLicenseResponse{
		License:     l,
		LicenseKey:  licenseKey,
		WorkerToken: workerToken,
	})
}

// revoke marque une license comme revoked. Idempotent : revoke un déjà-revoked
// est OK. Le Worker Cloudflare DOIT être notifié séparément (Phase 0.B-2) pour
// invalider côté D1.
func (h *adminLicensesHandler) revoke(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}
	var l license
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE licenses SET status = 'revoked', revoked_at = now()
		 WHERE id = $1
		 RETURNING id, club_id, license_key, hardware_id, role, status,
		           expires_at, last_seen_at, created_at, updated_at, revoked_at`,
		id,
	).Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
		&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("revoke license", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}
	return c.JSON(http.StatusOK, l)
}

// generateLicenseKey produit une clé du format POK-XXXX-XXXX-XXXX-XXXX
// (4 groupes base32 sans I/O/0/1 pour lisibilité humaine).
func generateLicenseKey() (string, error) {
	// 10 bytes random → 16 chars base32 (sans padding), groupés en 4×4.
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	enc = strings.ToUpper(enc)
	// Format POK-XXXX-XXXX-XXXX-XXXX
	if len(enc) < 16 {
		return "", errors.New("encoded key too short")
	}
	return "POK-" + enc[0:4] + "-" + enc[4:8] + "-" + enc[8:12] + "-" + enc[12:16], nil
}

// generateWorkerToken produit un token HMAC de 32 bytes encodés en hex (64 chars).
// Le token est destiné à être stocké côté client (license.json) et envoyé à
// chaque appel /api/auth/issue.
func generateWorkerToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sha256Hex retourne le digest hex SHA-256 d'une string. Utilisé pour stocker
// les worker_tokens en base sans jamais conserver la valeur en clair.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// isForeignKeyViolation détecte une violation FK (code Postgres 23503).
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23503") || strings.Contains(err.Error(), "foreign key")
}
