// Package migrate brings the database schema up to date. It runs as its own
// step of a deploy (cmd/migrate), never when the API starts: with several
// API replicas starting at once, each would race to change the schema, and a
// failed migration would crash-loop every replica instead of stopping one
// deploy step. The API only checks (Check) that the schema is the one it was
// built for, and refuses to start otherwise.
//
// How versions are kept: the table schema_migrations has one row per
// applied migration (its four-digit number). The Go server takes over from
// Alembic at revision 0016 (db.BaselineVersion):
//
//   - a database Alembic left at 0016 gets the row "0016" without any change;
//   - an empty database gets the baseline schema (db/schema.sql) and "0016";
//   - a database at any other Alembic revision is refused: bring it to 0016
//     with the Python app first (its migrations/ stay the history up to
//     there);
//
// then every db/migrations/NNNN_*.sql above the highest recorded number is
// applied in order, each in its own transaction together with its row, so a
// migration is either wholly applied and recorded or not at all.
//
// A Postgres advisory lock is held throughout, so two migrate runs at once
// (two deploys, a retry) queue instead of interleaving.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/db"
)

// lockKey is this app's advisory lock id ("mnmigr" in ASCII), fixed so every
// migrate run, on any machine, takes the same lock.
const lockKey int64 = 0x6d6e6d69677200

// Migration is one numbered SQL file.
type Migration struct {
	Version string // "0017"
	Name    string // "0017_web_sessions.sql"
	SQL     string
}

var fileRe = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Load reads the embedded migrations in order. A badly named file is an
// error (a typo would otherwise be skipped silently).
func Load() ([]Migration, error) {
	entries, err := fs.ReadDir(db.Migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[string]string{}
	for _, e := range entries {
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q is not named NNNN_name.sql", e.Name())
		}
		if m[1] <= db.BaselineVersion {
			return nil, fmt.Errorf("migration %q is not after the baseline %s", e.Name(), db.BaselineVersion)
		}
		if prev, dup := seen[m[1]]; dup {
			return nil, fmt.Errorf("migrations %q and %q share a number", prev, e.Name())
		}
		seen[m[1]] = e.Name()
		body, err := fs.ReadFile(db.Migrations, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: m[1], Name: e.Name(), SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Latest is the version the code expects the database to be at.
func Latest() (string, error) {
	ms, err := Load()
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return db.BaselineVersion, nil
	}
	return ms[len(ms)-1].Version, nil
}

// Up applies everything missing. It returns the versions it applied.
func Up(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) ([]string, error) {
	migrations, err := Load()
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return nil, fmt.Errorf("taking the migration lock: %w", err)
	}
	defer func() {
		// A fresh context: the caller's may be cancelled, and the lock must
		// still be released (it would also go with the connection).
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamp with time zone NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn.Conn())
	if err != nil {
		return nil, err
	}
	var done []string
	if len(applied) == 0 {
		if err := baseline(ctx, conn.Conn(), log); err != nil {
			return nil, err
		}
		applied[db.BaselineVersion] = true
		done = append(done, db.BaselineVersion)
	}
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		log.InfoContext(ctx, "applying migration", "migration", m.Name)
		err := pgx.BeginFunc(ctx, conn.Conn(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.Version)
			return err
		})
		if err != nil {
			return done, fmt.Errorf("migration %s failed (nothing of it was kept): %w", m.Name, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// baseline takes over a database at Alembic 0016, or creates the 0016 schema
// in an empty one.
func baseline(ctx context.Context, conn *pgx.Conn, log *slog.Logger) error {
	var alembic *string
	err := conn.QueryRow(ctx, `SELECT to_regclass('public.alembic_version')::text`).Scan(&alembic)
	if err != nil {
		return err
	}
	if alembic != nil {
		var version string
		if err := conn.QueryRow(ctx, "SELECT version_num FROM alembic_version").Scan(&version); err != nil {
			return fmt.Errorf("reading alembic_version: %w", err)
		}
		if version != db.BaselineVersion {
			return fmt.Errorf("the database is at Alembic revision %s; the Go server takes over at %s. "+
				"Bring it to %s with the Python app's migrations first", version, db.BaselineVersion, db.BaselineVersion)
		}
		log.InfoContext(ctx, "taking over the database from Alembic", "revision", version)
		_, err := conn.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", db.BaselineVersion)
		return err
	}
	var users *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.users')::text`).Scan(&users); err != nil {
		return err
	}
	if users != nil {
		return errors.New("the database has Movie Night's tables but no alembic_version or schema_migrations; " +
			"refusing to guess its schema version")
	}
	log.InfoContext(ctx, "creating the schema in an empty database", "baseline", db.BaselineVersion)
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, db.Baseline); err != nil {
			return fmt.Errorf("creating the baseline schema: %w", err)
		}
		_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", db.BaselineVersion)
		return err
	})
}

func appliedVersions(ctx context.Context, conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(versions))
	for _, v := range versions {
		out[v] = true
	}
	return out, nil
}

// Check is the API's startup check: the database must be at exactly the
// version this build expects. Older: run migrate first. Newer: this build is
// older than the schema (a rollback after a migration), which may misread
// it, so it refuses too.
func Check(ctx context.Context, pool *pgxpool.Pool) error {
	want, err := Latest()
	if err != nil {
		return err
	}
	var have *string
	err = pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&have)
	if err != nil || have == nil {
		return fmt.Errorf("the database has not been migrated for the Go server (run: migrate); expected version %s", want)
	}
	if *have != want {
		return fmt.Errorf("the database is at schema version %s but this build expects %s (run: migrate, or deploy the matching build)", *have, want)
	}
	return nil
}
