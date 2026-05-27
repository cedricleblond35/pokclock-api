// Package api porte la couche HTTP : routes Echo, middleware, handlers.
package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/cedricleblond35/pokclock-api/internal/clients/resend"
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
	// Phase 0.D-α.1.b — emails transactionnels d'inscriptions online.
	ResendClient  *resend.Client
	PublicSiteURL string
	// APIBaseURL : URL absolue de l'API elle-même (ex "https://api.pokclock.com").
	// Utilisé pour construire les liens magic link dans les emails — l'API
	// répond au clic et redirige ensuite vers PublicSiteURL.
	APIBaseURL string
	// Phase 0.E.1 — domaine du cookie de session joueur (ex ".pokclock.com").
	// Vide en dev local pour cookie limité à l'origine.
	PlayerCookieDomain string
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
			AllowOrigins:     d.AllowedOrigins,
			AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
			AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAuthorization},
			AllowCredentials: true, // cookie pokclock_player envoyé cross-subdomain (0.E.1)
			MaxAge:           300,
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

	// /api/structures/* : routes club-scoped. Accessibles à toute license avec
	// un club_id dans les claims (member/admin/superadmin). Phase 0.C : le
	// filtre se fait automatiquement sur claims.ClubID, impossible de voir
	// les structures d'un autre club.
	structuresH := &structuresHandler{
		pool:   d.Pool,
		logger: d.Logger,
	}
	structuresGroup := e.Group("/api/structures")
	structuresGroup.Use(jwtAuthMiddleware(d.Signer))
	structuresGroup.Use(requireClubScope())
	structuresGroup.GET("", structuresH.list)
	structuresGroup.POST("", structuresH.create)
	structuresGroup.GET("/:slug", structuresH.get)
	structuresGroup.PATCH("/:slug", structuresH.update)
	// DELETE réservé à admin/superadmin pour limiter les suppressions
	// accidentelles par un dealer.
	deleteH := structuresGroup.Group("")
	deleteH.Use(requireAnyRole(auth.RoleAdmin, auth.RoleSuperadmin))
	deleteH.DELETE("/:slug", structuresH.delete)

	// /api/club/* : routes admin de club (gestion des licenses du club, audit,
	// infos club). Réservées à admin + superadmin du club, jamais cross-club.
	clubGroup := e.Group("/api/club")
	clubGroup.Use(jwtAuthMiddleware(d.Signer))
	clubGroup.Use(requireClubScope())
	clubGroup.Use(requireAnyRole(auth.RoleAdmin, auth.RoleSuperadmin))

	clubInfoH := &clubInfoHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.GET("/info", clubInfoH.get)
	clubGroup.PATCH("/online-registrations", clubInfoH.updateOnlineRegistrationsFlag)

	clubLicensesH := &clubLicensesHandler{
		pool:        d.Pool,
		logger:      d.Logger,
		workerAdmin: d.WorkerAdminClient,
	}
	clubGroup.GET("/licenses", clubLicensesH.list)
	clubGroup.POST("/licenses", clubLicensesH.create)
	clubGroup.POST("/licenses/:id/revoke", clubLicensesH.revoke)

	// Phase 0.C-γ partie 3 : CRUD members + assignation membre↔license.
	clubMembersH := &clubMembersHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.GET("/members", clubMembersH.list)
	clubGroup.POST("/members", clubMembersH.create)
	clubGroup.PATCH("/members/:id", clubMembersH.update)
	clubGroup.DELETE("/members/:id", clubMembersH.delete)
	// Assign membre à une license — chemin sur /licenses/:id/assign-member
	// pour rester cohérent avec la sémantique (ressource = license).
	clubGroup.POST("/licenses/:id/assign-member", clubMembersH.assignLicense)

	clubAuditH := &clubAuditHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.GET("/audit", clubAuditH.search)

	// Phase 0.D-γ.1 : barème de points par club.
	clubPointSchemesH := &clubPointSchemesHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.GET("/point-scheme", clubPointSchemesH.get)
	clubGroup.PUT("/point-scheme", clubPointSchemesH.upsert)
	clubGroup.DELETE("/point-scheme", clubPointSchemesH.delete)

	// Phase 0.D-α : tournois publiés + modération des inscriptions en ligne.
	clubTournamentsH := &clubTournamentsHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.GET("/tournaments", clubTournamentsH.list)
	clubGroup.POST("/tournaments", clubTournamentsH.publish)
	clubGroup.POST("/tournaments/:id/close", clubTournamentsH.setStatus("closed"))
	clubGroup.POST("/tournaments/:id/cancel", clubTournamentsH.setStatus("cancelled"))

	// Phase 0.E.4 : publication des résultats finaux d'un tournoi.
	clubResultsH := &clubTournamentResultsHandler{pool: d.Pool, logger: d.Logger}
	clubGroup.POST("/tournaments/:id/results", clubResultsH.publishResults)

	emailCtx := &emailContext{
		client:        d.ResendClient,
		publicSiteURL: d.PublicSiteURL,
		apiBaseURL:    d.APIBaseURL,
		logger:        d.Logger,
	}

	clubRegistrationsH := &clubRegistrationsHandler{
		pool:   d.Pool,
		logger: d.Logger,
		emails: emailCtx,
	}
	clubGroup.GET("/tournaments/:tid/registrations", clubRegistrationsH.list)
	clubGroup.POST("/registrations/:id/confirm", clubRegistrationsH.confirm)
	clubGroup.POST("/registrations/:id/reject", clubRegistrationsH.reject)

	// /public/* : endpoints anonymes consommés par le site pokclock.com/clubs/:slug.
	// AUCUNE auth — rate-limit géré en amont (Cloudflare/Traefik).
	publicGroup := e.Group("/public")
	publicTournamentsH := &publicTournamentsHandler{
		pool:   d.Pool,
		logger: d.Logger,
		emails: emailCtx,
	}
	publicGroup.GET("/clubs/:slug/tournaments", publicTournamentsH.listByClubSlug)
	publicGroup.GET("/clubs/:slug/tournaments/:id", publicTournamentsH.getByClubSlug)
	publicGroup.POST("/clubs/:slug/tournaments/:id/register", publicTournamentsH.register)
	publicGroup.GET("/registrations/:token/cancel", publicTournamentsH.cancelByToken)

	// Phase 0.E.4 : résultats de tournoi + leaderboard club.
	publicResultsH := &publicResultsHandler{pool: d.Pool, logger: d.Logger}
	publicGroup.GET("/clubs/:slug/tournaments/:id/results", publicResultsH.getResults)
	publicGroup.GET("/clubs/:slug/leaderboard", publicResultsH.getLeaderboard)

	// /api/players/* : comptes joueurs (Phase 0.E.1).
	playersAuthH := &playersAuthHandler{
		pool:          d.Pool,
		signer:        d.Signer,
		emails:        emailCtx,
		logger:        d.Logger,
		publicSiteURL: d.PublicSiteURL,
		cookieDomain:  d.PlayerCookieDomain,
	}
	playersAuthGroup := e.Group("/api/players/auth")
	// Rate-limit dur sur les endpoints magic link : 10 req/min par IP pour
	// limiter le spam d'emails.
	playersAuthGroup.Use(middleware.RateLimiter(middleware.NewRateLimiterMemoryStore(10)))
	playersAuthGroup.POST("/magic-link", playersAuthH.requestMagicLink)
	playersAuthGroup.GET("/magic-link/verify", playersAuthH.verifyMagicLink)
	playersAuthGroup.POST("/logout", playersAuthH.logout)

	playersMeH := &playersMeHandler{
		pool:         d.Pool,
		logger:       d.Logger,
		emails:       emailCtx,
		cookieDomain: d.PlayerCookieDomain,
	}
	playersMeGroup := e.Group("/api/players/me")
	playersMeGroup.Use(playerAuthMiddleware(d.Signer))
	playersMeGroup.GET("", playersMeH.getMe)
	playersMeGroup.PATCH("", playersMeH.updateMe)
	playersMeGroup.DELETE("", playersMeH.deleteMe)

	// Phase 0.E.2 : inscription / cancel / liste via compte joueur.
	playersRegH := &playersRegistrationsHandler{pool: d.Pool, logger: d.Logger, emails: emailCtx}
	playersMeGroup.POST("/clubs/:slug/tournaments/:id/register", playersRegH.register)
	playersMeGroup.DELETE("/registrations/:id", playersRegH.cancelMyRegistration)
	playersMeGroup.GET("/registrations", playersRegH.listMyRegistrations)

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
	// Phase 0.C-γ partie 3e : visibilité super-admin des membres d'un club.
	adminGroup.GET("/clubs/:id/members", clubsH.listMembers)

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

