"""Watch progress from the Movie Night plugin for Jellyfin: where someone is
in a film or episode they started and haven't finished.

Revision ID: 0009
Revises: 0008
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0009"
down_revision = "0008"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "watch_progress",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
        sa.Column("key", sa.String(60), nullable=False),
        sa.Column("media_type", sa.String(10), nullable=False),
        sa.Column("tmdb_id", sa.Integer(), nullable=False),
        sa.Column("season", sa.Integer(), nullable=True),
        sa.Column("episode", sa.Integer(), nullable=True),
        sa.Column("title", sa.String(500), nullable=False, server_default=""),
        sa.Column("episode_title", sa.String(500), nullable=True),
        sa.Column("position_s", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("duration_s", sa.Integer(), nullable=True),
        sa.Column("state", sa.String(10), nullable=False, server_default="playing"),
        sa.Column("client", sa.String(100), nullable=True),
        sa.Column("device", sa.String(100), nullable=True),
        sa.Column("jellyfin_item_id", sa.String(64), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.UniqueConstraint("user_id", "key", name="uq_watch_progress"),
    )
    op.create_index("ix_watch_progress_user_id", "watch_progress", ["user_id"])


def downgrade() -> None:
    op.drop_index("ix_watch_progress_user_id", table_name="watch_progress")
    op.drop_table("watch_progress")
