"""App sign-in: device tokens and one-time codes.

Revision ID: 0003
Revises: 0002
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0003"
down_revision = "0002"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "app_tokens",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
        sa.Column("name", sa.String(length=100), nullable=False),
        sa.Column("token_hash", sa.String(length=64), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("last_used_at", sa.DateTime(timezone=True), nullable=True),
        sa.UniqueConstraint("token_hash"),
    )
    op.create_index("ix_app_tokens_user_id", "app_tokens", ["user_id"])
    op.create_table(
        "app_codes",
        sa.Column("code_hash", sa.String(length=64), primary_key=True),
        sa.Column("user_id", sa.Integer(), sa.ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
        sa.Column("challenge", sa.String(length=128), nullable=False),
        sa.Column("device", sa.String(length=100), nullable=False),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
    )


def downgrade() -> None:
    op.drop_table("app_codes")
    op.drop_index("ix_app_tokens_user_id", table_name="app_tokens")
    op.drop_table("app_tokens")
