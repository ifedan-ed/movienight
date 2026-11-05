"""The API contract: every request body and response of /api/v1, as Pydantic
models. FastAPI validates input against these and publishes them as the
OpenAPI spec (/api/v1/openapi.json); docs/openapi.json is the committed copy
the phone app's types are made from (mobile: npm run types). Regenerate it
after an API change (README, "API").
"""
from __future__ import annotations

from enum import Enum
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field

from app.core.config import settings

# Request shapes: how long a text and how many items one call may carry.
# Technical limits, not usage caps (docs/limits.md, "Technical limits"):
# they keep one request from carrying unbounded data, since there is no
# overall cap on a request body's size, here or in the proxy. Each kind is a
# setting (app/core/config.py): TEXT_MAX_CHARS (a comment, a description, a
# player's link, a password), SEARCH_MAX_CHARS, NAME_MAX_CHARS (a name a
# person types), REPORTED_MAX_CHARS (what a player or the Jellyfin plugin
# reports) and BATCH_MAX_ITEMS (items in one call). Longer or more is
# refused with 422 and a plain sentence (app/main.py, _plain). The other
# bounds below say what a valid value is (a rating 1 to 10, a date, an id's
# pattern), not how much: whole numbers that are stored stop at the
# database's integer range (DB_INT).

# The largest whole number the database's integer columns hold: a season,
# an episode or a position in seconds beyond it can't be stored.
DB_INT = 2_147_483_647

TitleKey = Annotated[str, Field(pattern=r"^(movie|tv):\d{1,10}$")]


class MediaType(str, Enum):
    movie = "movie"
    tv = "tv"


class Model(BaseModel):
    model_config = ConfigDict(extra="ignore")


class Ok(Model):
    ok: bool = True


# ---------------------------------------------------------------------------
# Titles and their state
# ---------------------------------------------------------------------------


class Progress(Model):
    percent: int
    eta: str | None = None
    status: str | None = None


class TitleState(Model):
    in_library: bool = False
    jellyfin_id: str | None = None
    jellyfin_url: str | None = None
    # what the request button shows when not in the library
    request: Literal["pending", "processing", "partial", "available", "dry_run"] | None = None
    progress: Progress | None = None
    requested_at: str | None = None
    watchlist: bool = False
    watched: int = 0
    rating: int | None = None
    favorite: bool = False


class Card(TitleState):
    """A title as every grid and row shows it."""

    key: str = Field(examples=["movie:603"])
    media_type: MediaType
    tmdb_id: int
    title: str
    year: int | None = None
    poster: str | None = None
    backdrop: str | None = None
    vote: float | None = None
    # extras on some endpoints
    overview: str | None = None
    genres: list[str] | None = None
    hero_label: str | None = None
    request_status: str | None = None
    media_status: str | None = None
    requested_by: str | None = None


class UpNextItem(Model):
    """Jellyfin "continue watching" / "next up" entry."""

    key: str | None = None
    media_type: MediaType | None = None
    tmdb_id: int | None = None
    jellyfin_id: str | None = None
    title: str = ""
    subtitle: str | None = None
    progress: int = 0
    image_item: str | None = None
    series_item: str | None = None
    runtime_min: int | None = None
    season: int | None = None
    episode: int | None = None
    backdrop: str | None = None
    poster: str | None = None


class RowMore(Model):
    key: str
    title: str


ListGroup = Literal["mine", "shared", "mdblist"]


class PosterRow(Model):
    id: str
    title: str
    kicker: str | None = None
    subtitle: str | None = None
    style: Literal["poster"] = "poster"
    items: list[Card]
    more: RowMore | None = None
    list_id: int | None = None
    group: ListGroup | None = None


class LandscapeRow(Model):
    id: str
    title: str
    kicker: str | None = None
    subtitle: str | None = None
    style: Literal["landscape"] = "landscape"
    items: list[UpNextItem]


Row = Annotated[PosterRow | LandscapeRow, Field(discriminator="style")]


class RowsOut(Model):
    rows: list[Row]
    # When a row key has nothing to show for a reason the person should know
    # ("Picked for you" being made, or failed and when it is tried again).
    note: str | None = None


class HomeRowSpec(Model):
    key: str
    title: str
    multi: bool
    style: Literal["poster", "landscape"]


