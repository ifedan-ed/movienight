// Command migrate brings the database schema up to date (internal/migrate):
// it takes over an Alembic database at revision 0016, or creates the schema
// in an empty one, then applies db/migrations in order. Run it as its own
// deploy step, before starting the new API and worker; it needs only
// DATABASE_URL.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"movienight/server/internal/config"
	"movienight/server/internal/logging"
	"movienight/server/internal/migrate"
	"movienight/server/internal/pg"
)

func main() {
	log := logging.New(os.Stdout, slog.LevelInfo)
	if err := run(log); err != nil {
		log.Error("migrate failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	url, err := config.DatabaseURL(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	done, err := migrate.Up(ctx, pool, log)
	if err != nil {
		return err
	}
	latest, _ := migrate.Latest()
	log.Info("the database is up to date", "applied", done, "version", latest)
	return nil
}
