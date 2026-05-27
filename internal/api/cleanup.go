package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StartMagicLinkCleanup lance une goroutine qui purge périodiquement les
// magic links morts (used_at NOT NULL ou expires_at < now()). Évite que la
// table player_magic_links grossisse indéfiniment au fil des logins.
//
// Intervalle : 6 heures (les tokens vivent 15 min, donc un nettoyage 6h est
// largement suffisant). Loggue le nombre de rows supprimées à chaque run pour
// observer la volumétrie.
//
// La goroutine s'arrête quand le contexte ctx est cancelled (shutdown propre).
func StartMagicLinkCleanup(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	const interval = 6 * time.Hour

	// Premier run immédiat pour purger ce qui s'est accumulé depuis le dernier
	// boot. La migration 019 a déjà fait le ménage au démarrage si elle vient
	// juste d'être appliquée, mais ce premier run sert pour les reboots futurs.
	go func() {
		runMagicLinkCleanup(ctx, pool, logger)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runMagicLinkCleanup(ctx, pool, logger)
			}
		}
	}()
}

func runMagicLinkCleanup(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	// Timeout court : le DELETE doit être quasi-instantané, si ça traîne c'est
	// que quelque chose de grave bloque la table.
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tag, err := pool.Exec(cctx,
		`DELETE FROM player_magic_links
		  WHERE used_at IS NOT NULL
		     OR expires_at < now()`)
	if err != nil {
		logger.Warn("magic link cleanup failed", "err", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		logger.Info("magic link cleanup", "rows_deleted", n)
	}
}
