"""Database tables.

Shared by everyone on the install: titles (TMDB cache), title_extras (outside
scores), imdb_ratings (IMDb's own ratings), library_items (what is in
Jellyfin), catalog lists (owner_id NULL: Shared and MDBList lists) and
app_settings.

Per person (user_id): watchlist, ratings, favorites (titles), episode
favorites (episodes someone liked), episode ratings, history, own lists,
starred lists, requests, AI rows, digests, play sessions, watch progress and
settings.

Titles are keyed by (media_type, tmdb_id) with media_type "movie" or "tv".
History rows for episodes use media_type "episode" with the show's tmdb_id
plus season and episode numbers; episode favorites and episode ratings are
keyed the same way (the show's tmdb_id, season, episode).
"""
from __future__ import annotations

import datetime as dt

from sqlalchemy import (Boolean, DateTime, Float, ForeignKey, Index, Integer, String, Text,
                        UniqueConstraint)
from sqlalchemy.orm import Mapped, mapped_column

from app.core.db import Base


def utcnow() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


# Retention: nothing is purged on a schedule. Kept for good: history,
# ratings and comments, digests (with their HTML), ai_row_cache (a row per
# rebuild), play_sessions, requests, the titles cache and the MDBList copies
# in app_settings. Deleted by the app: expired sign-in and pairing codes
# (when the next one is made), expired device tokens, library_items gone
# from Jellyfin, watch_progress once finished, an episode_favorites row
# when its episode is unliked, and an episode_ratings row when its rating is
# removed. Deleting a users row
# deletes everything of that person (every user_id foreign key cascades);
# there is no self-service Delete account. See docs/limits.md and README,
# "Privacy and accounts".


# ---------------------------------------------------------------------------
# People
# ---------------------------------------------------------------------------


class User(Base):
    """One person, created the first time they open the app (the username
    comes from their OIDC sign-in)."""

    __tablename__ = "users"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    username: Mapped[str] = mapped_column(String(200), unique=True)  # lower case
    email: Mapped[str | None] = mapped_column(String(320), nullable=True)
    display_name: Mapped[str | None] = mapped_column(Text, nullable=True)
    is_admin: Mapped[bool] = mapped_column(Boolean, default=False)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    last_seen_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    # Their account on the install's Jellyfin. The token is encrypted at rest
    # (core/crypto.py) and only used server-side, for play links and plays.
    jellyfin_user_id: Mapped[str | None] = mapped_column(String(64), nullable=True, index=True)
    jellyfin_user_name: Mapped[str | None] = mapped_column(String(200), nullable=True)
    jellyfin_token_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    jellyfin_linked_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    # Their user inside Seerr (created from the Jellyfin account on first request).
    seerr_user_id: Mapped[int | None] = mapped_column(Integer, nullable=True)
    # For scrobbling from other players (Kodi add-on): sha256 of the token.
    scrobble_token_hash: Mapped[str | None] = mapped_column(String(64), nullable=True, unique=True)
    # Sign-in with OIDC (optional; app/api/v1/auth_oidc.py): the provider's
    # stable subject ("sub") claim, so a changed email keeps the same person.
    oidc_sub: Mapped[str | None] = mapped_column(String(255), nullable=True, unique=True)
    # Sign-in with a password (optional; app/services/passwords.py): an
    # scrypt hash, never the password. NULL: no password set.
    password_hash: Mapped[str | None] = mapped_column(Text, nullable=True)
    # Raised to sign out every browser at once (a password change or reset):
    # a session made under an older number no longer counts (app/api/deps.py).
    session_epoch: Mapped[int] = mapped_column(Integer, default=0, server_default="0")


class UserSetting(Base):
    """Per-person settings as JSON values: Home rows per tab, players,
    digest choices, dismissed rate prompts."""

    __tablename__ = "user_settings"

    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), primary_key=True)
    key: Mapped[str] = mapped_column(String(100), primary_key=True)
    value: Mapped[str] = mapped_column(Text)


