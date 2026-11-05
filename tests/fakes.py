"""Stand-ins for TMDB, Jellyfin, Seerr, MDBList, Meilisearch, the AI model
and email, so tests never reach the internet. Titles are made up from their
ids: movie 603 is "Movie 603", show 1399 has 3 seasons of 10 episodes."""
from __future__ import annotations

import datetime as dt
from typing import Any

from app.clients.jellyfin import JellyfinClient


def movie(tid: int) -> dict[str, Any]:
    return {"id": tid, "media_type": "movie", "title": f"Movie {tid}", "original_title": f"Movie {tid}",
            "release_date": "2000-01-01", "poster_path": f"/p{tid}.jpg", "backdrop_path": f"/b{tid}.jpg",
            "overview": f"About movie {tid}.", "genre_ids": [35], "vote_average": 7.0, "vote_count": 100}


def show(tid: int) -> dict[str, Any]:
    return {"id": tid, "media_type": "tv", "name": f"Show {tid}", "original_name": f"Show {tid}",
            "first_air_date": "2011-04-17", "poster_path": f"/p{tid}.jpg", "backdrop_path": f"/b{tid}.jpg",
            "overview": f"About show {tid}.", "genre_ids": [18], "vote_average": 8.0, "vote_count": 100}


class FakeTMDB:
    enabled = True

    def __init__(self):
        self.calls: list[str] = []

    async def basic(self, media, tid):
        self.calls.append(f"basic {media} {tid}")
        if tid >= 9_000_000:
            return None  # "TMDB has no such title"
        return {**(show(tid) if media == "tv" else movie(tid)), "genres": [{"id": 35, "name": "Comedy"}], "runtime": 100}

    async def details(self, media, tid):
        base = await self.basic(media, tid)
        if base is None:
            return None
        extra = {"credits": {"cast": [{"id": 1, "name": "Actor One", "character": "Lead"}], "crew": [{"job": "Director", "name": "Dee Rector"}]},
                 "videos": {"results": [{"site": "YouTube", "key": "abc", "type": "Trailer", "official": True}]},
                 "watch/providers": {"results": {"US": {"link": "https://x", "flatrate": [{"provider_id": 8, "provider_name": "Netflix", "logo_path": "/n.png"}]}}},
                 "release_dates": {"results": [{"iso_3166_1": "US", "release_dates": [{"certification": "R"}]}]}}
        if media == "tv":
            extra.update({
                "seasons": [{"season_number": n, "name": f"Season {n}", "episode_count": 10, "air_date": f"201{n}-04-17", "poster_path": None}
                            for n in (1, 2, 3)],
                "number_of_seasons": 3, "number_of_episodes": 30, "status": "Ended", "created_by": [{"name": "Cree Ator"}],
                "last_episode_to_air": {"season_number": 3, "episode_number": 10, "name": "Finale", "air_date": "2013-06-09"},
                "content_ratings": {"results": [{"iso_3166_1": "US", "rating": "TV-MA"}]},
            })
        return {**base, **extra}

    async def season(self, tv_id, number):
        return {"name": f"Season {number}", "air_date": f"201{number}-04-17", "poster_path": None,
                "episodes": [{"episode_number": e, "name": f"Episode {e}", "air_date": f"201{number}-05-{e:02d}",
                              "still_path": None, "runtime": 55, "vote_average": 8.0} for e in range(1, 11)]}

    async def _page(self, media, page):
        start = 100 + page * 20
        return [movie(i) if media != "tv" else show(i) for i in range(start, start + 20)]

    async def discover(self, media, page=1, **filters):
        return await self._page(media, page)

    async def trending(self, media="all", window="week", page=1):
        return await self._page("movie" if media in ("all", "movie") else "tv", page)

    async def upcoming(self, media, page=1):
        return await self._page(media, page)

    async def recommendations(self, media, tid, page=1):
        return [movie(i) if media == "movie" else show(i) for i in range(tid + 1, tid + 6)] if page == 1 else []

    async def similar(self, media, tid, page=1):
        return []

    async def search_multi(self, query, page=1):
        return [movie(777), {"id": 5, "media_type": "person", "name": "Some Person", "profile_path": None}] if page == 1 else []

    async def search_multi_page(self, query, page=1):
        return await self.search_multi(query, page), 1

    async def discover_page(self, media, page=1, include_adult=False, **filters):
        year = filters.get("primary_release_year") or filters.get("first_air_date_year")
        if year != 1985:
            return {"results": [], "total_pages": 0}
        if page > 2:
            return {"results": [], "total_pages": 2}
        make = movie if media == "movie" else show
        results = [make(9000 + page * 10 + i) for i in range(3)]
        if include_adult and media == "movie" and page == 1:
            # What TMDB's default filter hides: a film it doesn't mark as
            # adult (kept) and one it does (dropped).
            results += [{**movie(22705), "title": "Cheeky", "vote_count": 300}, {**movie(613683), "adult": True}]
        return {"results": results, "total_pages": 2}

    async def search_movie(self, query, year=None):
        n = sum(ord(ch) for ch in query) % 1000 + 2000
        return [{**movie(n), "title": query, "release_date": f"{year or 2001}-01-01"}]

    async def collection(self, cid):
        return {"parts": [movie(603), movie(604)]}

    async def person(self, pid):
        return {"id": pid, "name": "Some Person", "biography": "", "combined_credits": {"cast": [movie(603)], "crew": []}}

    async def find_imdb(self, imdb_id):
        return {**movie(603), "media_type": "movie"}

    async def find_external(self, external_id, source):
        if source == "tvdb_id" and external_id == "121361":
            return {**show(1399), "media_type": "tv"}
        return None

    async def genres(self, media):
        return []


