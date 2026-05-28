package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// clubRosterHandler : Phase 0.J. Endpoint admin pour pousser le roster
// complet des membres permanents (Player.MembershipType=Member dans WPF)
// vers le cloud, independamment de la publication d'un tournoi.
//
// Permet l'auto-link Phase 0.H des nouveaux membres avant qu'aucun tournoi
// ne soit publie (cas du debut de saison ou onboarding d'un nouvel adherent).
//
// L'app WPF appelle ce endpoint a chaque save d'un Member (idempotent : on
// remplace toute la colonne `clubs.member_roster` d'un seul UPDATE).
type clubRosterHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

type rosterMember struct {
	FirstName string  `json:"firstName"`
	LastName  string  `json:"lastName"`
	Nickname  *string `json:"nickname,omitempty"`
	EmailHash *string `json:"emailHash,omitempty"`
}

type rosterRequest struct {
	Members []rosterMember `json:"members"`
}

// upsertRoster : PUT /api/club/roster
// Remplace integralement le roster des membres du club du caller.
// Idempotent. Les entries sans emailHash sont conservees (utile pour les
// membres sans email, pas auto-linkables mais comptabilises).
func (h *clubRosterHandler) upsertRoster(c echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil || claims.ClubID == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
	}

	var req rosterRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}

	// Normalise : trim whitespace, lowercase emailHash (deja en lower cote WPF
	// mais defensive). Garde uniquement les entries avec au minimum un nom.
	cleaned := make([]rosterMember, 0, len(req.Members))
	for _, m := range req.Members {
		fn := strings.TrimSpace(m.FirstName)
		ln := strings.TrimSpace(m.LastName)
		if fn == "" && ln == "" {
			continue
		}
		entry := rosterMember{FirstName: fn, LastName: ln}
		if m.Nickname != nil {
			nn := strings.TrimSpace(*m.Nickname)
			if nn != "" {
				entry.Nickname = &nn
			}
		}
		if m.EmailHash != nil {
			eh := strings.ToLower(strings.TrimSpace(*m.EmailHash))
			if eh != "" {
				entry.EmailHash = &eh
			}
		}
		cleaned = append(cleaned, entry)
	}

	rosterJSON, err := json.Marshal(cleaned)
	if err != nil {
		h.logger.Error("roster marshal", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encode"})
	}

	_, err = h.pool.Exec(c.Request().Context(),
		`UPDATE clubs SET member_roster = $1, updated_at = now() WHERE id = $2`,
		rosterJSON, claims.ClubID,
	)
	if err != nil {
		h.logger.Error("roster update", "err", err, "club_id", claims.ClubID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db_write"})
	}

	return c.JSON(http.StatusOK, map[string]any{"count": len(cleaned)})
}
