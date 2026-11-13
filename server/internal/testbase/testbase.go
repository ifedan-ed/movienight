// Package testbase is what every database test shares, without the API:
// the throwaway Postgres (pgvector/pgvector:pg17, migrated by
// internal/migrate) and Valkey that scripts/test.sh starts, emptied for each
// test (Conns), and a complete, valid test install's settings (BaseEnv).
// Without TEST_DATABASE_URL and TEST_VALKEY_URL a test that needs them
// fails and says so (it does not skip: a skipped test reads as a passing
// one).
//
// It is apart from package testenv (which builds and serves the whole API)
// so a service the API is built from can test itself against the real
// database without an import cycle through the server.
package testbase

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/config"
	"movienight/server/internal/kv"
	"movienight/server/internal/migrate"
	"movienight/server/internal/pg"
)

var (
	once    sync.Once
	pool    *pgxpool.Pool
	valkeyC valkey.Client
	openErr error
)

// Conns returns the shared, migrated test database and Valkey, both emptied.
func Conns(t testing.TB) (*pgxpool.Pool, valkey.Client) {
	t.Helper()
	once.Do(func() {
		dbURL, kvURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_VALKEY_URL")
		if dbURL == "" || kvURL == "" {
			openErr = errString("TEST_DATABASE_URL and TEST_VALKEY_URL are not set: run the tests with scripts/test.sh")
			return
		}
		ctx := context.Background()
		if pool, openErr = pg.Open(ctx, dbURL); openErr != nil {
			return
		}
		if _, openErr = migrate.Up(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); openErr != nil {
			return
		}
		valkeyC, openErr = kv.Open(ctx, kvURL)
	})
	if openErr != nil {
		t.Fatal(openErr)
	}
	Reset(t)
	return pool, valkeyC
}

type errString string

func (e errString) Error() string { return string(e) }

// Reset empties every table (but the migration record) and Valkey.
func Reset(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, `"`+name+`"`)
	}
	rows.Close()
	if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err := valkeyC.Do(ctx, valkeyC.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
}

// RepoRoot finds the repository root (the directory holding web/index.html).
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "web", "index.html")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (web/index.html) not found above the test")
		}
		dir = parent
	}
}

// Env is the settings a test install uses; Set changes one before Config.
type Env map[string]string

// BaseEnv is a complete, valid test install (fake upstream addresses that
// nothing in phase 1 calls).
func BaseEnv(t testing.TB) Env {
	return Env{
		"DATABASE_URL": os.Getenv("TEST_DATABASE_URL"), "VALKEY_URL": os.Getenv("TEST_VALKEY_URL"),
		// http, not https: Go's cookie jar (rightly) never sends a Secure
		// cookie to the plain-http test server.
		"SECRET_KEY": "test-secret", "PUBLIC_URL": "http://mn.test", "TRUSTED_PROXY": "none",
		"WEB_DIR":        filepath.Join(RepoRoot(t), "web"),
		"OWNER_USERNAME": "owner", "USER_ALIASES": "owner-alt=owner", "AUTH_DEV_ALLOWED": "true",
		"JELLYFIN_BASE_URL": "http://jellyfin.test", "JELLYFIN_PUBLIC_URL": "https://jf.test",
		"JELLYFIN_API_KEY": "test", "JELLYFIN_USER_ID": strings.Repeat("c", 32), "JELLYFIN_WEBHOOK_SECRET": "hook-secret",
		"TMDB_API_KEY": "test", "MEILI_BASE_URL": "http://meili.test", "TIMEZONE": "America/New_York",
	}
}

// Config builds the settings from env, failing the test on any problem.
func (e Env) Config(t testing.TB) *config.Config {
	t.Helper()
	cfg, err := config.LoadFrom(func(k string) (string, bool) { v, ok := e[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