class FakeJellyfin(JellyfinClient):
    """URL building is the real client's; everything that would call the
    server is canned here."""

    def __init__(self):
        super().__init__("https://jf.example", "api-key", "https://jf.example")
        self.played_items: list[dict[str, Any]] = []
        self.marked: list[tuple[str, str, str]] = []
        self.qc_ready = False
        self.single_account = True

    async def server_id(self):
        return "5e1f" + "0" * 28

    @property
    def enabled(self):
        return True

    async def library_items(self, user_id):
        return [
            {"Id": "a" * 32, "Type": "Movie", "Name": "Movie 603", "ProductionYear": 2000, "ProviderIds": {"Tmdb": "603"},
             "DateCreated": "2026-09-01T00:00:00Z"},
            {"Id": "b" * 32, "Type": "Series", "Name": "Show 1399", "ProductionYear": 2011, "ProviderIds": {"Tmdb": "1399"},
             "DateCreated": "2026-09-02T00:00:00Z"},
        ]

    async def resume(self, user_id):
        return []

    async def next_up(self, user_id):
        return []

    async def played(self, user_id, item_types="Movie,Episode"):
        return self.played_items

    async def item(self, user_id, item_id, fields=""):
        return {"Id": item_id, "Type": "Movie", "Name": "Movie 603", "UserData": {"PlaybackPositionTicks": 0},
                "MediaSources": [{"Id": item_id, "Container": "mkv", "DefaultSubtitleStreamIndex": 2, "MediaStreams": [
                    {"Type": "Subtitle", "Index": 2, "Language": "eng", "DisplayTitle": "English", "IsExternal": False, "IsTextSubtitleStream": True},
                    {"Type": "Subtitle", "Index": 3, "Language": "eng", "DisplayTitle": "English (file)", "IsExternal": True, "IsTextSubtitleStream": True},
                ]}]}

    async def episodes(self, series_id, user_id, season=None):
        return [{"Id": f"ep{season}{e:02d}".ljust(32, "0"), "ParentIndexNumber": season, "IndexNumber": e, "LocationType": "FileSystem"}
                for e in range(1, 11)]

    async def mark_played(self, user_id, item_id, when=None):
        self.marked.append(("played", user_id, item_id))

    async def mark_unplayed(self, user_id, item_id):
        self.marked.append(("unplayed", user_id, item_id))

    async def quick_connect_enabled(self):
        return True

    async def quick_connect_initiate(self, device_id):
        return {"Code": "123456", "Secret": "secret-123456"}

    async def quick_connect_ready(self, secret, device_id):
        return self.qc_ready

    async def quick_connect_finish(self, secret, device_id):
        # One Jellyfin account per Movie Night person in the browser tests
        # (device ids are movienight-<id>); the API tests expect "f"*32.
        n = device_id.rsplit("-", 1)[-1]
        jf_id = "f" * 32 if self.single_account else n.rjust(32, "f")[-32:]
        return {"AccessToken": "user-token", "User": {"Id": jf_id, "Name": "alice-jf"}}

    async def sign_in(self, username, password, device_id):
        from app.clients.jellyfin import JellyfinError

        if password != "right":
            raise JellyfinError(401, "Wrong Jellyfin username or password")
        return {"AccessToken": "user-token", "User": {"Id": "e" * 32, "Name": username}}

    async def sign_out(self, token, device_id):
        return None

    async def image(self, item_id, kind="Primary", max_width=400):
        return None


