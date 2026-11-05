"""Database access: one async engine (psycopg 3) for the app, Alembic
migrations on start. Every request gets its own AsyncSession; background
jobs open their own with `session()`."""
from __future__ import annotations

import asyncio
import logging
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path

from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker, create_async_engine
from sqlalchemy.orm import DeclarativeBase

from app.core.config import settings

log = logging.getLogger("movienight.db")
ROOT = Path(__file__).resolve().parents[2]


class Base(DeclarativeBase):
    pass


def make_engine(url: str | None = None) -> AsyncEngine:
    # hide_parameters: a database error that ends up in the log shows its SQL
    # but not the values sent with it (titles, reviews, names, emails). The
    # error text Postgres sends back can still quote a row ("Failing row
    # contains (...)", "Key (...)=(...)"); app/core/logs.py cuts those.
    # docs/limits.md, "Logs".
    return create_async_engine(url or settings.database_url, pool_pre_ping=True, pool_size=10, max_overflow=10,
                               hide_parameters=True)


engine = make_engine()
SessionLocal = async_sessionmaker(engine, expire_on_commit=False, autoflush=False)


@asynccontextmanager
async def session() -> AsyncIterator[AsyncSession]:
    async with SessionLocal() as s:
        yield s


def run_migrations(url: str | None = None) -> None:
    """Bring the schema to the latest Alembic revision (migrations/).
    Synchronous on purpose: called once at start, before serving."""
    from alembic import command
    from alembic.config import Config

    cfg = Config(str(ROOT / "alembic.ini"))
    cfg.set_main_option("script_location", str(ROOT / "migrations"))
    cfg.attributes["skip_logging"] = True
    if url:
        cfg.attributes["url"] = url
    command.upgrade(cfg, "head")


async def init_db() -> None:
    await asyncio.to_thread(run_migrations)
