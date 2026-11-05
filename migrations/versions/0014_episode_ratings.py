"""Episode ratings: the stars next to the heart on each episode of a season
page (website and phone app). One row per person and episode, keyed like
episode_favorites by the show's tmdb_id with the season and episode numbers
(app/models.py, EpisodeRating).

Revision ID: 0014
Revises: 0013
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0014"
down_revision = "0013"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # A new table only: the code before this revision never reads it, so
    # going back to it needs nothing but the downgrade below.
    op.create_table(
        "episode_ratings",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
        sa.Column("tmdb_id", sa.Integer(), nullable=False),
        sa.Column("season", sa.Integer(), nullable=False),
        sa.Column("episode", sa.Integer(), nullable=False),
        sa.Column("rating", sa.Integer(), nullable=False),
        sa.Column("rated_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("source", sa.String(30), nullable=False, server_default="app"),
        sa.UniqueConstraint("user_id", "tmdb_id", "season", "episode", name="uq_episode_rating"),
    )
    op.create_index("ix_episode_ratings_user_id", "episode_ratings", ["user_id"])


def downgrade() -> None:
    # Drops every episode rating: take a backup first (README, "Updating").
    op.drop_index("ix_episode_ratings_user_id", table_name="episode_ratings")
    op.drop_table("episode_ratings")