class AppSetting(Base):
    """Install-wide key/value state (Movie Night's copy of MDBList's lists,
    status for admins, ...)."""

    __tablename__ = "app_settings"

    key: Mapped[str] = mapped_column(String(100), primary_key=True)
    value: Mapped[str] = mapped_column(Text)


# ---------------------------------------------------------------------------
# Shared catalog
# ---------------------------------------------------------------------------


class Title(Base):
    """TMDB metadata cache, so posters and names for list, watchlist and
    history rows need no network call once seen. Refreshed when stale."""

    __tablename__ = "titles"

    key: Mapped[str] = mapped_column(String(40), primary_key=True)  # "movie:603"
    media_type: Mapped[str] = mapped_column(String(10))
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    title: Mapped[str] = mapped_column(Text, default="")
    original_title: Mapped[str | None] = mapped_column(Text, nullable=True)
    year: Mapped[int | None] = mapped_column(Integer, nullable=True)
    release_date: Mapped[str | None] = mapped_column(String(20), nullable=True)
    poster_path: Mapped[str | None] = mapped_column(String(200), nullable=True)
    backdrop_path: Mapped[str | None] = mapped_column(String(200), nullable=True)
    overview: Mapped[str | None] = mapped_column(Text, nullable=True)
    genres_json: Mapped[str | None] = mapped_column(Text, nullable=True)  # ["Drama", ...]
    runtime: Mapped[int | None] = mapped_column(Integer, nullable=True)  # minutes (tv: per episode)
    vote_average: Mapped[float | None] = mapped_column(Float, nullable=True)
    vote_count: Mapped[int | None] = mapped_column(Integer, nullable=True)
    imdb_id: Mapped[str | None] = mapped_column(String(20), nullable=True, index=True)
    detailed: Mapped[bool] = mapped_column(Boolean, default=False)  # full detail fetched
    fetched_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class ImdbRating(Base):
    """IMDb's own rating for every rated IMDb title, from IMDb's free daily
    file (services/imdb.py), replaced whole each night."""

    __tablename__ = "imdb_ratings"

    tconst: Mapped[str] = mapped_column(String(12), primary_key=True)  # "tt0111161"
    rating: Mapped[float] = mapped_column(Float)  # 0-10
    votes: Mapped[int] = mapped_column(Integer)


