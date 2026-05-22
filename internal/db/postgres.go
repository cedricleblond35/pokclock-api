// Package db isole la couche Postgres : pool de connexion + lancement des
// migrations au démarrage. Les requêtes métier vivent dans internal/domain/*.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// New construit un pool de connexions Postgres. Connexion testée par un ping
// avant de retourner.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, errors.New("empty database url")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pgx config: %w", err)
	}

	// Limites raisonnables pour 2 replicas d'API + 1 service de backup.
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

// RunMigrations applique les migrations embarquées dans le binaire au démarrage.
//
// goose gère les versions via une table goose_db_version créée automatiquement.
// On ouvre un *sql.DB via le driver stdlib pgx pour la durée des migrations,
// puis on le ferme.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	connConfig := pool.Config().ConnConfig
	sqlDB := stdlib.OpenDB(*connConfig)
	defer sqlDB.Close()

	migCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := goose.UpContext(migCtx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// ensureSQL prevents unused import warning in case goose helpers change.
var _ = (*sql.DB)(nil)
