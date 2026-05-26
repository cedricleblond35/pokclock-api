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

	"github.com/cedricleblond35/pokclock-api/internal/clients/workeradmin"
)

// clubLicensesHandler porte les routes /api/club/licenses, version
// "déléguée" des endpoints admin licenses. Différences avec adminLicensesHandler :
//  - Le club_id n'est pas dans le body, il est forcé depuis claims.ClubID
//  - Le caller doit avoir role admin ou superadmin (cf. router middlewares)
//  - Aucun cross-club possible : list / create / revoke filtrent par
//    claims.ClubID en dur
//
// La sync Postgres → D1 reste identique (best-effort, syncStatus dans la réponse).
type clubLicensesHandler struct {
	pool        *pgxpool.Pool
	logger      *slog.Logger
	workerAdmin *workeradmin.Client
}

type clubCreateLicenseRequest struct {
	Role      string  `json:"role"`
	ExpiresAt *string `json:"expiresAt,omitempty"`
}

func (h *clubLicensesHandler) list(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	limit := parsePositiveInt(c.QueryParam("limit"), defaultLicensesLimit, maxLicensesLimit)
	offset := parsePositiveInt(c.QueryParam("offset"), 0, 1_000_000)
	status := strings.TrimSpace(c.QueryParam("status"))
	role := strings.TrimSpace(c.QueryParam("role"))

	conds := []string{"club_id = $1"}
	args := []any{claims.ClubID}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "status = $"+strconv.Itoa(len(args)))
	}
	if role != "" {
		args = append(args, role)
		conds = append(conds, "role = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit, offset)
	limitPos := strconv.Itoa(len(args) - 1)
	offsetPos := strconv.Itoa(len(args))

	rows, err := h.pool.Query(c.Request().Context(),
		`SELECT id, club_id, license_key, hardware_id, role, status,
		        expires_at, last_seen_at, created_at, updated_at, revoked_at
		 FROM licenses
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY created_at DESC
		 LIMIT $`+limitPos+` OFFSET $`+offsetPos,
		args...,
	)
	if err != nil {
		h.logger.Error("club list licenses", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	defer rows.Close()

	out := make([]license, 0, limit)
	for rows.Next() {
		var l license
		if err := rows.Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
			&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt); err != nil {
			h.logger.Error("scan license", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		out = append(out, l)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"licenses": out,
		"limit":    limit,
		"offset":   offset,
	})
}

// create est l'équivalent club-scoped de adminLicensesHandler.create.
// Restriction supplémentaire : un admin de club ne peut PAS créer une license
// avec role=superadmin (sinon il pourrait s'auto-promouvoir global).
func (h *clubLicensesHandler) create(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}

	var req clubCreateLicenseRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Role == "" {
		req.Role = "member"
	}
	if !allowedLicenseRoles[req.Role] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_role"})
	}
	// Un admin de club ne peut pas créer un superadmin (escalation interdite).
	// Un superadmin global peut techniquement passer ici, mais il devrait
	// utiliser /api/admin/licenses ; on bloque quand même pour rester strict.
	if req.Role == "superadmin" {
		return c.JSON(http.StatusForbidden, map[string]string{
			"error":  "cannot_create_superadmin",
			"detail": "Les superadmins se créent via /api/admin/licenses.",
		})
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_expires_at"})
		}
		expiresAt = &t
	}

	// Récupération email + plan du club (préchecks pour la sync D1).
	var clubEmailPtr *string
	var clubPlan string
	err := h.pool.QueryRow(c.Request().Context(),
		`SELECT email, plan FROM clubs WHERE id = $1`, claims.ClubID,
	).Scan(&clubEmailPtr, &clubPlan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "club_not_found"})
		}
		h.logger.Error("club fetch email/plan", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	clubEmail := ""
	if clubEmailPtr != nil {
		clubEmail = strings.TrimSpace(*clubEmailPtr)
	}
	if clubEmail == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":  "club_email_required",
			"detail": "Définis un email sur ton club avant de générer une license.",
		})
	}
	tier, ok := planToTier[clubPlan]
	if !ok {
		h.logger.Error("unknown plan", "plan", clubPlan, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unknown_plan"})
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
		claims.ClubID, licenseKey, tokenHash, req.Role, expiresAt,
	).Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
		&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "license_key_collision"})
		}
		h.logger.Error("insert license", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	syncStatus, syncErr := syncCreateToWorker(c.Request().Context(), h.workerAdmin, h.logger,
		workeradmin.CreateLicenseInput{
			LicenseKey:    licenseKey,
			Email:         clubEmail,
			Tier:          tier,
			Role:          req.Role,
			ClubID:        claims.ClubID,
			ExpiresInDays: expiresInDays(expiresAt),
		})

	return c.JSON(http.StatusCreated, createLicenseResponse{
		License:     l,
		LicenseKey:  licenseKey,
		WorkerToken: workerToken,
		SyncStatus:  syncStatus,
		SyncError:   syncErr,
	})
}

