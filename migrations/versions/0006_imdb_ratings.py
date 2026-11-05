"""IMDb's own ratings, loaded nightly from IMDb's free dataset, so the title
page's IMDb score needs no outside service.

Revision ID: 0006
Revises: 0005
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0006"
down_revision = "0005"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "imdb_ratings",
        sa.Column("tconst", sa.String(12), primary_key=True),
        sa.Column("rating", sa.Float(), nullable=False),
        sa.Column("votes", sa.Integer(), nullable=False),
    )


def downgrade() -> None:
    op.drop_table("imdb_ratings")
