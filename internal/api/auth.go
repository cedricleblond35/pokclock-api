package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

type authHandler struct {
	signer       *auth.Signer
	workerClient *auth.WorkerClient
	logger       *slog.Logger
	// superadminLicenseKeys : bootstrap Phase 0.B. Si la licence est dans ce
	// set, on force role=superadmin dans le JWT émis. À retirer une fois
	// que le Worker /verify renvoie role dans sa réponse.
	superadminLicenseKeys map[string]struct{}
}

type issueRequest struct {
	LicenseKey  string `json:"licenseKey"`
	HardwareID  string `json:"hardwareId"`
	WorkerToken string `json:"workerToken"`
}

type issueResponse struct {
	Token     string `json:"token"`
	Tier      string `json:"tier"`
	ExpiresAt string `json:"expiresAt"`
}

// issue valide la licence via le Worker Cloudflare et émet un JWT RS256 24h.
func (h *authHandler) issue(c echo.Context) error {
	var req issueRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}

	req.LicenseKey = strings.TrimSpace(req.LicenseKey)
	req.HardwareID = strings.TrimSpace(req.HardwareID)
	req.WorkerToken = strings.TrimSpace(req.WorkerToken)

	if req.LicenseKey == "" || req.HardwareID == "" || req.WorkerToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":  "missing_fields",
			"detail": "licenseKey, hardwareId and workerToken are required",
		})
	}

	verifyResp, err := h.workerClient.Verify(c.Request().Context(), auth.VerifyRequest{
		LicenseKey:  req.LicenseKey,
		HardwareID:  req.HardwareID,
		WorkerToken: req.WorkerToken,
	})
	if err != nil {
		h.logger.Error("worker verify failed", "err", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "license_verification_unavailable"})
	}

	if !verifyResp.Valid {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":  "license_invalid",
			"reason": verifyResp.Reason,
		})
	}

	tier := auth.Tier(verifyResp.Tier)
	role := auth.Role(verifyResp.Role)
	if !role.IsValid() {
		// Worker n'a pas encore exposé `role` (legacy) ou valeur inconnue
		// → fallback safe sur member.
		role = auth.RoleMember
	}
	// Override bootstrap Phase 0.B : promotion explicite en superadmin via
	// env SUPERADMIN_LICENSE_KEYS. À retirer quand le Worker propage role.
	if _, ok := h.superadminLicenseKeys[req.LicenseKey]; ok {
		role = auth.RoleSuperadmin
		h.logger.Info("license promoted to superadmin via env bootstrap",
			"license_prefix", req.LicenseKey[:min(8, len(req.LicenseKey))])
	}
	token, err := h.signer.Issue(auth.IssueParams{
		LicenseKey: req.LicenseKey,
		HardwareID: req.HardwareID,
		Tier:       tier,
		Role:       role,
		ClubID:     verifyResp.ClubID,
	})
	if err != nil {
		h.logger.Error("jwt signing failed", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "signing_failed"})
	}

	return c.JSON(http.StatusOK, issueResponse{
		Token:     token,
		Tier:      string(tier),
		ExpiresAt: verifyResp.ExpiresAt,
	})
}

// refresh est un stub pour Phase 0.A. La rotation des refresh tokens nécessite
// une table dédiée et sera câblée en Phase 0.B avec la persistance.
func (h *authHandler) refresh(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "refresh_not_yet_implemented",
		"phase": "0.B",
	})
}