class HomeOut(Model):
    tab: str
    rows: list[HomeRowSpec]


class CardsOut(Model):
    items: list[Card]


class RequestsOut(CardsOut):
    page: int = 1
    more: bool = False  # another page after this one
    total: int = 0  # requests in all
    not_shown: int = 0  # on this page, requests that couldn't be shown (no TMDB id, or TMDB doesn't know them)


class PageOut(Model):
    title: str | None = None
    items: list[Card]
    more: bool
    page: int
    # Why a grid ends where it does, when that isn't the end of the list
    # (TMDB serves only its first 500 pages): shown under the last cards.
    note: str | None = None


class HomeConfigRow(Model):
    key: str
    on: bool
    title: str | None = None
    list: bool | None = None
    subtitle: str | None = None
    group: ListGroup | None = None


class HomeConfigOut(Model):
    tab: str
    rows: list[HomeConfigRow]


class HomeConfigIn(Model):
    rows: list[HomeConfigRow] = Field(max_length=settings.batch_max_items)  # BATCH_MAX_ITEMS


class Genre(Model):
    id: int
    name: str


class GenresOut(Model):
    genres: list[Genre]


class Provider(Model):
    id: int
    name: str


class ProvidersOut(Model):
    providers: list[Provider]


# ---------------------------------------------------------------------------
# Title page
# ---------------------------------------------------------------------------


class CastMember(Model):
    id: int | None = None
    name: str | None = None
    character: str | None = None
    photo: str | None = None


class StreamProvider(Model):
    id: int | None = None
    name: str | None = None
    logo: str | None = None


class Providers(Model):
    stream: list[StreamProvider] = []
    rent: list[StreamProvider] = []
    buy: list[StreamProvider] = []
    link: str | None = None


class Collection(Model):
    id: int | None = None
    name: str | None = None
    poster: str | None = None
    backdrop: str | None = None


class Season(Model):
    number: int
    name: str | None = None
    episodes: int | None = None
    air_date: str | None = None
    poster: str | None = None
    status: str | None = None
    watched: int = 0


class EpisodeRef(Model):
    season: int | None = None
    episode: int | None = None
    name: str | None = None
    air_date: str | None = None


class Watch(Model):
    ids: list[int]
    media_type: str
    key: str | None = None
    title: str
    season: int | None = None
    episode: int | None = None
    episode_title: str | None = None
    watched_at: str
    sources: list[str]


class ListMembership(Model):
    id: int
    name: str
    in_list: bool
    source: str


class Scores(Model):
    imdb_rating: float | None = None
    imdb_votes: int | None = None
    rt_critics: int | None = None
    rt_audience: int | None = None
    rt_url: str | None = None
    metacritic: int | None = None
    letterboxd: float | None = None
    trakt_rating: float | None = None
    trakt_votes: int | None = None
    mdblist_score: int | None = None


class ScoresOut(Model):
    scores: Scores | None = None
    # Set when MDBList couldn't refresh the outside scores (it refused, its own
    # limit for the key, or didn't answer): what happened, in plain words.
    note: str | None = None


class RatingEntry(Model):
    name: str
    rating: int
    review: str | None = None
    rated_at: str
    mine: bool = False


class Community(Model):
    """Ratings from the people on this install (this install)."""

    average: float | None = None
    count: int = 0
    mine: RatingEntry | None = None
    others: list[RatingEntry] = []


class Resume(Model):
    """Where this person is in the film, or in the show's latest started
    episode (from the Movie Night plugin for Jellyfin)."""

    season: int | None = None
    episode: int | None = None
    episode_title: str | None = None
    position_s: int
    duration_s: int | None = None
    percent: int | None = None
    state: str
    client: str | None = None
    device: str | None = None
    updated_at: str


