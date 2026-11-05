"""Trakt's own id for a title (from MDBList), so its Trakt link opens the
title: Trakt's new site no longer finds a title by its TMDB or IMDb id.

Revision ID: 0007
Revises: 0006
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0007"
down_revision = "0006"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("title_extras", sa.Column("trakt_id", sa.Integer(), nullable=True))


def downgrade() -> None:
    op.drop_column("title_extras", "trakt_id")