class FakeSeerr:
    enabled = True
    dry_run = True

    async def media_page(self, take=500, skip=0):
        return {"results": [], "pageInfo": {"results": 0}}

    async def media_info(self, media, tid):
        return None

    async def requests_page(self, take=50, skip=0, filter_="all", requested_by=None):
        return {"results": []}

    async def users(self):
        return []

    async def import_jellyfin_user(self, jf_id):
        return [{"id": 7, "jellyfinUserId": jf_id}]

    async def create_request(self, media_id, media_type="movie", as_user=None, seasons=None):
        return {"dry_run": True, "mediaId": media_id, "mediaType": media_type}


class FakeMDBList:
    enabled = True

    async def top_lists(self):
        return [{"mdblist_id": 2194, "name": "Latest TV Shows", "slug": "latest-tv-shows", "user": "gary", "description": "",
                 "media": "tv", "item_count": 2, "likes": 10, "dynamic": True}]

    async def search_lists(self, query):
        return await self.top_lists()

    async def get_list(self, ref):
        return (await self.top_lists())[0]

    async def list_items(self, list_id):
        return [("tv", 1399, "Show 1399", 2011), ("movie", 603, "Movie 603", 2000)]

    async def media_info(self, media, tid):
        return {"score": 82, "ids": {"tmdb": tid, "trakt": 481},
                "ratings": [{"source": "imdb", "value": 8.7, "votes": 100}, {"source": "tomatoes", "value": 83, "url": "/m/x"},
                            {"source": "trakt", "value": 87, "votes": 60}]}

    async def search(self, query, limit=10):
        return [("movie", 22705, "Cheeky", 2000)]


class FakeMeili:
    """Library: movie 603 matches weakly. Catalog: whatever `catalog` holds
    (tests fill it), else nothing."""

    def __init__(self):
        self.catalog: list[dict[str, Any]] = []
        self.sent: list[dict[str, Any]] = []
        self.queries: list[str] = []
        self.deleted: list[str] = []

    async def ensure_index(self):
        return None

    async def ensure_catalog_index(self):
        return None

    async def indexed_texts(self):
        return {}

    async def field_values(self, field, index):
        return {d["id"]: d.get(field) for d in self.catalog}

    async def replace_documents(self, docs, index="library"):
        if index == "catalog":
            self.sent.extend(docs)
        return 1

    async def delete_documents(self, ids, index="library"):
        self.deleted.extend(ids)

    async def search(self, query, semantic_ratio=0.8, limit=20, media_type=None, index="library", scores=False):
        if index == "catalog":
            self.queries.append(query)
            return [d for d in self.catalog if media_type in (None, d["media_type"])][:limit]
        return [{"id": "a" * 32, "tmdb_id": 603, "media_type": "movie", "title": "Movie 603", "_rankingScore": 0.62}]


class FakeAI:
    enabled = False
    model = "test-model"

    async def chat_json(self, system, user, attempts=2):
        raise AssertionError("no AI in tests")


class FakeSearchAI:
    """The search AI: names 'Movie Named' for anything, and counts calls."""
    enabled = True
    model = "test-search-model"

    def __init__(self):
        self.calls = 0

    async def chat_json(self, system, user, attempts=2):
        self.calls += 1
        return {"matches": [{"title": "Movie Named", "year": 1985, "type": "movie", "why": "kids, a map, the treasure"},
                            {"title": "", "type": "movie"}]}


class FakeMailer:
    enabled = True

    def __init__(self):
        self.sent: list[tuple[str, str]] = []
        self.headers: list[dict[str, str]] = []

    async def send(self, to, subject, html, text, headers=None):
        self.sent.append((to, subject))
        self.headers.append(dict(headers or {}))
        return "<id@test>"


def now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)