class TitleDetail(Model):
    key: str
    media_type: MediaType
    tmdb_id: int
    title: str
    original_title: str | None = None
    year: int | None = None
    release_date: str | None = None
    poster: str | None = None
    backdrop: str | None = None
    overview: str | None = None
    genres: list[str] = []
    runtime: int | None = None
    vote: float | None = None
    vote_count: int | None = None
    imdb_id: str | None = None
    tagline: str | None = None
    status_text: str | None = None
    ongoing: bool = False
    certification: str | None = None
    directors: list[str] = []
    director_label: str
    writers: list[str] = []
    cast: list[CastMember] = []
    studios: list[str] = []
    networks: list[str] = []
    languages: list[str] = []
    trailer: str | None = None
    providers: Providers
    collection: Collection | None = None
    seasons: list[Season] = []
    number_of_seasons: int | None = None
    number_of_episodes: int | None = None
    last_episode: EpisodeRef | None = None
    next_episode: EpisodeRef | None = None
    imdb_url: str | None = None
    tmdb_url: str
    jellyfin_url: str | None = None
    state: TitleState
    scores: Scores | None = None
    community: Community | None = None
    watches: list[Watch] = []
    lists: list[ListMembership] = []
    resume: Resume | None = None


class RelatedCollection(Model):
    name: str | None = None
    items: list[Card]


class RelatedOut(Model):
    recommendations: list[Card]
    collection: RelatedCollection | None = None


class RequestIn(Model):
    seasons: list[Annotated[int, Field(ge=0, le=DB_INT)]] | None = Field(default=None, max_length=settings.batch_max_items)


class RequestOut(Model):
    ok: bool = True
    already: bool
    state: TitleState


class WatchlistOut(Model):
    ok: bool = True
    watchlist: bool


class FavoriteOut(Model):
    ok: bool = True
    favorite: bool


class WatchedIn(Model):
    watched_at: str | None = Field(default=None, max_length=40, description="ISO date or datetime; default now")


class WatchesOut(Model):
    ok: bool = True
    id: int | None = None
    watches: list[Watch]


class IdsIn(Model):
    ids: list[int] = Field(min_length=1, max_length=settings.batch_max_items)  # BATCH_MAX_ITEMS


class RatingIn(Model):
    rating: int = Field(ge=1, le=10)
    review: str | None = Field(default=None, max_length=settings.text_max_chars)  # TEXT_MAX_CHARS


class RatingOut(Model):
    ok: bool = True
    rating: int | None
    review: str | None = None


class EpisodeRatingIn(Model):
    """An episode's rating: the same scale as a title's (1 to 10, two points a
    star), stars only (an episode has no comment)."""

    rating: int = Field(ge=1, le=10)


class EpisodeRatingOut(Model):
    ok: bool = True
    rating: int | None


class ListsForTitleOut(Model):
    lists: list[ListMembership]


# ---------------------------------------------------------------------------
# Seasons and episodes
# ---------------------------------------------------------------------------


class Episode(Model):
    number: int
    name: str
    overview: str | None = None
    air_date: str | None = None
    aired: bool
    still: str | None = None
    runtime: int | None = None
    vote: float | None = None
    watched: int = 0
    watch_ids: list[int] = []
    last_watched_at: str | None = None
    in_library: bool = False
    jellyfin_id: str | None = None
    favorite: bool = False  # this person liked this episode (the heart)
    rating: int | None = None  # this person's rating of this episode, 1-10 (not the show's)


class SeasonOut(Model):
    show_id: int
    number: int
    name: str | None = None
    overview: str | None = None
    air_date: str | None = None
    poster: str | None = None
    episodes: list[Episode]


class NextEpisode(Model):
    season: int
    episode: int
    label: str
    favorite: bool = False  # this person's heart on this episode (not the show's)
    rating: int | None = None  # this person's rating of this episode, 1-10 (not the show's)


class NextEpisodeOut(Model):
    next: NextEpisode | None = None


class EpisodeMark(Model):
    season: int = Field(ge=0, le=DB_INT)
    episode: int = Field(ge=0, le=DB_INT)


class EpisodesIn(Model):
    episodes: list[EpisodeMark] = Field(min_length=1, max_length=settings.batch_max_items)  # BATCH_MAX_ITEMS
    watched: bool = True
    watched_at: str | None = Field(default=None, max_length=40)


class ChangedOut(Model):
    ok: bool = True
    changed: int


# ---------------------------------------------------------------------------
# Playing
# ---------------------------------------------------------------------------


class Player(Model):
    id: str
    name: str
    builtin: bool
    template: str | None = None
    enabled: bool = True


class SubtitleTrack(Model):
    index: int
    label: str
    language: str | None = None
    external: bool
    text: bool
    default: bool


