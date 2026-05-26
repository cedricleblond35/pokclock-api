// Package api porte la couche HTTP : routes Echo, middleware, handlers.
package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/cedricleblond35/pokclock-api/internal/clients/workeradmin"
	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// Deps regroupe les dépendances injectées dans les handlers.
type Deps struct {
	BuildSHA          string
	Pool              *pgxpool.Pool
	Signer            *auth.Signer
	WorkerClient      *auth.WorkerClient
	WorkerAdminClient *workeradmin.Client
	AllowedOrigins    []string
	Logger            *slog.Logger
	// SuperadminLicenseKeys : bootstrap mechanism. Si la licence du caller est
	// dans ce set, le handler /api/auth/issue émet le JWT avec role=superadmin
	// même si le Worker /verify n'a pas (encore) propagé ce rôle. Vide en local
	// dev / dans les tests par défaut.
	SuperadminLicenseKeys map[string]struct{}
}

// Mount monte toutes les routes et middlewares sur l'instance Echo passée.
func Mount(e *echo.Echo, d Deps) {
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(slogMiddleware(d.Logger))

	if len(d.AllowedOrigins) > 0 {
		e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
			AllowOrigins: d.AllowedOrigins,
			AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
			AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAuthorization},
			MaxAge:       300,
		}))
	}

	healthH := &healthHandler{buildSHA: d.BuildSHA}
	e.GET("/api/health", healthH.handle)

	// JWKS publique, pas d'auth.
	e.GET("/.well-known/jwks.json", func(c echo.Context) error {
		return c.JSON(http.StatusOK, d.Signer.JWKS())
	})

	authH := &authHandler{
		signer:                d.Signer,
		workerClient:          d.WorkerClient,
		logger:                d.Logger,
		superadminLicenseKeys: d.SuperadminLicenseKeys,
	}
	authGroup := e.Group("/api/auth")
	// Rate-limit dur sur les endpoints auth : 10 req/min par IP.
	authGroup.Use(middleware.RateLimiter(middleware.NewRateLimiterMemoryStore(10)))
	authGroup.POST("/issue", authH.issue)
	authGroup.POST("/refresh", authH.refresh)

	structuresH := &structuresHandler{
		pool:   d.Pool,
		logger: d.Logger,
	}
	structuresGroup := e.Group("/api/structures")
	structuresGroup.Use(jwtAuthMiddleware(d.Signer))
	structuresGroup.POST("", structuresH.create)
	structuresGroup.GET("/:slug", structuresH.get)
	structuresGroup.PATCH("/:slug", structuresH.update)

	// /api/admin/* : routes super-admin (cross-clubs), réservées à Cédric.
	// Double protection : JWT valide + claim role=superadmin.
	adminGroup := e.Group("/api/admin")
	adminGroup.Use(jwtAuthMiddleware(d.Signer))
	adminGroup.Use(requireRole(auth.RoleSuperadmin))

	clubsH := &adminClubsHandler{pool: d.Pool, logger: d.Logger}
	adminGroup.GET("/clubs", clubsH.list)
	adminGroup.POST("/clubs", clubsH.create)
	adminGroup.GET("/clubs/:id", clubsH.get)
	adminGroup.PATCH("/clubs/:id", clubsH.update)
	adminGroup.POST("/clubs/:id/suspend", clubsH.suspend)
	adminGroup.POST("/clubs/:id/unsuspend", clubsH.unsuspend)
	adminGroup.DELETE("/clubs/:id", clubsH.softDelete)

	licensesH := &adminLicensesHandler{
		pool:        d.Pool,
		logger:      d.Logger,
		workerAdmin: d.WorkerAdminClient,
	}
	adminGroup.GET("/licenses", licensesH.list)
	adminGroup.POST("/licenses", licensesH.create)
	adminGroup.POST("/licenses/:id/revoke", licensesH.revoke)

	auditH := &adminAuditHandler{pool: d.Pool, logger: d.Logger}
	adminGroup.GET("/audit", auditH.search)
}

// slogMiddleware logge chaque requête : méthode, path, status, latence, request ID.
//
// Le middleware tourne AVANT l'HTTPErrorHandler d'Echo, donc res.Status reste
// à 200 (valeur par défaut) tant qu'une erreur n'a pas été écrite. On dérive
// alors le code depuis l'*echo.HTTPError retourné par next() pour que les
// 404/4xx/5xx apparaissent correctement dans les logs.
func slogMiddleware(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := echoNow()
			err := next(c)
			req := c.Request()
			res := c.Response()

			status := res.Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				} else if status == http.StatusOK {
					status = http.StatusInternalServerError
				}
			}

			logger.Info("http",
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"bytes", res.Size,
				"rid", res.Header().Get(echo.HeaderXRequestID),
				"dur_ms", echoSince(start).Milliseconds(),
			)
			return err
		}
	}
}

