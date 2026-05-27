package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	googleoauth2 "golang.org/x/oauth2/google"

	"github.com/cedricleblond35/pokclock-api/internal/clients/googlecal"
)

// calendarSync : service partagé pour pousser les events Calendar quand le
// joueur a connecté son agenda. Best-effort : tout échec est loggé en warn
// sans bloquer la lifecycle de la registration.
type calendarSync struct {
	pool         *pgxpool.Pool
	logger       *slog.Logger
	cal          *googlecal.Client
	clientID     string
	clientSecret string
}

func newCalendarSync(pool *pgxpool.Pool, logger *slog.Logger, cal *googlecal.Client, clientID, clientSecret string) *calendarSync {
	return &calendarSync{
		pool: pool, logger: logger, cal: cal,
		clientID: clientID, clientSecret: clientSecret,
	}
}

// enabled : Calendar sync activé uniquement si les credentials OAuth sont set.
func (s *calendarSync) enabled() bool {
	return s != nil && s.cal != nil && s.clientID != "" && s.clientSecret != ""
}

// OnRegistration : crée l'event Calendar pour la registration si le joueur
// a Calendar connecté. Met à jour tournament_registrations.google_event_id
// avec l'ID retourné. Fire-and-forget : appelle ça via `go` dans le caller.
func (s *calendarSync) OnRegistration(ctx context.Context, registrationID string) {
	if !s.enabled() {
		return
	}

	var (
		playerID       *string
		tournamentName string
		startAt        time.Time
		clubName       string
		buyIn          string
		status         string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT r.player_id, t.name, t.start_at, c.name, t.buy_in_amount::text, r.status
		 FROM tournament_registrations r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 JOIN clubs c ON c.id = t.club_id
		 WHERE r.id = $1`,
		registrationID,
	).Scan(&playerID, &tournamentName, &startAt, &clubName, &buyIn, &status)
	if err != nil {
		s.logger.Warn("calendar sync: registration lookup", "err", err, "reg_id", registrationID)
		return
	}
	if playerID == nil {
		return // inscription anonyme, pas de Calendar
	}

	accessToken, ok := s.fetchAccessToken(ctx, *playerID)
	if !ok {
		return // pas connecté Calendar, ou refresh failed
	}

	ev := buildEvent(tournamentName, clubName, startAt, buyIn, status)
	eventID, err := s.cal.InsertEvent(ctx, accessToken, ev)
	if err != nil {
		s.logger.Warn("calendar insert event", "err", err, "reg_id", registrationID)
		return
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE tournament_registrations SET google_event_id = $2 WHERE id = $1`,
		registrationID, eventID); err != nil {
		s.logger.Warn("calendar persist event id", "err", err, "reg_id", registrationID)
	}
}

// OnStatusChange : appelle quand l'admin confirm/reject une registration ou
// quand le joueur cancel. Update ou delete l'event selon le nouveau status.
func (s *calendarSync) OnStatusChange(ctx context.Context, registrationID, newStatus string) {
	if !s.enabled() {
		return
	}

	var (
		playerID       *string
		eventID        *string
		tournamentName string
		startAt        time.Time
		clubName       string
		buyIn          string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT r.player_id, r.google_event_id, t.name, t.start_at, c.name, t.buy_in_amount::text
		 FROM tournament_registrations r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 JOIN clubs c ON c.id = t.club_id
		 WHERE r.id = $1`,
		registrationID,
	).Scan(&playerID, &eventID, &tournamentName, &startAt, &clubName, &buyIn)
	if err != nil {
		s.logger.Warn("calendar sync: status change lookup", "err", err, "reg_id", registrationID)
		return
	}
	if playerID == nil || eventID == nil {
		return
	}

	accessToken, ok := s.fetchAccessToken(ctx, *playerID)
	if !ok {
		return
	}

	switch newStatus {
	case "confirmed":
		ev := buildEvent(tournamentName, clubName, startAt, buyIn, "confirmed")
		if err := s.cal.UpdateEvent(ctx, accessToken, *eventID, ev); err != nil {
			s.logger.Warn("calendar update event", "err", err, "reg_id", registrationID)
		}
	case "rejected", "cancelled":
		if err := s.cal.DeleteEvent(ctx, accessToken, *eventID); err != nil {
			s.logger.Warn("calendar delete event", "err", err, "reg_id", registrationID)
			return
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE tournament_registrations SET google_event_id = NULL WHERE id = $1`,
			registrationID); err != nil {
			s.logger.Warn("calendar clear event id", "err", err, "reg_id", registrationID)
		}
	}
}

