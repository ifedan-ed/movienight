// Package db holds the database schema and its migrations as files, embedded
// into the binaries so the migrate command needs nothing beside itself.
//
//   - schema.sql is the schema exactly as Alembic left it at revision 0016
//     (pg_dump --schema-only of a database migrated by the Python app's
//     Alembic migrations, now removed). It is the
//     Go server's baseline: an existing install is already at it, and a new,
//     empty database is created from it.
//   - migrations/NNNN_name.sql are the changes after 0016, applied in order by
//     `migrate` (internal/migrate), each once, each in its own transaction.
//
// sqlc reads both (sqlc.yaml), so the generated queries always match the
// schema the migrations produce.
package db

import "embed"

// Baseline is the schema at Alembic revision 0016.
//
//go:embed schema.sql
var Baseline string

// BaselineVersion is the Alembic revision the baseline corresponds to. The
// migrate command refuses a database Alembic left at any other revision.
const BaselineVersion = "0016"

// Migrations are the plain SQL migrations after the baseline.
//
//go:embed migrations/*.sql
var Migrations embed.FS
