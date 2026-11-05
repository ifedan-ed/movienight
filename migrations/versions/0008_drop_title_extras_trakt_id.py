"""The Trakt button is gone (Trakt's site needs a sign-in to show a title),
and with it the Trakt id kept for it.

Revision ID: 0008
Revises: 0007
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0008"
down_revision = "0007"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.drop_column("title_extras", "trakt_id")


def downgrade() -> None:
    op.add_column("title_extras", sa.Column("trakt_id", sa.Integer(), nullable=True))
