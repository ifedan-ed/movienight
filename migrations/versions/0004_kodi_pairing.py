"""Kodi (and other TV apps) sign-in: device tokens get a scope, and a short
code the TV shows is confirmed by the person on the web.

Revision ID: 0004
Revises: 0003
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0004"
down_revision = "0003"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("app_tokens", sa.Column("scope", sa.String(length=16), nullable=False, server_default="app"))
    op.create_table(
        "pair_codes",
        sa.Column("code_hash", sa.String(length=64), primary_key=True),
        sa.Column("poll_hash", sa.String(length=64), nullable=False),
        sa.Column("device", sa.String(length=100), nullable=False),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=True),
        sa.Column("token_enc", sa.Text(), nullable=True),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.UniqueConstraint("poll_hash"),
    )


def downgrade() -> None:
    op.drop_table("pair_codes")
    op.drop_column("app_tokens", "scope")
