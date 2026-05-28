package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// playersDashboardHandler : Phase 0.I. Endpoint agrégé pour le dashboard joueur.
// Pour chaque club où le joueur est membre actif, on renvoie :
//   - club (id, slug, name)
//   - rang du joueur dans le leaderboard global du club (myRank)
//   - saison active courante + rang du joueur dans cette saison (activeSeason)
//   - 3 prochains tournois "open" + statut d'inscription du joueur
//
// Le rang est calculé en agrégeant tournament_results par (lower(first_name),
// lower(last_name)) — même règle que publicResultsHandler.getLeaderboard pour
// rester cohérent. Le joueur est identifié par player_id OU par nom (match
// insensible à la casse) pour englober les résultats publiés avant qu'il ait
// son compte cloud.
//
// Tri des clubs : par dernière activité (max start_at d'un tournoi du club),
// fallback sur requested_at de la membership si aucun tournoi.
type playersDashboardHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type clubWidgetClub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type clubWidgetRank struct {
	Position          int `json:"position"`
	TotalEntries      int `json:"totalEntries"`
	TotalPoints       int `json:"totalPoints"`
	TournamentsPlayed int `json:"tournamentsPlayed"`
}

type clubWidgetSeason struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	StartsAt  time.Time       `json:"startsAt"`
	EndsAt    time.Time       `json:"endsAt"`
	MyRank    *clubWidgetRank `json:"myRank,omitempty"`
}

type clubWidgetUpcoming struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	StartAt             time.Time `json:"startAt"`
	Audience            string    `json:"audience"`
	SpotsLeft           *int      `json:"spotsLeft,omitempty"`
	MyRegistrationStatus *string  `json:"myRegistrationStatus,omitempty"`
}

type clubWidget struct {
	Club                clubWidgetClub       `json:"club"`
	MyRank              *clubWidgetRank      `json:"myRank,omitempty"`
	ActiveSeason        *clubWidgetSeason    `json:"activeSeason,omitempty"`
	UpcomingTournaments []clubWidgetUpcoming `json:"upcomingTournaments"`
}