class PlayInfo(Model):
    available: bool
    linked: bool
    jellyfin_item_id: str | None = None
    web_url: str | None = None
    title: str | None = None
    season: int | None = None
    episode: int | None = None
    subtitles: list[SubtitleTrack] = []
    chosen_subtitle: int | None = None
    resume_seconds: int = 0
    players: list[Player]
    default_player: str
    subtitle_language: str | None = None


class PlayIn(Model):
    player: str = Field(pattern=r"^[a-z0-9_]{1,40}$")
    return_to: Literal["web", "app"] = "web"  # where the player sends you back
    season: int | None = Field(default=None, ge=0, le=DB_INT)
    episode: int | None = Field(default=None, ge=0, le=DB_INT)
    subtitle: int | None = Field(default=None, ge=0, le=DB_INT)


class PlayOut(Model):
    session_id: str
    url: str
    web_url: str | None = None


class PlaySessionOut(Model):
    id: str
    media_type: str
    tmdb_id: int | None = None
    season: int | None = None
    episode: int | None = None
    title: str
    player: str
    started_at: str
    key: str | None = None
    poster: str | None = None


class OpenSessionOut(Model):
    session: PlaySessionOut | None = None


class BackOut(Model):
    ask: bool
    session: PlaySessionOut


class FinishIn(Model):
    outcome: Literal["watched", "not_finished", "dismissed"]


class PlayerSettingsOut(Model):
    players: list[Player]
    default_player: str
    subtitle_language: str | None = None
    ask_finished: bool = True


class CustomPlayerIn(Model):
    name: str = Field(min_length=1, max_length=settings.name_max_chars)  # NAME_MAX_CHARS
    template: str = Field(min_length=4, max_length=settings.text_max_chars)  # TEXT_MAX_CHARS


class PlayerSettingsIn(Model):
    # Limits the owner keeps (docs/limits.md): up to 20 players of your own,
    # and up to 30 players ticked; more is refused with 422 ("You can have up
    # to 20 players of your own.", app/main.py _plain).
    custom: list[CustomPlayerIn] = Field(default=[], max_length=20)
    default_player: str | None = Field(default=None, pattern=r"^[a-z0-9_]{1,40}$")
    enabled: list[Annotated[str, Field(pattern=r"^[a-z0-9_]{1,40}$")]] | None = Field(default=None, max_length=30)
    subtitle_language: str | None = Field(default=None, max_length=3)
    ask_finished: bool | None = None


# ---------------------------------------------------------------------------
# Lists
# ---------------------------------------------------------------------------


class ListSummary(Model):
    id: int
    name: str
    description: str = ""
    kind: str
    source: str
    origin: str | None = None
    group: ListGroup
    by: str
    likes: int | None = None
    count: int | None = None
    movies: int = 0
    shows: int = 0
    posters: list[str] = []
    pinned: bool = False
    starred: bool = False
    editable: bool = False
    deletable: bool = False


class PersonalCollection(Model):
    count: int
    posters: list[str]


class ListsOut(Model):
    watchlist: PersonalCollection
    favorites: PersonalCollection
    ratings: PersonalCollection
    lists: list[ListSummary]


class ListIn(Model):
    name: str = Field(min_length=1, max_length=settings.name_max_chars)  # NAME_MAX_CHARS
    description: str | None = Field(default=None, max_length=settings.text_max_chars)  # TEXT_MAX_CHARS


class SmartFilters(Model):
    media: MediaType = MediaType.movie
    genre: str | None = Field(default=None, pattern=r"^\d{1,6}$")
    provider: str | None = Field(default=None, pattern=r"^\d{1,6}$")
    year_from: int | None = Field(default=None, ge=1870, le=2100)
    year_to: int | None = Field(default=None, ge=1870, le=2100)
    min_rating: float | None = Field(default=None, ge=0, le=10)
    runtime_min: int | None = Field(default=None, ge=0, le=1000)
    runtime_max: int | None = Field(default=None, ge=0, le=1000)
    language: str | None = Field(default=None, pattern=r"^[a-zA-Z]{2,3}$")
    sort: str | None = Field(default=None, max_length=settings.name_max_chars)  # NAME_MAX_CHARS


class SmartIn(Model):
    name: str = Field(min_length=1, max_length=settings.name_max_chars)  # NAME_MAX_CHARS
    filters: SmartFilters


