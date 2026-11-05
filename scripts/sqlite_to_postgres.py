"""One-time move of Movie Night's data from its old SQLite file to Postgres.

    docker compose run --rm --no-deps -e RUN_JOBS=false web \\
        python -m scripts.sqlite_to_postgres /data/movienight.db

Brings Postgres to the latest schema first, refuses to run if Postgres
already holds data, copies every current table (only the columns both sides
have), and moves the id sequences past the copied rows. The SQLite file is
left as it was.
"""
from __future__ import annotations

import sys

from sqlalchemy import create_engine, inspect, select, text

from app import models  # noqa: F401  (registers the tables)
from app.core.db import Base, engine, run_migrations

# Parents before children (list_items points at lists).
TABLES = [
    "titles", "library_items", "watchlist", "user_ratings", "favorites", "history",
    "lists", "list_items", "requests", "app_settings", "ai_row_cache",
]


def main(sqlite_path: str) -> None:
    if engine.dialect.name != "postgresql":
        sys.exit("DATABASE_URL must point at Postgres")
    run_migrations()
    src = create_engine(f"sqlite:///{sqlite_path}")
    src_tables = set(inspect(src).get_table_names())

    with engine.connect() as conn:
        busy = [t for t in TABLES if conn.execute(text(f"SELECT 1 FROM {t} LIMIT 1")).first()]
    if busy:
        sys.exit(f"Postgres already has data in {busy}; not copying twice")

    with src.connect() as s, engine.begin() as d:
        for name in TABLES:
            if name not in src_tables:
                print(f"{name}: not in SQLite, skipped")
                continue
            table = Base.metadata.tables[name]
            have = {c["name"] for c in inspect(src).get_columns(name)}
            cols = [c for c in table.c if c.name in have]
            rows = [dict(r._mapping) for r in s.execute(select(*cols))]
            for i in range(0, len(rows), 1000):
                d.execute(table.insert(), rows[i:i + 1000])
            print(f"{name}: {len(rows)} rows")
        for name in TABLES:
            if "id" in Base.metadata.tables[name].c:
                d.execute(text(
                    f"SELECT setval(pg_get_serial_sequence('{name}', 'id'), "
                    f"COALESCE((SELECT MAX(id) FROM {name}), 1), (SELECT MAX(id) FROM {name}) IS NOT NULL)"
                ))
    print("done")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
