package migrate

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/db"
	"movienight/server/internal/pg"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// freshDB makes an empty database of its own for one scenario.
func freshDB(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Fatal("TEST_DATABASE_URL is not set: run the tests with server/scripts/test.sh")
	}
	ctx := context.Background()
	admin, err := pg.Open(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	p, err := pg.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func versions(t *testing.T, p *pgxpool.Pool) string {
	rows, _ := p.Query(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

func TestAnEmptyDatabaseGetsTheBaselineAndEveryMigration(t *testing.T) {
	p := freshDB(t, "mig_empty")
	ctx := context.Background()
	if err := Check(ctx, p); err == nil {
		t.Fatal("an unmigrated database passed the check")
	}
	if _, err := Up(ctx, p, quiet); err != nil {
		t.Fatal(err)
	}
	// The baseline, then every migration in order (not only the latest:
	// each later phase adds its own).
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := "0016"
	for _, m := range all {
		want += "," + m.Version
	}
	if got := versions(t, p); got != want {
		t.Fatalf("applied %s, want %s", got, want)
	}
	if err := Check(ctx, p); err != nil {
		t.Fatal(err)
	}
	// Twice is a no-op.
	if done, err := Up(ctx, p, quiet); err != nil || len(done) != 0 {
		t.Fatal(done, err)
	}
}

func TestAnAlembicDatabaseAt0016IsTakenOverInPlace(t *testing.T) {
	p := freshDB(t, "mig_alembic")
	ctx := context.Background()
	// What the Python app leaves: the 0016 schema, alembic_version, data.
	if _, err := p.Exec(ctx, db.Baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `CREATE TABLE alembic_version (version_num varchar(32) PRIMARY KEY);
		INSERT INTO alembic_version VALUES ('0016');
		INSERT INTO users (username, is_admin, created_at) VALUES ('kept', false, now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := Up(ctx, p, quiet); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM users WHERE username = 'kept'").Scan(&n)
	if n != 1 {
		t.Fatal("the data did not survive")
	}
	if err := Check(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func TestOtherAlembicRevisionsAndUnknownSchemasAreRefused(t *testing.T) {
	ctx := context.Background()
	p := freshDB(t, "mig_old")
	_, _ = p.Exec(ctx, `CREATE TABLE alembic_version (version_num varchar(32) PRIMARY KEY); INSERT INTO alembic_version VALUES ('0015');`)
	if _, err := Up(ctx, p, quiet); err == nil || !strings.Contains(err.Error(), "0015") {
		t.Fatal(err)
	}
	q := freshDB(t, "mig_unknown")
	_, _ = q.Exec(ctx, `CREATE TABLE users (id int)`)
	if _, err := Up(ctx, q, quiet); err == nil {
		t.Fatal("a schema of unknown version was taken over")
	}
}

func TestMigrationFilesAreWellFormed(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].Version != "0017" {
		t.Fatalf("%+v", ms)
	}
}
