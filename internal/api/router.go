// Package api porte la couche HTTP : routes Echo, middleware, handlers.
package api

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// Deps regroupe les dépendances injectées dans les handlers.
type Deps struct {
	BuildSHA       string
	Pool           *pgxpool.Pool
	Signer         *auth.Signer
	WorkerClient   *auth.WorkerClient
	AllowedOrigins []string
	Logger         *slog.Logger
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
		signer:       d.Signer,
		workerClient: d.WorkerClient,
		logger:       d.Logger,
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
}

// slogMiddleware logge chaque requête : méthode, path, status, latence, request ID.
func slogMiddleware(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := echoNow()
			err := next(c)
			req := c.Request()
			res := c.Response()
			logger.Info("http",
				"method", req.Method,
				"path", req.URL.Path,
				"status", res.Status,
				"bytes", res.Size,
				"rid", res.Header().Get(echo.HeaderXRequestID),
				"dur_ms", echoSince(start).Milliseconds(),
			)
			return err
		}
	}
}
