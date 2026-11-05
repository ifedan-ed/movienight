"""A digest's send tries and its send in progress, so the schedule sends
each one at most once and gives up after DIGEST_SEND_TRIES clear failures
(app/services/digest.py, send).

Revision ID: 0012
Revises: 0011
"""
from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "0012"
down_revision = "0011"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # A server default, so the rows already there start at 0 tries and the
    # code before this revision can still insert digests.
    op.add_column("digests", sa.Column("send_tries", sa.Integer(), nullable=False, server_default="0"))
    op.add_column("digests", sa.Column("sending_since", sa.DateTime(timezone=True), nullable=True))


def downgrade() -> None:
    op.drop_column("digests", "sending_since")
    op.drop_column("digests", "send_tries")
