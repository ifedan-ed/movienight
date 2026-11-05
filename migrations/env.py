"""Alembic environment. The schema is app.core.db.Base.metadata
(app/models.py). Migrations run with a plain (sync) psycopg connection; the
URL is the app's DATABASE_URL unless a caller passes one."""
from __future__ import annotations

from logging.config import fileConfig

from alembic import context
from sqlalchemy import engine_from_config, pool

from app import models  # noqa: F401  (registers the tables)
from app.core.config import settings
from app.core.db import Base

config = context.config
if config.config_file_name is not None and not config.attributes.get("skip_logging"):
    fileConfig(config.config_file_name)

target_metadata = Base.metadata
URL = config.attributes.get("url") or settings.database_url


def run_offline() -> None:
    context.configure(url=URL, target_metadata=target_metadata, literal_binds=True, compare_type=True)
    with context.begin_transaction():
        context.run_migrations()


def run_online() -> None:
    section = config.get_section(config.config_ini_section) or {}
    section["sqlalchemy.url"] = URL
    engine = engine_from_config(section, prefix="sqlalchemy.", poolclass=pool.NullPool)
    with engine.connect() as conn:
        context.configure(connection=conn, target_metadata=target_metadata, compare_type=True)
        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_offline()
else:
    run_online()
