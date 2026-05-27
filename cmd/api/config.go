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
	// WorkerAdminURL est l'URL de base du Worker (sans path) utilisée pour
	// synchroniser les licenses créées en Postgres vers D1
	// (ex: "https://pokclock-license.dynamidoxa.workers.dev"). Optionnel : si
	// vide, la sync est désactivée et les licenses ops ne seront pas propagées.
	WorkerAdminURL string
	// WorkerAdminToken est le Bearer ADMIN_TOKEN du Worker. Lu en priorité
	// depuis WORKER_ADMIN_TOKEN_FILE (Swarm secret), sinon depuis l'env directe.
	WorkerAdminToken string
	// Resend (Phase 0.D-α.1.b) : emails transactionnels d'inscriptions online.
	// Optionnels — si manquants, les emails sont skipped silencieusement et
	// l'inscription se fait quand même (le cancel_token est renvoyé en JSON).
	ResendAPIKey  string // RESEND_API_KEY_FILE (Swarm) ou RESEND_API_KEY direct
	EmailFrom     string // ex: "PokClock <noreply@pokclock.com>", domaine vérifié dans Resend
	PublicSiteURL string // ex: "https://pokclock.com" — pour construire les liens d'annulation
	// SuperadminLicenseKeys est un mécanisme de bootstrap pour Phase 0.B :
	// tant que le Worker /verify n'expose pas encore `role` dans sa réponse,
	// on promeut explicitement certaines license keys en role=superadmin
	// dans le handler /api/auth/issue. À retirer une fois le Worker à jour.
	//
	// Format env : "POK-AAAA-BBBB-CCCC-DDDD,POK-EEEE-FFFF-GGGG-HHHH" (CSV)
	SuperadminLicenseKeys map[string]struct{}
}

func loadConfig() (Config, error) {
	cfg := Config{
		Port:              envOr("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		JWTPrivateKeyPath: os.Getenv("JWT_PRIVATE_KEY_PATH"),
		JWTPublicKeyPath:  os.Getenv("JWT_PUBLIC_KEY_PATH"),
		WorkerVerifyURL:   os.Getenv("WORKER_VERIFY_URL"),
		WorkerAdminURL:    os.Getenv("WORKER_ADMIN_URL"),
		LogLevel:          envOr("LOG_LEVEL", "info"),
	}

	// WORKER_ADMIN_TOKEN_FILE (Swarm secret) prioritaire, sinon env directe.
	if path := strings.TrimSpace(os.Getenv("WORKER_ADMIN_TOKEN_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read worker admin token file %s: %w", path, err)
		}
		cfg.WorkerAdminToken = strings.TrimSpace(string(b))
	} else {
		cfg.WorkerAdminToken = strings.TrimSpace(os.Getenv("WORKER_ADMIN_TOKEN"))
	}

	// Resend : RESEND_API_KEY_FILE (Swarm) prioritaire.
	if path := strings.TrimSpace(os.Getenv("RESEND_API_KEY_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read resend api key file %s: %w", path, err)
		}
		cfg.ResendAPIKey = strings.TrimSpace(string(b))
	} else {
		cfg.ResendAPIKey = strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	}
	cfg.EmailFrom = envOr("EMAIL_FROM", "PokClock <noreply@pokclock.com>")
	cfg.PublicSiteURL = strings.TrimRight(envOr("PUBLIC_SITE_URL", "https://pokclock.com"), "/")

	origins := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if s := strings.TrimSpace(o); s != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, s)
			}
		}
	}

	if sa := strings.TrimSpace(os.Getenv("SUPERADMIN_LICENSE_KEYS")); sa != "" {
		cfg.SuperadminLicenseKeys = make(map[string]struct{})
		for _, k := range strings.Split(sa, ",") {
			if s := strings.TrimSpace(k); s != "" {
				cfg.SuperadminLicenseKeys[s] = struct{}{}
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