class ListPatch(Model):
    name: str | None = Field(default=None, max_length=settings.name_max_chars)  # NAME_MAX_CHARS
    description: str | None = Field(default=None, max_length=settings.text_max_chars)  # TEXT_MAX_CHARS
    pinned: bool | None = None
    starred: bool | None = None


class CreatedOut(Model):
    ok: bool = True
    id: int


class ListFlagsOut(Model):
    ok: bool = True
    pinned: bool
    starred: bool


class ListItemIn(Model):
    media_type: MediaType
    tmdb_id: int = Field(gt=0)


class AddedOut(Model):
    ok: bool = True
    added: bool


class ListOut(Model):
    id: int
    name: str
    description: str = ""
    kind: str
    source: str
    origin: str | None = None
    group: ListGroup
    by: str
    origin_url: str | None = None
    likes: int | None = None
    count: int | None = None
    movies: int = 0
    shows: int = 0
    # Titles of an MDBList list that MDBList sent without a TMDB id: not shown.
    not_shown: int = 0
    filters: dict | None = None
    pinned: bool
    starred: bool
    editable: bool
    deletable: bool
    synced_at: str | None = None
    media: MediaType | None = None
    sort: str
    items: list[Card]
    more: bool
    page: int
    note: str | None = None  # a smart list that stops at TMDB's 500 pages says so


class CountOut(Model):
    ok: bool = True
    count: int


class CollectionOut(Model):
    name: str
    count: int
    items: list[Card]
    more: bool
    page: int


class MDBListSummary(Model):
    mdblist_id: int | None = None
    name: str
    slug: str | None = None
    user: str = ""
    description: str = ""
    media: MediaType | None = None
    item_count: int | None = None
    likes: int | None = None
    dynamic: bool = False
    followed_id: int | None = None


class MDBListExploreOut(Model):
    mode: Literal["browse", "search", "link"]
    results: list[MDBListSummary] = []
    error: str | None = None
    enabled: bool = True


class MDBListPreviewOut(Model):
    list: MDBListSummary
    count: int
    items: list[Card]
    # Titles MDBList sent without a TMDB id: they can't be shown.
    not_shown: int = 0


class FollowIn(Model):
    ref: str = Field(min_length=1, max_length=settings.name_max_chars, description="MDBList list id or mdblist.com/lists/<user>/<slug> link")


class FollowOut(Model):
    ok: bool = True
    id: int
    count: int


# ---------------------------------------------------------------------------
# Profile, account, history, requests, search, people
# ---------------------------------------------------------------------------


class Stats(Model):
    movies: int
    movie_plays: int
    episodes: int
    shows: int
    hours: int
    this_year: int
    ratings: int
    watchlist: int


class JellyfinLink(Model):
    linked: bool
    can_play: bool = False  # signed in (token kept): play links work
    user_name: str | None = None
    linked_at: str | None = None
    server: str
    quick_connect: bool | None = None
    # A plain sentence when the person's plays couldn't be read from Jellyfin
    # the last time, or when Jellyfin lists plays of theirs without a date
    # (not added); None when all is well (services/status.py).
    plays_notice: str | None = None


class Features(Model):
    requests: bool
    mdblist: bool
    ai: bool
    email: bool
    dry_run: bool


class TextLimits(Model):
    text_max_chars: int = settings.text_max_chars
    search_max_chars: int = settings.search_max_chars
    name_max_chars: int = settings.name_max_chars
    reported_max_chars: int = settings.reported_max_chars
    batch_max_items: int = settings.batch_max_items  # the apps split bigger calls to fit


class MeOut(Model):
    user: str
    display_name: str | None = None
    email: str | None = None
    is_admin: bool
    stats: Stats
    jellyfin: JellyfinLink
    features: Features
    scrobble_token_set: bool
    # The install's text lengths (TEXT_MAX_CHARS, SEARCH_MAX_CHARS), so the
    # apps can count down to them instead of cutting text without a word.
    limits: TextLimits = TextLimits()
    # Admins only (empty for everyone else): what needs their attention, in
    # plain sentences, e.g. a library sync that didn't finish (services/status.py).
    admin_notices: list[str] = []


class QuickConnectOut(Model):
    code: str
    secret: str


class QuickConnectIn(Model):
    secret: str = Field(min_length=8, max_length=200)


