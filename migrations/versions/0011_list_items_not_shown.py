"""How many titles of a followed MDBList list can't be shown (MDBList sent
them without a TMDB id), so the list page can say so.

Revision ID: 0011
Revises: 0010
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0011"
down_revision = "0010"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("lists", sa.Column("not_shown", sa.Integer(), nullable=True))


def downgrade() -> None:
    op.drop_column("lists", "not_shown")