// OnPlayerCancelByToken : helper pour le flow cancel via cancel_token (magic
// link email). Anonyme côté caller mais peut avoir un player_id en DB.
func (s *calendarSync) OnPlayerCancelByToken(ctx context.Context, token string) {
	if !s.enabled() {
		return
	}
	var regID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM tournament_registrations WHERE cancel_token = $1`,
		token,
	).Scan(&regID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("calendar cancel by token lookup", "err", err)
		}
		return
	}
	s.OnStatusChange(ctx, regID, "cancelled")
}

// fetchAccessToken charge le token, rafraîchit si expiré, persiste le nouveau.
// Retourne (accessToken, true) si OK, ("", false) sinon (pas connecté /
// refresh échoue / DB error).
func (s *calendarSync) fetchAccessToken(ctx context.Context, playerID string) (string, bool) {
	var (
		access, refresh, scope string
		expiresAt              time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT access_token, refresh_token, expires_at, scope
		 FROM player_google_tokens WHERE player_id = $1`,
		playerID,
	).Scan(&access, &refresh, &expiresAt, &scope)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("calendar fetch token", "err", err, "player_id", playerID)
		}
		return "", false
	}

	// Token encore valide pendant > 1 min : on garde.
	if time.Now().Add(1 * time.Minute).Before(expiresAt) {
		return access, true
	}

	// Refresh
	conf := &oauth2.Config{
		ClientID:     s.clientID,
		ClientSecret: s.clientSecret,
		Endpoint:     googleoauth2.Endpoint,
	}
	tokSource := conf.TokenSource(ctx, &oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		Expiry:       expiresAt,
		TokenType:    "Bearer",
	})
	newTok, err := tokSource.Token()
	if err != nil {
		s.logger.Warn("calendar refresh token", "err", err, "player_id", playerID)
		return "", false
	}

	if newTok.AccessToken != access {
		_, _ = s.pool.Exec(ctx,
			`UPDATE player_google_tokens
			 SET access_token = $2, expires_at = $3, updated_at = now()
			 WHERE player_id = $1`,
			playerID, newTok.AccessToken, newTok.Expiry)
	}
	return newTok.AccessToken, true
}

// buildEvent construit le payload Calendar selon le status de la registration.
func buildEvent(tournamentName, clubName string, startAt time.Time, buyIn, status string) googlecal.Event {
	prefix := ""
	switch status {
	case "pending":
		prefix = "[En attente] "
	case "confirmed":
		prefix = ""
	}
	summary := prefix + tournamentName

	// Durée par défaut : 4h. On n'a pas l'info exacte côté DB, c'est un
	// raisonnable pour un tournoi MTT. Plus tard on pourra estimer via le
	// PokClock structure analyzer.
	end := startAt.Add(4 * time.Hour)

	desc := fmt.Sprintf(
		"Tournoi organisé par %s.\nBuy-in : %s €\nStatut inscription : %s\n\nGéré via pokclock.com",
		clubName, strings.ReplaceAll(buyIn, ".", ","), translateStatus(status))

	return googlecal.Event{
		Summary:     summary,
		Description: desc,
		Location:    clubName,
		Start:       googlecal.EventTime{DateTime: startAt.Format(time.RFC3339), TimeZone: "Europe/Paris"},
		End:         googlecal.EventTime{DateTime: end.Format(time.RFC3339), TimeZone: "Europe/Paris"},
	}
}

func translateStatus(s string) string {
	switch s {
	case "pending":
		return "en attente de validation"
	case "confirmed":
		return "confirmée"
	case "rejected":
		return "refusée"
	case "cancelled":
		return "annulée"
	default:
		return s
	}
}
