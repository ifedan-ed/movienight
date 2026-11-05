"""Play sessions remember when "Finished it?" was asked, so it is asked once.

Revision ID: 0005
Revises: 0004
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0005"
down_revision = "0004"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("play_sessions", sa.Column("asked_at", sa.DateTime(timezone=True), nullable=True))


def downgrade() -> None:
    op.drop_column("play_sessions", "asked_at")
