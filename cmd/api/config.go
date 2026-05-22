package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config rassemble la configuration runtime de l'API.
//
// Tous les champs sont lus depuis l'environnement au démarrage. Pas de fichier
// de config sur disque, par design : la conf vit dans Docker Swarm (variables
// + secrets montés en fichiers).
type Config struct {
	Port              string
	DatabaseURL       string
	JWTPrivateKeyPath string
	JWTPublicKeyPath  string
	WorkerVerifyURL   string
	AllowedOrigins    []string
	LogLevel          string
}

func loadConfig() (Config, error) {
	cfg := Config{
		Port:              envOr("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		JWTPrivateKeyPath: os.Getenv("JWT_PRIVATE_KEY_PATH"),
		JWTPublicKeyPath:  os.Getenv("JWT_PUBLIC_KEY_PATH"),
		WorkerVerifyURL:   os.Getenv("WORKER_VERIFY_URL"),
		LogLevel:          envOr("LOG_LEVEL", "info"),
	}

	origins := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if s := strings.TrimSpace(o); s != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, s)
			}
		}
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.JWTPrivateKeyPath == "" {
		missing = append(missing, "JWT_PRIVATE_KEY_PATH")
	}
	if cfg.JWTPublicKeyPath == "" {
		missing = append(missing, "JWT_PUBLIC_KEY_PATH")
	}
	if cfg.WorkerVerifyURL == "" {
		missing = append(missing, "WORKER_VERIFY_URL")
	}
	if len(missing) > 0 {
		return Config{}, errors.New("missing required env vars: " + strings.Join(missing, ", "))
	}

	if _, err := os.Stat(cfg.JWTPrivateKeyPath); err != nil {
		return Config{}, fmt.Errorf("jwt private key not readable at %s: %w", cfg.JWTPrivateKeyPath, err)
	}
	if _, err := os.Stat(cfg.JWTPublicKeyPath); err != nil {
		return Config{}, fmt.Errorf("jwt public key not readable at %s: %w", cfg.JWTPublicKeyPath, err)
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
