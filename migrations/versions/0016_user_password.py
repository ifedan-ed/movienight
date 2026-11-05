"""Sign-in with a username and password (optional, PASSWORD_SIGNIN):
users.password_hash keeps an scrypt hash (app/services/passwords.py), and
users.session_epoch signs every browser out at once when a password changes
(app/api/deps.py).

Revision ID: 0016
Revises: 0015
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0016"
down_revision = "0015"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # Nullable hash and a defaulted counter: existing rows need no backfill,
    # and code before this revision never reads either column.
    op.add_column("users", sa.Column("password_hash", sa.Text(), nullable=True))
    op.add_column("users", sa.Column("session_epoch", sa.Integer(), nullable=False, server_default="0"))


def downgrade() -> None:
    op.drop_column("users", "session_epoch")
    op.drop_column("users", "password_hash")