class QuickConnectStateOut(Model):
    ok: bool = True
    linked: bool
    user_name: str | None = None


class PasswordLinkIn(Model):
    username: str = Field(min_length=1, max_length=settings.name_max_chars)  # NAME_MAX_CHARS
    password: str = Field(max_length=settings.text_max_chars)  # TEXT_MAX_CHARS (never kept)


class TokenOut(Model):
    ok: bool = True
    token: str


class EmailSettings(Model):
    weekly: bool
    monthly: bool
    yearly: bool
    address: str | None = None


class EmailSettingsIn(Model):
    weekly: bool | None = None
    monthly: bool | None = None
    yearly: bool | None = None
    address: str | None = Field(default=None, max_length=320)


class DigestOut(Model):
    id: int
    kind: Literal["week", "month", "year"]
    subject: str
    period_start: str
    period_end: str
    model: str | None = None
    sent_at: str | None = None
    sent_to: str | None = None


class SentOut(Model):
    ok: bool = True
    sent_to: str


class HistoryItem(Watch):
    card: Card | None = None
    # An episode's own heart and rating (this person's; not the show's).
    # Always false / null for films.
    episode_favorite: bool = False
    episode_rating: int | None = None


class HistoryOut(Model):
    items: list[HistoryItem]
    more: bool
    page: int
    total: int


class RatingsOut(Model):
    items: list[Card]
    more: bool
    page: int
    total: int


class RatePromptItem(Model):
    key: str
    media_type: MediaType
    tmdb_id: int
    title: str
    poster: str | None = None


class RatePromptOut(Model):
    item: RatePromptItem | None = None


class KeyIn(Model):
    key: TitleKey


class PersonHit(Model):
    id: int | None = None
    name: str | None = None
    photo: str | None = None
    known_for: str | None = None


class SearchMore(Model):
    library: bool = False
    tmdb: bool = False
    people: bool = False


class SearchOut(Model):
    library: list[Card]
    tmdb: list[Card]
    people: list[PersonHit]
    # Which section holds the best match, so it can be shown first.
    first: Literal["library", "tmdb"] = "library"
    # The search as it was run after fixing typos, when that changed it.
    corrected: str | None = None
    # It reads like a described plot and the AI is on: ask /search/ai too.
    ai: bool = False
    # The page asked for (each holds the matches before it too), and which
    # sections have more matches: the apps offer "Show more" (page + 1).
    page: int = 1
    more: SearchMore = Field(default_factory=lambda: SearchMore())
    # A section that stopped at Meilisearch's 1,000 hits, in plain words.
    note: str | None = None


class AiGuess(Model):
    card: Card
    why: str | None = None


class AiGuessOut(Model):
    enabled: bool
    matches: list[AiGuess]
    # When there are no matches: why, in plain words (the model didn't answer,
    # or had no guess). A failed or empty answer is kept a minute, not a day.
    note: str | None = None


class PersonOut(Model):
    id: int | None = None
    name: str | None = None
    photo: str | None = None
    biography: str | None = None
    birthday: str | None = None
    place_of_birth: str | None = None
    known_for: str | None = None
    items: list[Card]


class KeysIn(Model):
    keys: list[TitleKey] = Field(max_length=settings.batch_max_items)  # BATCH_MAX_ITEMS


class FilledTitle(Model):
    poster: str | None = None
    backdrop: str | None = None
    year: int | None = None
    title: str | None = None
    vote: float | None = None


class FillOut(Model):
    titles: dict[str, FilledTitle]


class SyncOut(Model):
    ok: bool = True
    counts: dict[str, int]
    # What wasn't done, in plain words (plays Jellyfin lists without a date).
    note: str | None = None


# ---------------------------------------------------------------------------
# Scrobbling (other apps telling Movie Night what was watched)
# ---------------------------------------------------------------------------


class AppTokenIn(Model):
    code: str = Field(min_length=10, max_length=200)
    verifier: str = Field(min_length=43, max_length=128)


class AppTokenOut(Model):
    ok: bool = True
    token: str
    user: str
    device_id: int


