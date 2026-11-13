// Package pg opens the Postgres connection pool every part of the server
// shares.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxConns is each process's ceiling on Postgres connections. The Python
// used 10 plus 10 overflow; with several API replicas plus the worker, 20 a
// process keeps the total well under Postgres's default max_connections
// (100) for up to four replicas. Past that, pgbouncer goes in front (the
// plan for scale) rather than raising this.
const MaxConns = 20

// Open connects and pings, so a wrong DATABASE_URL fails startup at once.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx's message can quote the URL, password and all: say only what.
		return nil, fmt.Errorf("DATABASE_URL is not a valid Postgres address")
	}
	cfg.MaxConns = MaxConns
	// Connections are recycled so a Postgres failover or a pgbouncer restart
	// is picked up without restarting the app.
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres did not answer: %w", err)
	}
	return pool, nil
}
