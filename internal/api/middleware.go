package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

const ctxClaimsKey = "auth.claims"

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

// requireAnyRole accepte plusieurs rôles (OU logique). Utilisé pour les
// endpoints accessibles à la fois aux admins de club et au superadmin.
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
				return c.JSON(http.StatusForbidden, map[string]string{"error": "insufficient_role"})
			}
			return next(c)
		}
	}
}
