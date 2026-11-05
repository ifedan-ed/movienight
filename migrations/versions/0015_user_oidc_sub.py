"""Sign-in with OIDC (optional): users.oidc_sub stores the provider's stable
subject claim ("sub"), so a person is matched by subject rather than by a
username or email the provider might change. Added for
app/api/v1/auth_oidc.py.

Revision ID: 0015
Revises: 0014
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0015"
down_revision = "0014"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # A new nullable column and a unique constraint: existing rows keep NULL,
    # and the code before this revision never reads it, so going back needs
    # only the downgrade below.
    op.add_column("users", sa.Column("oidc_sub", sa.String(255), nullable=True))
    op.create_unique_constraint("uq_users_oidc_sub", "users", ["oidc_sub"])


def downgrade() -> None:
    op.drop_constraint("uq_users_oidc_sub", "users", type_="unique")
    op.drop_column("users", "oidc_sub")