// revoke ne peut révoquer que des licenses du même club que le caller.
// Une tentative de révoquer une license d'un autre club retourne 404.
func (h *clubLicensesHandler) revoke(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_claims"})
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing_id"})
	}

	var l license
	err := h.pool.QueryRow(c.Request().Context(),
		`UPDATE licenses SET status = 'revoked', revoked_at = now()
		 WHERE id = $1 AND club_id = $2
		 RETURNING id, club_id, license_key, hardware_id, role, status,
		           expires_at, last_seen_at, created_at, updated_at, revoked_at`,
		id, claims.ClubID,
	).Scan(&l.ID, &l.ClubID, &l.LicenseKey, &l.HardwareID, &l.Role, &l.Status,
		&l.ExpiresAt, &l.LastSeenAt, &l.CreatedAt, &l.UpdatedAt, &l.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Soit l'id n'existe pas, soit il appartient à un autre club —
			// dans les deux cas, 404 (on ne révèle pas l'existence cross-club).
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not_found"})
		}
		h.logger.Error("club revoke license", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	syncStatus, syncErr := syncRevokeToWorker(c.Request().Context(), h.workerAdmin, h.logger, l.LicenseKey)

	return c.JSON(http.StatusOK, revokeLicenseResponse{
		license:    l,
		SyncStatus: syncStatus,
		SyncError:  syncErr,
	})
}

// syncCreateToWorker / syncRevokeToWorker — helpers partagés avec
// adminLicensesHandler. Extraits en fonctions package-level pour éviter
// la duplication entre les deux handlers (admin + club).
func syncCreateToWorker(
	ctx context.Context,
	client *workeradmin.Client,
	logger *slog.Logger,
	in workeradmin.CreateLicenseInput,
) (string, string) {
	if client == nil || !client.IsConfigured() {
		return "skipped", ""
	}
	if err := client.CreateLicense(ctx, in); err != nil {
		logger.Error("worker sync create license failed",
			"err", err, "license_key", in.LicenseKey, "club_id", in.ClubID)
		return "failed", err.Error()
	}
	logger.Info("worker sync create license ok",
		"license_key", in.LicenseKey, "club_id", in.ClubID, "tier", in.Tier, "role", in.Role)
	return "ok", ""
}

func syncRevokeToWorker(
	ctx context.Context,
	client *workeradmin.Client,
	logger *slog.Logger,
	licenseKey string,
) (string, string) {
	if client == nil || !client.IsConfigured() {
		return "skipped", ""
	}
	if err := client.RevokeLicense(ctx, licenseKey); err != nil {
		logger.Error("worker sync revoke license failed", "err", err, "license_key", licenseKey)
		return "failed", err.Error()
	}
	logger.Info("worker sync revoke license ok", "license_key", licenseKey)
	return "ok", ""
}
