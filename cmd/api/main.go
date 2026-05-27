// Package main est l'entry point du backend pokclock-api.
//
// Charge la config depuis l'environnement, monte les middlewares Echo, initialise
// le pool Postgres, expose les endpoints HTTP, et gère l'arrêt propre via SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	apihttp "github.com/cedricleblond35/pokclock-api/internal/api"
	"github.com/cedricleblond35/pokclock-api/internal/clients/resend"
	"github.com/cedricleblond35/pokclock-api/internal/clients/workeradmin"
	"github.com/cedricleblond35/pokclock-api/internal/db"
	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// buildSHA est injecté au build via -ldflags="-X main.buildSHA=<sha>".
// Défaut "dev" pour les builds locaux.
var buildSHA = "dev"

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(2)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info("starting pokclock-api", "build", buildSHA, "port", cfg.Port)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.RunMigrations(ctx, pool); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}

	signer, err := auth.NewSigner(cfg.JWTPrivateKeyPath, cfg.JWTPublicKeyPath)
	if err != nil {
		logger.Error("jwt signer init failed", "err", err)
		os.Exit(1)
	}

	workerClient := auth.NewWorkerClient(cfg.WorkerVerifyURL, http.DefaultClient)

	workerAdminClient := workeradmin.New(cfg.WorkerAdminURL, cfg.WorkerAdminToken, nil)
	if !workerAdminClient.IsConfigured() {
		logger.Warn("worker admin sync disabled — WORKER_ADMIN_URL or WORKER_ADMIN_TOKEN missing",
			"url_set", cfg.WorkerAdminURL != "")
	} else {
		logger.Info("worker admin sync enabled", "url", cfg.WorkerAdminURL)
	}

	resendClient := resend.New(cfg.ResendAPIKey, cfg.EmailFrom, nil)
	if !resendClient.IsConfigured() {
		logger.Warn("resend emails disabled — RESEND_API_KEY or EMAIL_FROM missing",
			"from_set", cfg.EmailFrom != "")
	} else {
		logger.Info("resend emails enabled", "from", cfg.EmailFrom, "site", cfg.PublicSiteURL)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	apihttp.Mount(e, apihttp.Deps{
		BuildSHA:              buildSHA,
		Pool:                  pool,
		Signer:                signer,
		WorkerClient:          workerClient,
		WorkerAdminClient:     workerAdminClient,
		ResendClient:          resendClient,
		PublicSiteURL:         cfg.PublicSiteURL,
		AllowedOrigins:        cfg.AllowedOrigins,
		Logger:                logger,
		SuperadminLicenseKeys: cfg.SuperadminLicenseKeys,
	})

	if n := len(cfg.SuperadminLicenseKeys); n > 0 {
		logger.Info("bootstrap superadmin keys configured", "count", n)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           e,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server crashed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("bye")
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