// getDashboard : GET /api/players/me/dashboard
// Renvoie { widgets: [...] } trié par dernière activité.
func (h *playersDashboardHandler) getDashboard(c echo.Context) error {
	claims := PlayerClaimsFromContext(c)
	if claims == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}
	ctx := c.Request().Context()

	var firstName, lastName, email string
	err := h.pool.QueryRow(ctx,
		`SELECT first_name, last_name, email FROM players WHERE id = $1 AND status = 'active'`,
		claims.PlayerID,
	).Scan(&firstName, &lastName, &email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusGone, map[string]string{"error": "player_gone"})
		}
		h.logger.Error("dashboard fetch profile", "err", err, "player_id", claims.PlayerID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}

	// Phase 0.I : retrigger l'auto-link 0.H à chaque chargement du dashboard.
	// Permet au joueur de voir une nouvelle adhésion apparaître après que
	// l'admin a publié un tournoi le concernant, sans avoir à se relogger.
	// Idempotent : ON CONFLICT DO NOTHING sur player_club_memberships.
	autoLinkPlayerToClubs(ctx, h.pool, h.logger, claims.PlayerID, email)

	rows, err := h.pool.Query(ctx,
		`SELECT c.id, c.slug, c.name,
		        COALESCE(
		            (SELECT MAX(t.start_at)
		             FROM published_tournaments t
		             WHERE t.club_id = c.id AND t.status IN ('open','closed')),
		            m.requested_at
		        ) AS last_activity
		 FROM player_club_memberships m
		 JOIN clubs c ON c.id = m.club_id
		 WHERE m.player_id = $1 AND m.status = 'active'
		   AND c.status = 'active'
		 ORDER BY last_activity DESC NULLS LAST`,
		claims.PlayerID,
	)
	if err != nil {
		h.logger.Error("dashboard list active clubs", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_read"})
	}
	type clubRow struct {
		ID, Slug, Name string
	}
	var clubsList []clubRow
	for rows.Next() {
		var cr clubRow
		var lastActivity *time.Time
		if err := rows.Scan(&cr.ID, &cr.Slug, &cr.Name, &lastActivity); err != nil {
			rows.Close()
			h.logger.Error("dashboard scan club", "err", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_scan"})
		}
		clubsList = append(clubsList, cr)
	}
	rows.Close()

	widgets := make([]clubWidget, 0, len(clubsList))
	for _, cr := range clubsList {
		w := clubWidget{
			Club: clubWidgetClub{ID: cr.ID, Slug: cr.Slug, Name: cr.Name},
		}
		w.MyRank = h.computeRank(ctx, cr.ID, claims.PlayerID, firstName, lastName, nil)
		w.ActiveSeason = h.fetchActiveSeason(ctx, cr.ID, claims.PlayerID, firstName, lastName)
		w.UpcomingTournaments = h.fetchUpcoming(ctx, cr.ID, claims.PlayerID)
		widgets = append(widgets, w)
	}
	return c.JSON(http.StatusOK, map[string]any{"widgets": widgets})
}

// computeRank : agrège tournament_results pour le club (ou la saison si seasonID
// est fourni) et calcule la position du joueur. Retourne nil si le joueur
// n'a aucun résultat enregistré dans ce scope.
func (h *playersDashboardHandler) computeRank(ctx context.Context, clubID, playerID,
	firstName, lastName string, seasonID *string) *clubWidgetRank {

	// Filtre commun pour matcher le joueur courant dans tournament_results :
	// soit le player_id est renseigné, soit on tombe sur un fallback nom.
	playerMatch := `(r.player_id = $2 OR (lower(r.first_name) = lower($3) AND lower(r.last_name) = lower($4)))`

	var (
		myPoints, myTournaments int
		seasonFilter            string
		args                    []any
	)
	args = []any{clubID, playerID, firstName, lastName}
	if seasonID != nil {
		seasonFilter = ` AND t.season_id = $5`
		args = append(args, *seasonID)
	}

	err := h.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.points), 0)::int, COUNT(*)::int
		 FROM tournament_results r
		 JOIN published_tournaments t ON t.id = r.published_tournament_id
		 WHERE t.club_id = $1 AND `+playerMatch+seasonFilter,
		args...,
	).Scan(&myPoints, &myTournaments)
	if err != nil {
		h.logger.Error("dashboard player stats", "err", err)
		return nil
	}
	if myTournaments == 0 {
		return nil
	}

	// Combien d'entrées du leaderboard ont plus de points que moi ?
	// On regroupe sur (lower(first_name), lower(last_name)) comme dans le
	// leaderboard public, puis on compte combien dépassent myPoints.
	betterArgs := []any{clubID, myPoints}
	betterSeasonFilter := ""
	if seasonID != nil {
		betterSeasonFilter = ` AND t.season_id = $3`
		betterArgs = append(betterArgs, *seasonID)
	}
	var betterCount int
	err = h.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM (
		    SELECT SUM(r.points)::int AS pts
		    FROM tournament_results r
		    JOIN published_tournaments t ON t.id = r.published_tournament_id
		    WHERE t.club_id = $1`+betterSeasonFilter+`
		    GROUP BY lower(r.first_name), lower(r.last_name)
		    HAVING SUM(r.points)::int > $2
		 ) sub`,
		betterArgs...,
	).Scan(&betterCount)
	if err != nil {
		h.logger.Error("dashboard count better", "err", err)
		return nil
	}

	totalArgs := []any{clubID}
	totalSeasonFilter := ""
	if seasonID != nil {
		totalSeasonFilter = ` AND t.season_id = $2`
		totalArgs = append(totalArgs, *seasonID)
	}
	var totalEntries int
	err = h.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM (
		    SELECT 1 FROM tournament_results r
		    JOIN published_tournaments t ON t.id = r.published_tournament_id
		    WHERE t.club_id = $1`+totalSeasonFilter+`
		    GROUP BY lower(r.first_name), lower(r.last_name)
		 ) sub`,
		totalArgs...,
	).Scan(&totalEntries)
	if err != nil {
		h.logger.Error("dashboard count total", "err", err)
		return nil
	}

	return &clubWidgetRank{
		Position:          betterCount + 1,
		TotalEntries:      totalEntries,
		TotalPoints:       myPoints,
		TournamentsPlayed: myTournaments,
	}
}

// fetchActiveSeason : retourne la saison `active` la plus récente du club
// (sinon nil) + le rang du joueur dans cette saison.
func (h *playersDashboardHandler) fetchActiveSeason(ctx context.Context, clubID,
	playerID, firstName, lastName string) *clubWidgetSeason {

	var s clubWidgetSeason
	err := h.pool.QueryRow(ctx,
		`SELECT id, slug, name, starts_at, ends_at
		 FROM seasons
		 WHERE club_id = $1 AND status = 'active'
		 ORDER BY starts_at DESC
		 LIMIT 1`,
		clubID,
	).Scan(&s.ID, &s.Slug, &s.Name, &s.StartsAt, &s.EndsAt)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("dashboard fetch active season", "err", err)
		}
		return nil
	}
	s.MyRank = h.computeRank(ctx, clubID, playerID, firstName, lastName, &s.ID)
	return &s
}

// fetchUpcoming : 3 prochains tournois `open` du club + statut d'inscription
// du joueur (pending/confirmed/rejected/cancelled ou nil si pas inscrit).
func (h *playersDashboardHandler) fetchUpcoming(ctx context.Context, clubID, playerID string) []clubWidgetUpcoming {
	rows, err := h.pool.Query(ctx,
		`SELECT t.id, t.name, t.start_at, t.audience, t.max_players,
		        COALESCE((SELECT COUNT(*) FROM tournament_registrations r
		                   WHERE r.published_tournament_id = t.id AND r.status = 'confirmed'), 0)
		      + COALESCE(jsonb_array_length(t.local_players), 0) AS total_players,
		        (SELECT status FROM tournament_registrations r
		          WHERE r.published_tournament_id = t.id AND r.player_id = $2
		          ORDER BY r.registered_at DESC LIMIT 1) AS my_status
		 FROM published_tournaments t
		 WHERE t.club_id = $1
		   AND t.status = 'open'
		   AND t.start_at > now()
		 ORDER BY t.start_at ASC
		 LIMIT 3`,
		clubID, playerID,
	)
	if err != nil {
		h.logger.Error("dashboard fetch upcoming", "err", err)
		return []clubWidgetUpcoming{}
	}
	defer rows.Close()

	out := make([]clubWidgetUpcoming, 0, 3)
	for rows.Next() {
		var u clubWidgetUpcoming
		var maxPlayers, totalPlayers int
		var myStatus *string
		if err := rows.Scan(&u.ID, &u.Name, &u.StartAt, &u.Audience, &maxPlayers, &totalPlayers, &myStatus); err != nil {
			h.logger.Error("dashboard scan upcoming", "err", err)
			continue
		}
		if maxPlayers > 0 {
			spots := maxPlayers - totalPlayers
			if spots < 0 {
				spots = 0
			}
			u.SpotsLeft = &spots
		}
		u.MyRegistrationStatus = myStatus
		out = append(out, u)
	}
	return out
}
