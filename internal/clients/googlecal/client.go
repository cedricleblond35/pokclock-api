// Package googlecal porte un client HTTP minimal pour la Google Calendar API.
// Pas de SDK complet google.golang.org/api : on a besoin de 3 endpoints
// (insert / update / delete event sur primary calendar), un wrapper léger
// suffit.
package googlecal

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

const (
	apiBase = "https://www.googleapis.com/calendar/v3"
)

// Event est le payload minimal qu'on envoie. Pas de récurrence, pas
// d'attendees, pas de conférence : juste un event simple bloquant le slot
// dans l'agenda du joueur.
type Event struct {
	Summary     string    `json:"summary"`
	Description string    `json:"description,omitempty"`
	Location    string    `json:"location,omitempty"`
	Start       EventTime `json:"start"`
	End         EventTime `json:"end"`
}

type EventTime struct {
	DateTime string `json:"dateTime"`           // RFC3339
	TimeZone string `json:"timeZone,omitempty"` // ex "Europe/Paris"
}

// EventResponse est la réponse minimale qu'on consomme : l'ID pour pouvoir
// update/delete plus tard.
type EventResponse struct {
	ID   string `json:"id"`
	Link string `json:"htmlLink"`
}

// Client envoie les requêtes Calendar. L'accessToken est passé à chaque
// appel parce qu'il peut avoir été rafraîchi entre deux opérations.
type Client struct {
	HTTP *http.Client
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// InsertEvent crée un event dans le calendrier primary du joueur.
// Retourne l'ID de l'event créé pour persistance.
func (c *Client) InsertEvent(ctx context.Context, accessToken string, ev Event) (string, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("marshal event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/calendars/primary/events", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", parseAPIError(res, "insert event")
	}
	var parsed EventResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return parsed.ID, nil
}

// UpdateEvent remplace les champs de l'event avec ceux fournis. PATCH
// plutôt que PUT pour ne pas avoir à renvoyer tous les champs.
func (c *Client) UpdateEvent(ctx context.Context, accessToken, eventID string, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		apiBase+"/calendars/primary/events/"+eventID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return parseAPIError(res, "update event")
	}
	return nil
}

// DeleteEvent retire l'event du calendrier. 410 Gone et 404 Not Found sont
// traités comme un succès (idempotence : si l'user a déjà supprimé l'event
// manuellement côté Calendar, on ne re-throw pas).
func (c *Client) DeleteEvent(ctx context.Context, accessToken, eventID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		apiBase+"/calendars/primary/events/"+eventID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusGone, http.StatusNotFound:
		return nil
	default:
		return parseAPIError(res, "delete event")
	}
}

// RevokeToken : Google permet de révoquer un access ou refresh token via
// https://oauth2.googleapis.com/revoke. Utile quand le joueur clique
// "Déconnecter mon agenda" pour invalider le token côté Google aussi.
func (c *Client) RevokeToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/revoke?token="+token, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// 200 = OK, 400 invalid_token = déjà révoqué = OK pour nous
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusBadRequest {
		return nil
	}
	return parseAPIError(res, "revoke token")
}

// apiError est le payload typique d'erreur Google : {"error":{"code":401,"message":"..."}}
type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func parseAPIError(res *http.Response, op string) error {
	body, _ := io.ReadAll(res.Body)
	var e apiError
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s (%d)", op, e.Error.Message, e.Error.Code)
	}
	return errors.New(op + ": http " + res.Status)
}
