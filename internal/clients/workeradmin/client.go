// Package workeradmin appelle les endpoints admin du Worker Cloudflare
// `pokclock-license` pour synchroniser les licenses créées en Postgres avec
// la base D1 qui sert le /verify de l'app cliente.
//
// Contrat avec le Worker (cf. backend/src/routes/admin.ts) :
//   - POST /admin/license   { email, tier, role, clubId, licenseKey, expiresInDays, sendEmail }
//   - POST /admin/revoke    { licenseKey }
// Auth : Bearer ADMIN_TOKEN.
//
// Best-effort : le caller doit traiter une erreur de sync comme non-bloquante
// pour la création/révocation en Postgres (le record local reste la source de
// vérité business, la D1 peut être resync manuellement).
package workeradmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client encapsule l'appel aux endpoints admin du Worker.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New construit un Client. baseURL est sans slash final.
// Si http est nil, un client avec timeout 8s est créé.
// Si token est vide, les méthodes retournent ErrNotConfigured.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	} else if httpClient.Timeout == 0 {
		httpClient.Timeout = 8 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

// ErrNotConfigured est retournée si baseURL ou token sont vides.
// Le caller doit traiter cela comme "sync désactivé" (typiquement en dev local
// ou si le secret n'a pas encore été créé en Swarm).
var ErrNotConfigured = errors.New("workeradmin: url or token not configured")

// IsConfigured indique si le client peut effectivement appeler le Worker.
func (c *Client) IsConfigured() bool {
	return c.baseURL != "" && c.token != ""
}

// CreateLicenseInput est le payload envoyé à POST /admin/license.
// Les champs reproduisent l'API Worker, sans superflu côté Go.
type CreateLicenseInput struct {
	LicenseKey    string
	Email         string
	Tier          string // "Solo" | "Club" | "Pro" | "Trial"
	Role          string // "member" | "admin" | "superadmin"
	ClubID        string // UUID du club côté pokclock-api, NULL accepté
	ExpiresInDays *int   // nil = perpétuelle
}

// CreateLicense insère la license côté Worker. Idempotent côté caller : si la
// clé existe déjà en D1, le Worker répond 409 et l'erreur retournée porte le
// flag `Duplicate`.
func (c *Client) CreateLicense(ctx context.Context, in CreateLicenseInput) error {
	if !c.IsConfigured() {
		return ErrNotConfigured
	}
	body := map[string]any{
		"licenseKey": in.LicenseKey,
		"email":      in.Email,
		"tier":       in.Tier,
		"role":       in.Role,
		"sendEmail":  false, // l'app cliente ne doit pas recevoir le mail du Worker
	}
	if in.ClubID != "" {
		body["clubId"] = in.ClubID
	}
	if in.ExpiresInDays != nil {
		body["expiresInDays"] = *in.ExpiresInDays
	}
	return c.post(ctx, "/admin/license", body)
}

// RevokeLicense marque une license comme revoked côté Worker D1.
// Le Worker répond 404 si la clé est inconnue : on retourne nil dans ce cas
// (sync revoke sur une license qui n'existe pas côté Worker = no-op, pas
// d'erreur réelle).
func (c *Client) RevokeLicense(ctx context.Context, licenseKey string) error {
	if !c.IsConfigured() {
		return ErrNotConfigured
	}
	err := c.post(ctx, "/admin/revoke", map[string]any{"licenseKey": licenseKey})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}

// APIError porte le détail d'une réponse non-2xx du Worker.
type APIError struct {
	Status    int
	Code      string // ex: "duplicate_key", "not_found"
	Message   string
	Duplicate bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("workeradmin: %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) post(ctx context.Context, path string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pokclock-api/workeradmin")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call worker: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	apiErr := &APIError{Status: resp.StatusCode}
	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
		apiErr.Code = parsed.Error
		apiErr.Message = parsed.Message
	} else {
		apiErr.Message = string(raw)
	}
	apiErr.Duplicate = apiErr.Code == "duplicate_key"
	return apiErr
}
