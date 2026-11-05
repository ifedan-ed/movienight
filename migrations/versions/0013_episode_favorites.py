"""Liked episodes: the heart on each episode of a season page (website and
phone app). One row per person and episode, keyed by the show's tmdb_id with
the season and episode numbers (app/models.py, EpisodeFavorite).

Revision ID: 0013
Revises: 0012
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0013"
down_revision = "0012"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # A new table only: the code before this revision never reads it, so
    # going back to it needs nothing but the downgrade below.
    op.create_table(
        "episode_favorites",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
        sa.Column("tmdb_id", sa.Integer(), nullable=False),
        sa.Column("season", sa.Integer(), nullable=False),
        sa.Column("episode", sa.Integer(), nullable=False),
        sa.Column("added_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("source", sa.String(30), nullable=False, server_default="app"),
        sa.UniqueConstraint("user_id", "tmdb_id", "season", "episode", name="uq_episode_favorite"),
    )
    op.create_index("ix_episode_favorites_user_id", "episode_favorites", ["user_id"])


def downgrade() -> None:
    # Drops every liked episode: take a backup first (README, "Updating").
    op.drop_index("ix_episode_favorites_user_id", table_name="episode_favorites")
    op.drop_table("episode_favorites")