class TitleExtra(Base):
    """Outside scores from MDBList (IMDb, Rotten Tomatoes, Metacritic,
    Letterboxd, Trakt) for the title page; asked again after a week, and a
    score MDBList doesn't send back is kept rather than cleared."""

    __tablename__ = "title_extras"

    key: Mapped[str] = mapped_column(String(40), primary_key=True)
    imdb_rating: Mapped[float | None] = mapped_column(Float, nullable=True)
    imdb_votes: Mapped[int | None] = mapped_column(Integer, nullable=True)
    rt_critics: Mapped[int | None] = mapped_column(Integer, nullable=True)  # percent
    rt_audience: Mapped[int | None] = mapped_column(Integer, nullable=True)  # percent
    rt_url: Mapped[str | None] = mapped_column(String(300), nullable=True)
    metacritic: Mapped[int | None] = mapped_column(Integer, nullable=True)
    letterboxd: Mapped[float | None] = mapped_column(Float, nullable=True)  # 0-5
    trakt_rating: Mapped[float | None] = mapped_column(Float, nullable=True)  # 0-10
    trakt_votes: Mapped[int | None] = mapped_column(Integer, nullable=True)
    mdblist_score: Mapped[int | None] = mapped_column(Integer, nullable=True)  # 0-100
    wiki_title: Mapped[str | None] = mapped_column(String(300), nullable=True)
    wiki_extract: Mapped[str | None] = mapped_column(Text, nullable=True)
    fetched_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class LibraryItem(Base):
    """What is in the install's Jellyfin right now (library sync job)."""

    __tablename__ = "library_items"

    jellyfin_id: Mapped[str] = mapped_column(String(64), primary_key=True)
    media_type: Mapped[str] = mapped_column(String(10))  # movie | tv
    tmdb_id: Mapped[int | None] = mapped_column(Integer, nullable=True, index=True)
    title: Mapped[str] = mapped_column(Text, default="")
    year: Mapped[int | None] = mapped_column(Integer, nullable=True)
    date_added: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    synced_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class UserList(Base):
    """A list of titles.

    owner_id: the person who keeps it; NULL for catalog lists everyone sees.
    kind:     list (items stored here) | smart (saved filters, run live)
    source:   app    made here (owner's own, editable)
              copy   fixed copy of a curated list (Shared list)
              follow kept in step with its MDBList original
    origin:   where a copy/follow came from: trakt | mdblist
    """

    __tablename__ = "lists"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    owner_id: Mapped[int | None] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), nullable=True, index=True)
    name: Mapped[str] = mapped_column(Text)
    description: Mapped[str | None] = mapped_column(Text, nullable=True)
    kind: Mapped[str] = mapped_column(String(20), default="list")
    source: Mapped[str] = mapped_column(String(20), default="app")
    origin: Mapped[str | None] = mapped_column(String(20), nullable=True)
    origin_id: Mapped[str | None] = mapped_column(String(64), nullable=True, index=True)
    origin_user: Mapped[str | None] = mapped_column(Text, nullable=True)
    origin_slug: Mapped[str | None] = mapped_column(Text, nullable=True)
    likes: Mapped[int | None] = mapped_column(Integer, nullable=True)
    # Titles of a followed MDBList list that MDBList sent without a TMDB id:
    # they can't be shown (titles are known by it), so the list page says how many.
    not_shown: Mapped[int | None] = mapped_column(Integer, nullable=True)
    filters_json: Mapped[str | None] = mapped_column(Text, nullable=True)  # smart lists
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    synced_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class ListItem(Base):
    __tablename__ = "list_items"
    __table_args__ = (UniqueConstraint("list_id", "media_type", "tmdb_id", name="uq_list_item"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    list_id: Mapped[int] = mapped_column(Integer, ForeignKey("lists.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    position: Mapped[int] = mapped_column(Integer, default=0)
    added_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class ListFavorite(Base):
    """A list someone starred (usable as a filter on the Lists tab)."""

    __tablename__ = "list_favorites"

    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), primary_key=True)
    list_id: Mapped[int] = mapped_column(Integer, ForeignKey("lists.id", ondelete="CASCADE"), primary_key=True)
    added_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


# ---------------------------------------------------------------------------
# Per person
# ---------------------------------------------------------------------------


class WatchlistEntry(Base):
    __tablename__ = "watchlist"
    __table_args__ = (UniqueConstraint("user_id", "media_type", "tmdb_id", name="uq_watchlist_title"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    added_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    source: Mapped[str] = mapped_column(String(30), default="app")
    # Soft delete, so an import never brings back what someone removed.
    removed_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class UserRating(Base):
    __tablename__ = "user_ratings"
    __table_args__ = (UniqueConstraint("user_id", "media_type", "tmdb_id", name="uq_rating_title"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    rating: Mapped[int] = mapped_column(Integer)  # 1-10
    review: Mapped[str | None] = mapped_column(Text, nullable=True)  # their own notes
    rated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    source: Mapped[str] = mapped_column(String(30), default="app")


class Favorite(Base):
    __tablename__ = "favorites"
    __table_args__ = (UniqueConstraint("user_id", "media_type", "tmdb_id", name="uq_favorite_title"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    added_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    source: Mapped[str] = mapped_column(String(30), default="app")
    removed_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class EpisodeFavorite(Base):
    """One episode someone liked: the heart on each episode of a season page.
    Keyed by the show's tmdb_id with the season and episode numbers, like
    history. Separate from favorites, which hold titles (a film or a whole
    show): an episode's heart doesn't favourite its show. Unliking deletes
    the row (nothing imports episode hearts, so nothing could bring one
    back). No cap on how many."""

    __tablename__ = "episode_favorites"
    __table_args__ = (UniqueConstraint("user_id", "tmdb_id", "season", "episode", name="uq_episode_favorite"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    tmdb_id: Mapped[int] = mapped_column(Integer)  # the show
    season: Mapped[int] = mapped_column(Integer)
    episode: Mapped[int] = mapped_column(Integer)
    added_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    source: Mapped[str] = mapped_column(String(30), default="app")


class EpisodeRating(Base):
    """One episode someone rated: the stars next to the heart on each episode
    of a season page. Keyed like EpisodeFavorite (the show's tmdb_id, season,
    episode); the same scale as a title's rating (1 to 10, two points a star,
    checked by the API), but stars only: no comment, and only its person sees
    it. Separate from user_ratings: an episode's rating is not the show's, and
    it is not counted in Ratings, Your ratings, the community average, the AI
    picks or digests. Rating again replaces it; removing it deletes the row.
    No cap on how many."""

    __tablename__ = "episode_ratings"
    __table_args__ = (UniqueConstraint("user_id", "tmdb_id", "season", "episode", name="uq_episode_rating"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    tmdb_id: Mapped[int] = mapped_column(Integer)  # the show
    season: Mapped[int] = mapped_column(Integer)
    episode: Mapped[int] = mapped_column(Integer)
    rating: Mapped[int] = mapped_column(Integer)  # 1-10, as user_ratings
    rated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    source: Mapped[str] = mapped_column(String(30), default="app")


class HistoryEntry(Base):
    """One watch. Manual marks have no source_ref; synced rows (Jellyfin,
    other players) carry one so re-syncs never duplicate them."""

    __tablename__ = "history"
    __table_args__ = (
        UniqueConstraint("user_id", "source", "source_ref", name="uq_history_source_ref"),
        Index("ix_history_user_time", "user_id", "watched_at"),
    )

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")  # movie | episode
    tmdb_id: Mapped[int | None] = mapped_column(Integer, nullable=True, index=True)  # movie or show id
    season: Mapped[int | None] = mapped_column(Integer, nullable=True)
    episode: Mapped[int | None] = mapped_column(Integer, nullable=True)
    episode_title: Mapped[str | None] = mapped_column(Text, nullable=True)
    title: Mapped[str] = mapped_column(Text, default="")  # movie or show title at watch time
    watched_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow, index=True)
    source: Mapped[str] = mapped_column(String(30), default="manual")  # manual | jellyfin | trakt | player | api
    source_ref: Mapped[str | None] = mapped_column(String(100), nullable=True)


class RequestRecord(Base):
    """Requests sent to Seerr from this app."""

    __tablename__ = "requests"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10), default="movie")
    tmdb_id: Mapped[int] = mapped_column(Integer, index=True)
    seerr_request_id: Mapped[int | None] = mapped_column(Integer, nullable=True)
    status: Mapped[str] = mapped_column(String(30), default="requested")
    dry_run: Mapped[bool] = mapped_column(Boolean, default=False)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class AIRowCache(Base):
    """A person's "Picked for you" rows, rebuilt daily or after new watches."""

    __tablename__ = "ai_row_cache"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    generated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    model: Mapped[str | None] = mapped_column(String(100), nullable=True)
    payload_json: Mapped[str] = mapped_column(Text)


class Digest(Base):
    """A weekly digest or month in review, as sent (or previewed)."""

    __tablename__ = "digests"
    __table_args__ = (UniqueConstraint("user_id", "kind", "period_start", name="uq_digest_period"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    kind: Mapped[str] = mapped_column(String(10))  # week | month
    period_start: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True))
    period_end: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True))
    subject: Mapped[str] = mapped_column(String(300))
    html: Mapped[str] = mapped_column(Text)
    data_json: Mapped[str] = mapped_column(Text)
    model: Mapped[str | None] = mapped_column(String(100), nullable=True)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    sent_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    sent_to: Mapped[str | None] = mapped_column(String(320), nullable=True)
    # The schedule's tries at sending it (at most DIGEST_SEND_TRIES), and
    # when a send started and hasn't reported back yet: set before the SMTP
    # call and committed, so a send cut off midway (the process stopped, the
    # commit after it failed) is taken as sent, never sent again
    # (app/services/digest.py, send; migration 0012).
    send_tries: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    sending_since: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class WatchProgress(Base):
    """Where someone is in a film or episode they started and haven't
    finished, as Jellyfin reports it through the Movie Night plugin (any app
    that plays through Jellyfin, Kodi's Jellyfin add-on included). Cleared
    once it is finished."""

    __tablename__ = "watch_progress"
    __table_args__ = (UniqueConstraint("user_id", "key", name="uq_watch_progress"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    key: Mapped[str] = mapped_column(String(60))  # "movie:603" or "tv:1399:1:5"
    media_type: Mapped[str] = mapped_column(String(10))  # movie | episode
    tmdb_id: Mapped[int] = mapped_column(Integer)  # the film, or the show for an episode
    season: Mapped[int | None] = mapped_column(Integer, nullable=True)
    episode: Mapped[int | None] = mapped_column(Integer, nullable=True)
    title: Mapped[str] = mapped_column(Text, default="")
    episode_title: Mapped[str | None] = mapped_column(Text, nullable=True)
    position_s: Mapped[int] = mapped_column(Integer, default=0)
    duration_s: Mapped[int | None] = mapped_column(Integer, nullable=True)
    state: Mapped[str] = mapped_column(String(10), default="playing")  # playing | paused | stopped
    client: Mapped[str | None] = mapped_column(Text, nullable=True)  # "Kodi", "Jellyfin Web", ...
    device: Mapped[str | None] = mapped_column(Text, nullable=True)
    jellyfin_item_id: Mapped[str | None] = mapped_column(String(64), nullable=True)
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class PlaySession(Base):
    """A Play in VLC / Infuse / another player started from Movie Night.
    Finished either by the player's callback or by the person answering
    "Finished it?", which records the watch and marks it played in Jellyfin."""

    __tablename__ = "play_sessions"

    id: Mapped[str] = mapped_column(String(40), primary_key=True)  # random, also the callback secret
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    media_type: Mapped[str] = mapped_column(String(10))  # movie | episode
    tmdb_id: Mapped[int | None] = mapped_column(Integer, nullable=True)
    season: Mapped[int | None] = mapped_column(Integer, nullable=True)
    episode: Mapped[int | None] = mapped_column(Integer, nullable=True)
    title: Mapped[str] = mapped_column(Text, default="")
    jellyfin_item_id: Mapped[str] = mapped_column(String(64))
    player: Mapped[str] = mapped_column(String(40))
    started_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    finished_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    outcome: Mapped[str | None] = mapped_column(String(20), nullable=True)  # watched | not_finished | dismissed
    asked_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)  # "Finished it?" shown


class AppToken(Base):
    """A signed-in phone or tablet running the Movie Night app. The token
    itself is only shown to the app once; this keeps its sha256."""

    __tablename__ = "app_tokens"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), index=True)
    name: Mapped[str] = mapped_column(Text, default="Movie Night app")
    token_hash: Mapped[str] = mapped_column(String(64), unique=True)
    # "app": the phone app, the whole API. "scrobble": Kodi and the like,
    # which may only report what was watched.
    scope: Mapped[str] = mapped_column(String(16), default="app", server_default="app")
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    last_used_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class PairCode(Base):
    """A TV app signing in (ten minutes): it shows `code`, the person types it
    on the web, and the app, polling with its own secret, collects the token
    once (kept encrypted here only until then)."""

    __tablename__ = "pair_codes"

    code_hash: Mapped[str] = mapped_column(String(64), primary_key=True)
    poll_hash: Mapped[str] = mapped_column(String(64), unique=True)
    device: Mapped[str] = mapped_column(Text, default="Kodi")
    user_id: Mapped[int | None] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"), nullable=True)
    token_enc: Mapped[str | None] = mapped_column(Text, nullable=True)
    expires_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True))


class AppCode(Base):
    """A one-time sign-in code for the app (five minutes). The app proves it
    asked for it with the verifier behind `challenge` (PKCE)."""

    __tablename__ = "app_codes"

    code_hash: Mapped[str] = mapped_column(String(64), primary_key=True)
    user_id: Mapped[int] = mapped_column(Integer, ForeignKey("users.id", ondelete="CASCADE"))
    challenge: Mapped[str] = mapped_column(String(128))
    device: Mapped[str] = mapped_column(Text, default="Movie Night app")
    expires_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True))
