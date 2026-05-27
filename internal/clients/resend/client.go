// Package resend wraps l'API Resend (https://resend.com) pour envoyer
// les emails transactionnels du backend (confirmations d'inscriptions, etc.).
//
// On utilise l'API HTTP brute plutôt que le SDK Go officiel pour rester
// léger (1 dépendance en moins) et pour mirror exactement ce que fait le
// Worker Cloudflare côté StackPilot/backend.
package resend

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

const apiURL = "https://api.resend.com/emails"

// Client encapsule les appels Resend. Best-effort : retourne une erreur
// si l'envoi échoue mais le caller doit traiter ça comme non bloquant
// (l'inscription a déjà été persistée en base).
type Client struct {
	apiKey   string
	from     string
	http     *http.Client
}

// New construit un Client. apiKey + from sont obligatoires.
// from doit être au format "Name <email@domain>" (ex: "PokClock <noreply@pokclock.com>")
// et le domaine doit être vérifié dans Resend dashboard avant utilisation prod.
func New(apiKey, from string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	} else if httpClient.Timeout == 0 {
		httpClient.Timeout = 10 * time.Second
	}
	return &Client{apiKey: apiKey, from: from, http: httpClient}
}

// IsConfigured indique si le client peut effectivement envoyer (apiKey + from non vides).
// Utile pour skip silencieux en dev local sans clé Resend.
func (c *Client) IsConfigured() bool {
	return c.apiKey != "" && c.from != ""
}

// Message est un email à envoyer. Subject + ToEmail + HTML obligatoires.
// Text est optionnel (fallback texte brut généré depuis HTML si vide côté Resend).
type Message struct {
	ToEmail string
	ToName  string // optionnel — préfixe le To en "Nom <email>"
	Subject string
	HTML    string
	Text    string
}

// Send envoie le message. Best-effort côté caller : on retourne l'erreur
// pour log mais le caller ne doit pas la propager au user.
func (c *Client) Send(ctx context.Context, msg Message) error {
	if !c.IsConfigured() {
		return errors.New("resend: not configured (RESEND_API_KEY or EMAIL_FROM missing)")
	}
	if msg.ToEmail == "" || msg.Subject == "" || msg.HTML == "" {
		return errors.New("resend: missing required fields (to, subject, html)")
	}

	to := msg.ToEmail
	if msg.ToName != "" {
		to = fmt.Sprintf("%s <%s>", strings.ReplaceAll(msg.ToName, ",", ""), msg.ToEmail)
	}

	body := map[string]any{
		"from":    c.from,
		"to":      []string{to},
		"subject": msg.Subject,
		"html":    msg.HTML,
	}
	if msg.Text != "" {
		body["text"] = msg.Text
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call resend: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("resend %d: %s", resp.StatusCode, string(raw))
}