class DeviceOut(Model):
    id: int
    name: str
    created_at: str
    last_used_at: str | None = None
    # "app": a phone or tablet; "scrobble": Kodi or another TV app that only reports plays
    kind: Literal["app", "scrobble"] = "app"
    # When its sign-in runs out: unless it is used before ("unused": after
    # DEVICE_IDLE_DAYS without use), or for good ("age": DEVICE_MAX_DAYS after
    # it signed in).
    expires_at: str
    expires_because: Literal["unused", "age"]


class PairStartIn(Model):
    device: str = Field(default="Kodi", max_length=settings.reported_max_chars)  # REPORTED_MAX_CHARS


class PairStartOut(Model):
    code: str
    poll: str
    expires_in: int
    interval: int


class PairPollIn(Model):
    poll: str = Field(min_length=20, max_length=100)


class PairPollOut(Model):
    status: Literal["waiting", "ok"]
    token: str | None = None
    user: str | None = None  # the name to show
    account: str | None = None  # the username: unique, and it never changes


class PairConfirmIn(Model):
    code: str = Field(min_length=6, max_length=20)


class PairConfirmOut(Model):
    ok: bool = True
    device: str


class ScrobbleIn(Model):
    """A finished watch reported by another app (the Kodi add-on, etc.)."""

    media_type: Literal["movie", "episode"]
    tmdb_id: int | None = Field(default=None, gt=0, description="movie id, or the show's id for an episode")
    imdb_id: str | None = Field(default=None, pattern=r"^tt\d{5,10}$")
    tvdb_id: int | None = Field(default=None, gt=0, description="the show's TheTVDB id, for libraries scraped from TheTVDB")
    title: str | None = Field(default=None, max_length=settings.reported_max_chars)  # REPORTED_MAX_CHARS
    season: int | None = Field(default=None, ge=0, le=DB_INT)
    episode: int | None = Field(default=None, ge=0, le=DB_INT)
    watched_at: str | None = Field(default=None, max_length=40)
    source: str = Field(default="api", pattern=r"^[a-z0-9_-]{1,30}$")
    ref: str | None = Field(default=None, max_length=settings.reported_max_chars,
                            description="the sender's own id for this play, to avoid duplicates")


class JellyfinPluginItem(Model):
    """A film or episode as the Movie Night plugin for Jellyfin sends it."""

    id: str = Field(pattern=r"^[0-9a-fA-F-]{32,36}$")
    type: Literal["Movie", "Episode"]
    # REPORTED_MAX_CHARS for every text; whole numbers up to DB_INT (a
    # season numbered by its year, a day-long film: the plugin never retries
    # a refusal, so nothing real may be refused).
    name: str = Field(default="", max_length=settings.reported_max_chars)
    series_id: str | None = Field(default=None, pattern=r"^[0-9a-fA-F-]{32,36}$")
    series_name: str | None = Field(default=None, max_length=settings.reported_max_chars)
    season: int | None = Field(default=None, ge=0, le=DB_INT)
    episode: int | None = Field(default=None, ge=0, le=DB_INT)
    year: int | None = Field(default=None, ge=0, le=DB_INT)
    runtime_s: int | None = Field(default=None, ge=0, le=DB_INT)
    tmdb: str | None = Field(default=None, max_length=settings.reported_max_chars)
    imdb: str | None = Field(default=None, max_length=settings.reported_max_chars)
    tvdb: str | None = Field(default=None, max_length=settings.reported_max_chars)
    series_tmdb: str | None = Field(default=None, max_length=settings.reported_max_chars)
    series_imdb: str | None = Field(default=None, max_length=settings.reported_max_chars)
    series_tvdb: str | None = Field(default=None, max_length=settings.reported_max_chars)


class JellyfinPluginEvent(Model):
    event: Literal["start", "progress", "pause", "stop", "played", "unplayed"]
    user_id: str = Field(pattern=r"^[0-9a-fA-F-]{32,36}$", description="the Jellyfin user")
    item: JellyfinPluginItem
    position_s: int | None = Field(default=None, ge=0, le=DB_INT)
    played_to_completion: bool = False
    last_played: str | None = Field(default=None, max_length=40)
    client: str | None = Field(default=None, max_length=settings.reported_max_chars, description="the app, e.g. Kodi or Jellyfin Web")
    device: str | None = Field(default=None, max_length=settings.reported_max_chars)


class ScrobbleOut(Model):
    ok: bool = True
    recorded: bool
    detail: str | None = None
