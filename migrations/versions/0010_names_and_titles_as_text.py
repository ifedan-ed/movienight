"""Names and titles as text: no column width to cut them to.

Titles (from TMDB, Jellyfin and players), names (lists, devices, apps) and
the like were VARCHAR(100..500). The request-shape limits that fed them are
now settings (NAME_MAX_CHARS, REPORTED_MAX_CHARS; docs/limits.md, "Technical
limits"), which an owner may raise, and a title from TMDB or Jellyfin was cut
to fit without a word. As TEXT nothing is cut. In Postgres, VARCHAR(n) to
TEXT changes no stored data and rewrites nothing.

Downgrade shortens any longer value to the old width (left(value, n)).

Revision ID: 0010
Revises: 0009
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0010"
down_revision = "0009"
branch_labels = None
depends_on = None

# table -> {column: its old VARCHAR width}
COLUMNS = {
    "users": {"display_name": 200},
    "titles": {"title": 500, "original_title": 500},
    "library_items": {"title": 500},
    "lists": {"name": 300, "origin_user": 200, "origin_slug": 300},
    "history": {"title": 500, "episode_title": 500},
    "watch_progress": {"title": 500, "episode_title": 500, "client": 100, "device": 100},
    "play_sessions": {"title": 500},
    "app_tokens": {"name": 100},
    "pair_codes": {"device": 100},
    "app_codes": {"device": 100},
}


def upgrade() -> None:
    for table, columns in COLUMNS.items():
        for column, width in columns.items():
            op.alter_column(table, column, type_=sa.Text(), existing_type=sa.String(width))


def downgrade() -> None:
    for table, columns in COLUMNS.items():
        for column, width in columns.items():
            op.alter_column(table, column, type_=sa.String(width), existing_type=sa.Text(),
                            postgresql_using=f"left({column}, {width})")
