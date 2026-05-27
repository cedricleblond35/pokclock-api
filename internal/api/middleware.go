package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

const (
	ctxClaimsKey       = "auth.claims"
	ctxPlayerClaimsKey = "auth.player_claims"
)

// playerAuthMiddleware vérifie un JWT RS256 dans le cookie pokclock_player.
// Pour les routes /api/players/* qui ne sont pas dans le sous-groupe d'auth
// (où le cookie n'est pas encore présent). À utiliser APRÈS le middleware
// de récupération du cookie.
func playerAuthMiddleware(signer *auth.Signer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie(playerCookieName)
			if err != nil || cookie == nil || cookie.Value == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
			}
			claims, err := signer.VerifyPlayer(cookie.Value)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
			}
			c.Set(ctxPlayerClaimsKey, claims)
			return next(c)
		}
	}
}

// PlayerClaimsFromContext récupère les claims joueur. Retourne nil si non
// authentifié comme joueur.
func PlayerClaimsFromContext(c echo.Context) *auth.PlayerClaims {
	v, ok := c.Get(ctxPlayerClaimsKey).(*auth.PlayerClaims)
	if !ok {
		return nil
	}
	return v
}

// jwtAuthMiddleware vérifie un JWT RS256 dans le header Authorization: Bearer.
// Les claims sont stockés dans le contexte Echo sous ctxClaimsKey.
func jwtAuthMiddleware(signer *auth.Signer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get(echo.HeaderAuthorization)
			if authHeader == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing_token"})
			}
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "malformed_authorization"})
			}
			claims, err := signer.Verify(parts[1])
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
			}
			c.Set(ctxClaimsKey, claims)
			return next(c)
		}
	}
}

// ClaimsFromContext récupère les claims JWT injectés par le middleware.
// Retourne nil si la requête n'est pas authentifiée.
func ClaimsFromContext(c echo.Context) *auth.Claims {
	v, ok := c.Get(ctxClaimsKey).(*auth.Claims)
	if !ok {
		return nil
	}
	return v
}

// requireRole rejette les requêtes dont les claims JWT n'ont pas exactement
// le rôle attendu. À utiliser APRÈS jwtAuthMiddleware dans la chaîne — le
// middleware suppose que les claims sont déjà en contexte.
//
// Renvoie 401 si le contexte n'a pas de claims (middleware mal câblé),
// 403 si les claims existent mais que le rôle ne correspond pas.
func requireRole(role auth.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := ClaimsFromContext(c)
			if claims == nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
			}
			if claims.Role != role {
				return c.JSON(http.StatusForbidden, map[string]string{
					"error":    "insufficient_role",
					"required": string(role),
				})
			}
			return next(c)
		}
	}
}

// requireAnyRole accepte les requêtes dont les claims JWT portent l'un des
// rôles passés. Utilisé pour les routes /api/club/* qui doivent être ouvertes
// à admin ET superadmin (un super-admin peut agir au nom de n'importe quel
// club via ses propres claims, voir requireClubScope).
func requireAnyRole(roles ...auth.Role) echo.MiddlewareFunc {
	allowed := make(map[auth.Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := ClaimsFromContext(c)
			if claims == nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
			}
			if _, ok := allowed[claims.Role]; !ok {
				accepted := make([]string, 0, len(roles))
				for _, r := range roles {
					accepted = append(accepted, string(r))
				}
				return c.JSON(http.StatusForbidden, map[string]string{
					"error":    "insufficient_role",
					"accepted": strings.Join(accepted, ","),
				})
			}
			return next(c)
		}
	}
}

// requireClubScope garantit que les claims contiennent un ClubID non vide.
// Pour les routes /api/club/* : sans club_id dans le JWT, impossible de
// scope les requêtes — on rejette.
//
// Le super-admin global (clubId vide en pratique) doit utiliser /api/admin/*
// avec un clubId explicite en query/body, pas /api/club/*.
func requireClubScope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := ClaimsFromContext(c)
			if claims == nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "not_authenticated"})
			}
			if strings.TrimSpace(claims.ClubID) == "" {
				return c.JSON(http.StatusForbidden, map[string]string{
					"error":  "missing_club_scope",
					"detail": "Cette license n'est rattachée à aucun club. Voir avec le super-admin.",
				})
			}
			return next(c)
		}
	}
}
