package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WorkerClient parle au Worker Cloudflare pokclock-license/verify pour valider
// une licence.
type WorkerClient struct {
	url    string
	http   *http.Client
}

// NewWorkerClient construit un client HTTP avec un timeout strict de 8s.
// Si http est nil, http.DefaultClient est utilisé (mais sans timeout explicite,
// préférer passer un http.Client configuré).
func NewWorkerClient(url string, httpClient *http.Client) *WorkerClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	} else if httpClient.Timeout == 0 {
		// Garde-fou : on ne veut pas qu'un Worker en rade bloque /auth/issue à l'infini.
		httpClient.Timeout = 8 * time.Second
	}
	return &WorkerClient{url: url, http: httpClient}
}

// VerifyRequest correspond au payload attendu par le Worker.
type VerifyRequest struct {
	LicenseKey  string `json:"licenseKey"`
	HardwareID  string `json:"hardwareId"`
	WorkerToken string `json:"token"`
}

// VerifyResponse représente le retour du Worker.
//
// Success (200) :
//
//	{
//	  "valid": true,
//	  "tier": "Solo|Club|Pro|Trial",
//	  "role": "member|admin|superadmin",
//	  "clubId": "uuid",
//	  "expiresAt": "...",
//	  "lastVerifiedAt": "..."
//	}
//
// Failure (4xx ou 200 avec valid=false) :
//
//	{ "valid": false, "reason": "revoked|expired|no_activation|bad_signature|mismatch|not_activated" }
//
// Compat : role et clubId sont optionnels côté Worker (introduits en Phase 0.B).
// Si absents, l'API émet le JWT avec role=member et clubId vide.
type VerifyResponse struct {
	Valid          bool   `json:"valid"`
	Tier           string `json:"tier,omitempty"`
	Role           string `json:"role,omitempty"`
	ClubID         string `json:"clubId,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	LastVerifiedAt string `json:"lastVerifiedAt,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Verify envoie la requête au Worker. Retourne une erreur en cas de défaillance
// HTTP/réseau. Si l'appel aboutit, le caller lit response.Valid pour décider
// d'émettre un JWT ou de rejeter.
func (c *WorkerClient) Verify(ctx context.Context, req VerifyRequest) (*VerifyResponse, error) {
	if c.url == "" {
		return nil, errors.New("worker url not configured")
	}
	if req.LicenseKey == "" || req.HardwareID == "" || req.WorkerToken == "" {
		return nil, errors.New("licenseKey, hardwareId and token are required")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal verify request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build worker request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "pokclock-api/1.0")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call worker: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, fmt.Errorf("read worker response: %w", err)
	}

	var parsed VerifyResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode worker response (status %d): %w", resp.StatusCode, err)
	}

	// 4xx du Worker = licence invalide. On retourne la réponse normalement
	// pour que le handler /auth/issue puisse répondre 401 avec la raison.
	return &parsed, nil
}
