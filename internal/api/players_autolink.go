package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// autoLinkPlayerToClubs : Phase 0.H. À l'inscription d'un nouveau joueur
// (magic link verify ou Google OAuth callback), on cherche si son email
// correspond à un joueur local pré-existant dans la base WPF d'un ou
// plusieurs clubs (via le snapshot local_players des tournois publiés).
//
// Privacy : l'email lui-même n'est jamais transmis par l'app WPF. Le club
// pousse uniquement le SHA-256 de l'email normalisé (lower + trim) dans le
// champ emailHash du LocalPlayerRecord JSONB. On compare hash contre hash.
//
// Pour chaque club matched, on crée une row player_club_memberships en
// status='pending'. L'admin du club voit la demande dans son onglet WPF
// MEMBRES JOUEURS et l'approuve d'un clic.
//
// Fire-and-forget : l'échec ne bloque pas l'auth.
func autoLinkPlayerToClubs(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, playerID, email string) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || playerID == "" {
		return
	}
	sum := sha256.Sum256([]byte(email))
	hashHex := hex.EncodeToString(sum[:])

	rows, err := pool.Query(ctx,
		`SELECT DISTINCT t.club_id
		 FROM published_tournaments t,
		      jsonb_array_elements(t.local_players) p
		 WHERE p->>'emailHash' = $1
		   AND t.status <> 'cancelled'`,
		hashHex,
	)
	if err != nil {
		logger.Warn("auto-link query failed", "err", err, "player_id", playerID)
		return
	}
	defer rows.Close()

	var clubIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		clubIDs = append(clubIDs, id)
	}
	if err := rows.Err(); err != nil {
		logger.Warn("auto-link iterate failed", "err", err)
	}

	for _, clubID := range clubIDs {
		_, err := pool.Exec(ctx,
			`INSERT INTO player_club_memberships (player_id, club_id, status)
			 VALUES ($1, $2, 'pending')
			 ON CONFLICT (player_id, club_id) DO NOTHING`,
			playerID, clubID,
		)
		if err != nil {
			logger.Warn("auto-link insert membership failed", "err", err, "player_id", playerID, "club_id", clubID)
		}
	}
	if len(clubIDs) > 0 {
		logger.Info("player auto-linked to clubs", "player_id", playerID, "club_count", len(clubIDs))
	}
}
