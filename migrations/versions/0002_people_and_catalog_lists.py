"""People and catalog lists

- users, user_settings, list_favorites, digests and play_sessions tables.
- user_id on everything personal; existing rows become the owner's
  (OWNER_USERNAME, default danitex), linked to JELLYFIN_USER_ID.
- Lists get owner_id (NULL = catalog list everyone sees) and a generic
  origin (trakt | mdblist). Lists made by the owner stay theirs and
  editable; curated copies become shared, fixed Shared lists.
- Home rows and dismissed prompts move from app_settings to the owner's
  user_settings; Trakt sync state is dropped.
- title_extras gains Metacritic, Letterboxd and MDBList scores.

Revision ID: 0002
Revises: 0001
"""
from __future__ import annotations

import datetime as dt
import os

import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None

PERSONAL = ["watchlist", "user_ratings", "favorites", "history", "requests"]
OLD_UNIQUE = {
    "watchlist": ("uq_watchlist_title", ["media_type", "tmdb_id"]),
    "user_ratings": ("uq_rating_title", ["media_type", "tmdb_id"]),
    "favorites": ("uq_favorite_title", ["media_type", "tmdb_id"]),
    "history": ("uq_history_source_ref", ["source", "source_ref"]),
}


def upgrade() -> None:
    conn = op.get_bind()

    op.create_table(
        "users",
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("username", sa.String(length=200), nullable=False),
        sa.Column("email", sa.String(length=320), nullable=True),
        sa.Column("display_name", sa.String(length=200), nullable=True),
        sa.Column("is_admin", sa.Boolean(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("last_seen_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("jellyfin_user_id", sa.String(length=64), nullable=True),
        sa.Column("jellyfin_user_name", sa.String(length=200), nullable=True),
        sa.Column("jellyfin_token_enc", sa.Text(), nullable=True),
        sa.Column("jellyfin_linked_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("seerr_user_id", sa.Integer(), nullable=True),
        sa.Column("scrobble_token_hash", sa.String(length=64), nullable=True),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("username"),
        sa.UniqueConstraint("scrobble_token_hash"),
    )
    op.create_index("ix_users_jellyfin_user_id", "users", ["jellyfin_user_id"])

    owner = (os.environ.get("OWNER_USERNAME") or "danitex").strip().lower()
    jf_user = (os.environ.get("JELLYFIN_USER_ID") or "").strip() or None
    now = dt.datetime.now(dt.timezone.utc)
    owner_id = conn.execute(
        sa.text(
            "INSERT INTO users (username, is_admin, created_at, jellyfin_user_id, jellyfin_linked_at) "
            "VALUES (:u, true, :now, :jf, :linked) RETURNING id"
        ),
        {"u": owner, "now": now, "jf": jf_user, "linked": now if jf_user else None},
    ).scalar_one()

    op.create_table(
        "user_settings",
        sa.Column("user_id", sa.Integer(), nullable=False),
        sa.Column("key", sa.String(length=100), nullable=False),
        sa.Column("value", sa.Text(), nullable=False),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("user_id", "key"),
    )

    # --- personal tables get user_id ------------------------------------------
    for table in PERSONAL:
        op.add_column(table, sa.Column("user_id", sa.Integer(), nullable=True))
        conn.execute(sa.text(f"UPDATE {table} SET user_id = :o"), {"o": owner_id})
        op.alter_column(table, "user_id", nullable=False)
        op.create_foreign_key(f"fk_{table}_user", table, "users", ["user_id"], ["id"], ondelete="CASCADE")
        op.create_index(f"ix_{table}_user_id", table, ["user_id"])
        if table in OLD_UNIQUE:
            name, cols = OLD_UNIQUE[table]
            op.drop_constraint(name, table, type_="unique")
            op.create_unique_constraint(name, table, ["user_id", *cols])
    op.create_index("ix_history_user_time", "history", ["user_id", "watched_at"])

    # AI rows are per person now; the cache simply rebuilds.
    conn.execute(sa.text("DELETE FROM ai_row_cache"))
    op.add_column("ai_row_cache", sa.Column("user_id", sa.Integer(), nullable=False))
    op.add_column("ai_row_cache", sa.Column("model", sa.String(length=100), nullable=True))
    op.create_foreign_key("fk_ai_row_cache_user", "ai_row_cache", "users", ["user_id"], ["id"], ondelete="CASCADE")
    op.create_index("ix_ai_row_cache_user_id", "ai_row_cache", ["user_id"])

    # --- lists -----------------------------------------------------------------
    op.add_column("lists", sa.Column("owner_id", sa.Integer(), nullable=True))
    op.add_column("lists", sa.Column("origin", sa.String(length=20), nullable=True))
    op.add_column("lists", sa.Column("origin_id", sa.String(length=64), nullable=True))
    op.add_column("lists", sa.Column("origin_user", sa.String(length=200), nullable=True))
    op.add_column("lists", sa.Column("origin_slug", sa.String(length=300), nullable=True))
    conn.execute(sa.text(
        "UPDATE lists SET origin = 'trakt', origin_id = CAST(trakt_id AS VARCHAR), "
        "origin_user = trakt_user, origin_slug = trakt_slug "
        "WHERE trakt_id IS NOT NULL OR trakt_slug IS NOT NULL OR source IN ('trakt', 'trakt_mine', 'copy')"
    ))
    # The owner's own lists (made here, or brought over from their Trakt account).
    conn.execute(sa.text("UPDATE lists SET owner_id = :o, source = 'app' WHERE source IN ('app', 'trakt_mine')"),
                 {"o": owner_id})
    # Curated copies: shared with everyone, fixed (Shared lists).
    conn.execute(sa.text("UPDATE lists SET owner_id = NULL, source = 'copy' WHERE source IN ('copy', 'trakt')"))
    op.create_foreign_key("fk_lists_owner", "lists", "users", ["owner_id"], ["id"], ondelete="CASCADE")
    op.create_index("ix_lists_owner_id", "lists", ["owner_id"])
    op.create_index("ix_lists_origin_id", "lists", ["origin_id"])
    op.drop_index("ix_lists_trakt_id", table_name="lists")
    op.drop_column("lists", "trakt_id")
    op.drop_column("lists", "trakt_user")
    op.drop_column("lists", "trakt_slug")
    op.alter_column("lists", "source", type_=sa.String(length=20), existing_type=sa.String(length=30))

    op.create_table(
        "list_favorites",
        sa.Column("user_id", sa.Integer(), nullable=False),
        sa.Column("list_id", sa.Integer(), nullable=False),
        sa.Column("added_at", sa.DateTime(timezone=True), nullable=False),
        sa.ForeignKeyConstraint(["list_id"], ["lists.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("user_id", "list_id"),
    )

    # --- settings: Home rows and dismissed prompts become the owner's -----------
    conn.execute(sa.text(
        "INSERT INTO user_settings (user_id, key, value) "
        "SELECT :o, key, value FROM app_settings WHERE key LIKE 'home_rows:%' OR key = 'rate_prompt_dismissed'"
    ), {"o": owner_id})
    conn.execute(sa.text(
        "DELETE FROM app_settings WHERE key LIKE 'home_rows:%' "
        "OR key IN ('rate_prompt_dismissed', 'trakt_profile_sync', 'enabled_providers')"
    ))

    # --- shared catalog -------------------------------------------------------
    op.drop_column("library_items", "played")  # played state is per person (history)
    op.add_column("title_extras", sa.Column("metacritic", sa.Integer(), nullable=True))
    op.add_column("title_extras", sa.Column("letterboxd", sa.Float(), nullable=True))
    op.add_column("title_extras", sa.Column("mdblist_score", sa.Integer(), nullable=True))
    op.create_index("ix_titles_imdb_id", "titles", ["imdb_id"])

    # --- new per-person tables --------------------------------------------------
    op.create_table(
        "digests",
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("user_id", sa.Integer(), nullable=False),
        sa.Column("kind", sa.String(length=10), nullable=False),
        sa.Column("period_start", sa.DateTime(timezone=True), nullable=False),
        sa.Column("period_end", sa.DateTime(timezone=True), nullable=False),
        sa.Column("subject", sa.String(length=300), nullable=False),
        sa.Column("html", sa.Text(), nullable=False),
        sa.Column("data_json", sa.Text(), nullable=False),
        sa.Column("model", sa.String(length=100), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("sent_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("sent_to", sa.String(length=320), nullable=True),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("user_id", "kind", "period_start", name="uq_digest_period"),
    )
    op.create_index("ix_digests_user_id", "digests", ["user_id"])

    op.create_table(
        "play_sessions",
        sa.Column("id", sa.String(length=40), nullable=False),
        sa.Column("user_id", sa.Integer(), nullable=False),
        sa.Column("media_type", sa.String(length=10), nullable=False),
        sa.Column("tmdb_id", sa.Integer(), nullable=True),
        sa.Column("season", sa.Integer(), nullable=True),
        sa.Column("episode", sa.Integer(), nullable=True),
        sa.Column("title", sa.String(length=500), nullable=False),
        sa.Column("jellyfin_item_id", sa.String(length=64), nullable=False),
        sa.Column("player", sa.String(length=40), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("outcome", sa.String(length=20), nullable=True),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index("ix_play_sessions_user_id", "play_sessions", ["user_id"])


def downgrade() -> None:
    conn = op.get_bind()
    op.drop_index("ix_play_sessions_user_id", table_name="play_sessions")
    op.drop_table("play_sessions")
    op.drop_index("ix_digests_user_id", table_name="digests")
    op.drop_table("digests")
    op.drop_index("ix_titles_imdb_id", table_name="titles")
    op.drop_column("title_extras", "mdblist_score")
    op.drop_column("title_extras", "letterboxd")
    op.drop_column("title_extras", "metacritic")
    op.add_column("library_items", sa.Column("played", sa.Boolean(), nullable=False, server_default=sa.false()))

    conn.execute(sa.text(
        "INSERT INTO app_settings (key, value) SELECT key, value FROM user_settings "
        "WHERE user_id = (SELECT min(id) FROM users) AND (key LIKE 'home_rows:%' OR key = 'rate_prompt_dismissed') "
        "ON CONFLICT (key) DO NOTHING"
    ))
    op.drop_table("list_favorites")

    op.add_column("lists", sa.Column("trakt_id", sa.Integer(), nullable=True))
    op.add_column("lists", sa.Column("trakt_user", sa.String(length=200), nullable=True))
    op.add_column("lists", sa.Column("trakt_slug", sa.String(length=300), nullable=True))
    conn.execute(sa.text(
        "UPDATE lists SET trakt_id = CAST(origin_id AS INTEGER), trakt_user = origin_user, trakt_slug = origin_slug "
        "WHERE origin = 'trakt' AND origin_id ~ '^[0-9]+$'"
    ))
    op.create_index("ix_lists_trakt_id", "lists", ["trakt_id"])
    op.drop_index("ix_lists_origin_id", table_name="lists")
    op.drop_index("ix_lists_owner_id", table_name="lists")
    op.drop_constraint("fk_lists_owner", "lists", type_="foreignkey")
    for col in ("origin_slug", "origin_user", "origin_id", "origin", "owner_id"):
        op.drop_column("lists", col)

    op.drop_index("ix_ai_row_cache_user_id", table_name="ai_row_cache")
    op.drop_constraint("fk_ai_row_cache_user", "ai_row_cache", type_="foreignkey")
    op.drop_column("ai_row_cache", "model")
    op.drop_column("ai_row_cache", "user_id")

    op.drop_index("ix_history_user_time", table_name="history")
    for table in PERSONAL:
        if table in OLD_UNIQUE:
            name, cols = OLD_UNIQUE[table]
            op.drop_constraint(name, table, type_="unique")
        # Keep only the first person's rows so the old unique constraints hold.
        conn.execute(sa.text(f"DELETE FROM {table} WHERE user_id <> (SELECT min(id) FROM users)"))
        if table in OLD_UNIQUE:
            op.create_unique_constraint(name, table, cols)
        op.drop_index(f"ix_{table}_user_id", table_name=table)
        op.drop_constraint(f"fk_{table}_user", table, type_="foreignkey")
        op.drop_column(table, "user_id")

    op.drop_table("user_settings")
    op.drop_index("ix_users_jellyfin_user_id", table_name="users")
    op.drop_table("users")
