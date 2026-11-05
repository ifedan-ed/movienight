"""The owner's rule: no usage caps, and nothing cut without a word. Counts
that used to stop ("Not now"s, Seerr's media, other people's ratings,
digests, requests, pages) now go on; the limits that stay (docs/limits.md, "Technical
limits") are settings and say so in plain words. The owner keeps one cap
("Limits the owner keeps"): players of your own (20, 30 ticked); it is
checked here too."""
import datetime as dt

from sqlalchemy import select

from app.core.db import session
from app.models import Digest, User, UserRating
from app.services import history, state


async def _uid(name: str = "alice") -> int:
    async with session() as db:
        return await db.scalar(select(User.id).where(User.username == name))


async def test_players_of_your_own_stop_at_20_and_30_ticked(alice):
    # Limits the owner keeps (docs/limits.md, "Limits the owner keeps").
    custom = [{"name": f"Player {n}", "template": f"p{n}-{{url_raw}}"} for n in range(1, 22)]
    r = await alice.put("/api/v1/me/players", json={"custom": custom})
    assert r.status_code == 422 and r.json()["detail"] == "You can have up to 20 players of your own."
    r = await alice.put("/api/v1/me/players", json={"custom": custom[:20], "enabled": [f"custom{n}" for n in range(1, 21)] + ["vlc"] * 11})
    assert r.status_code == 422 and r.json()["detail"] == "Up to 30 players can be ticked."
    got = await alice.put("/api/v1/me/players", json={"custom": custom[:20], "enabled": ["vlc"] + [f"custom{n}" for n in range(1, 21)]})
    assert got.status_code == 200
    assert len([p for p in got.json()["players"] if not p["builtin"]]) == 20


async def test_every_not_now_is_remembered(alice):
    await alice.get("/api/v1/me")
    uid = await _uid()
    async with session() as db:
        for n in range(1, 602):
            await history.dismiss_rate_prompt(db, uid, f"movie:{n}")
        from app.services import users
        kept = await users.get_setting(db, uid, history.RATE_DISMISSED_KEY)
    assert len(kept) == 601 and kept[0] == "movie:1"


async def test_every_one_of_seerrs_media_is_read(up):
    total = 12_500

    async def media_page(take=500, skip=0):
        return {"results": [{"tmdbId": n, "mediaType": "movie", "status": 5} for n in range(skip + 1, min(skip + take, total) + 1)],
                "pageInfo": {"results": total}}

    up.seerr.media_page = media_page
    got = await state.seerr_media_map(up.seerr, force=True)
    assert len(got) == total and ("movie", total) in got


async def test_everyones_ratings_on_a_title_are_listed(alice):
    await alice.get("/api/v1/me")
    async with session() as db:
        for n in range(60):
            u = User(username=f"viewer{n}", display_name=f"Viewer {n}", is_admin=False)
            db.add(u)
            await db.flush()
            db.add(UserRating(user_id=u.id, media_type="movie", tmdb_id=603, rating=7,
                              rated_at=dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc) + dt.timedelta(hours=n)))
        await db.commit()
    community = (await alice.get("/api/v1/title/movie/603")).json()["community"]
    assert community["count"] == 60 and len(community["others"]) == 60


async def test_every_digest_is_listed(alice):
    await alice.get("/api/v1/me")
    uid = await _uid()
    start = dt.datetime(2025, 1, 6, tzinfo=dt.timezone.utc)
    async with session() as db:
        db.add_all([Digest(user_id=uid, kind="week", period_start=start + dt.timedelta(weeks=n), period_end=start + dt.timedelta(weeks=n + 1),
                           subject=f"Week {n}", html="<p>x</p>", data_json="{}") for n in range(30)])
        await db.commit()
    assert len((await alice.get("/api/v1/me/digests")).json()) == 30


async def test_requests_go_page_by_page_to_the_last_one(alice, up):
    total = 95

    async def requests_page(take=50, skip=0, filter_="all", requested_by=None):
        rows = [{"media": {"tmdbId": 1000 + n, "mediaType": "movie", "status": 2}, "status": 1, "createdAt": "2026-10-01T00:00:00Z"}
                for n in range(skip, min(skip + take, total))]
        if skip == 0:
            rows[0] = {"media": {"mediaType": "movie", "status": 2}, "status": 1}  # one Seerr has no TMDB id for
        return {"results": rows, "pageInfo": {"results": total}}

    up.seerr.requests_page = requests_page
    first = (await alice.get("/api/v1/requests")).json()
    assert first["total"] == 95 and first["more"] is True and len(first["items"]) == 39 and first["not_shown"] == 1
    last = (await alice.get("/api/v1/requests", params={"page": 3})).json()
    assert last["more"] is False and len(last["items"]) == 15


async def test_pages_go_on_past_the_old_page_caps(alice):
    assert (await alice.get("/api/v1/history", params={"page": 20_000})).status_code == 200
    assert (await alice.get("/api/v1/collection/watchlist", params={"page": 501})).status_code == 200
    assert (await alice.get("/api/v1/browse", params={"key": "watchlist", "page": 501})).status_code == 200


async def test_a_technical_limit_says_so_in_plain_words(alice, monkeypatch):
    me = (await alice.get("/api/v1/me")).json()
    assert me["limits"] == {"text_max_chars": 5000, "search_max_chars": 200, "name_max_chars": 300,
                            "reported_max_chars": 500, "batch_max_items": 1000}
    r = await alice.post("/api/v1/title/movie/603/rating", json={"rating": 8, "review": "x" * 5001})
    assert r.status_code == 422 and r.json()["detail"] == "Your comment is too long: at most 5,000 characters."
    r = await alice.get("/api/v1/search", params={"q": "y" * 201})
    assert r.status_code == 422 and r.json()["detail"] == "A search is too long: at most 200 characters."


def test_every_request_shape_is_a_setting():
    # Each request-shape limit reads its setting (docs/limits.md, "Technical
    # limits"): NAME_MAX_CHARS, REPORTED_MAX_CHARS, BATCH_MAX_ITEMS,
    # TEXT_MAX_CHARS and SEARCH_MAX_CHARS. None is a number of its own.
    from app.api import schemas as s
    from app.core.config import settings

    def bound(model, field):
        return next(m.max_length for m in model.model_fields[field].metadata if hasattr(m, "max_length"))
    assert settings.name_max_chars == 300 and settings.reported_max_chars == 500 and settings.batch_max_items == 1000
    for model, field in ((s.ListIn, "name"), (s.SmartIn, "name"), (s.ListPatch, "name"), (s.SmartFilters, "sort"),
                         (s.FollowIn, "ref"), (s.CustomPlayerIn, "name"), (s.PasswordLinkIn, "username")):
        assert bound(model, field) == settings.name_max_chars, (model, field)
    for model, field in ((s.ScrobbleIn, "title"), (s.ScrobbleIn, "ref"), (s.JellyfinPluginItem, "name"),
                         (s.JellyfinPluginItem, "series_name"), (s.JellyfinPluginItem, "tmdb"),
                         (s.JellyfinPluginEvent, "client"), (s.JellyfinPluginEvent, "device"), (s.PairStartIn, "device")):
        assert bound(model, field) == settings.reported_max_chars, (model, field)
    for model, field in ((s.IdsIn, "ids"), (s.EpisodesIn, "episodes"), (s.KeysIn, "keys"), (s.HomeConfigIn, "rows"),
                         (s.RequestIn, "seasons")):
        assert bound(model, field) == settings.batch_max_items, (model, field)
    for model, field in ((s.RatingIn, "review"), (s.CustomPlayerIn, "template"), (s.PasswordLinkIn, "password")):
        assert bound(model, field) == settings.text_max_chars, (model, field)


async def test_a_name_too_long_is_refused_in_plain_words(alice):
    r = await alice.post("/api/v1/lists", json={"name": "n" * 301})
    assert r.status_code == 422 and r.json()["detail"] == "The name is too long: at most 300 characters."
    assert (await alice.post("/api/v1/lists", json={"name": "n" * 300})).status_code == 200


async def test_a_season_numbered_by_its_year_and_a_day_long_film_are_not_refused(alice, up):
    # The plugin never retries a refusal, so a real value refused is a play lost.
    from tests.test_jellyfin_plugin import EPISODE, MOVIE, jf_user, send
    from tests.test_shows_play_sync import link_jellyfin
    await link_jellyfin(alice, up)
    uid = await jf_user()
    assert (await send("start", {**EPISODE, "season": 2023, "episode": 20230115}, uid, position_s=0)).status_code == 202
    long_film = {**MOVIE, "runtime_s": 3_000_000, "name": "A" * 500}
    assert (await send("progress", long_film, uid, position_s=200_000, client="C" * 300, device="D" * 300)).status_code == 202
    r = (await alice.get("/api/v1/title/movie/603")).json()["resume"]
    assert r["position_s"] == 200_000 and r["client"] == "C" * 300  # kept whole, not cut to a column's width
    assert (await alice.get("/api/v1/title/tv/1399/season/2023")).status_code != 422


async def test_a_long_reported_title_without_a_ref_is_recorded_once(alice):
    token = (await alice.post("/api/v1/me/scrobble-token")).json()["token"]
    from tests.conftest import client
    title = "T" * 500  # the made-up reference holds the title: longer than the 100 kept, so it is hashed
    async with client(csrf=False, Authorization=f"Bearer {token}") as kodi:
        body = {"media_type": "movie", "title": title, "watched_at": "2026-01-02T20:00:00Z"}
        first = await kodi.post("/api/v1/scrobble", json=body)
        again = await kodi.post("/api/v1/scrobble", json=body)
    assert first.status_code == 200 and first.json()["recorded"] is True
    assert again.json() == {"ok": True, "recorded": False, "detail": "already recorded"}
    hist = (await alice.get("/api/v1/history")).json()["items"]
    assert hist[0]["title"] == title


async def test_continue_watching_shows_every_item(alice, up, monkeypatch):
    # The old 20 in progress and 20 next up are gone: every one, page by page.
    import httpx
    from sqlalchemy import select

    import importlib

    from app import clients
    from app.core.db import session
    from app.models import User
    from app.services import rows
    jellyfin_client = importlib.import_module("app.clients.jellyfin")  # the module, not clients.jellyfin()
    monkeypatch.setattr(jellyfin_client, "CONTINUE_PAGE", 10)
    resume = [{"Id": f"{n:032x}", "Type": "Movie", "Name": f"Movie {n}", "ProviderIds": {"Tmdb": str(n)},
               "UserData": {"PlayedPercentage": 40}} for n in range(1, 27)]
    next_up = [{"Id": f"{1000 + n:032x}", "Type": "Episode", "Name": f"Episode {n}", "SeriesId": f"{5000 + n:032x}",
                "SeriesName": f"Show {n}", "ParentIndexNumber": 1, "IndexNumber": 2} for n in range(30)]

    def handler(request):
        p = request.url.params
        start, limit = int(p["StartIndex"]), int(p["Limit"])
        items = resume if request.url.path.endswith("/Items/Resume") else next_up
        return httpx.Response(200, json={"Items": items[start:start + limit], "TotalRecordCount": len(items)})
    jf = jellyfin_client.JellyfinClient("http://jf.test", "key", client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    clients.set_overrides(**{**clients._overrides, "jellyfin": jf})
    await alice.get("/api/v1/me")
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        user.jellyfin_user_id = "f" * 32
        await db.commit()
        items = await rows.continue_row(db, user, "home", None)
    assert len(items) == 26 + 30
    assert sum(1 for i in items if i["title"].startswith("Movie ")) == 26


async def test_because_you_watched_looks_past_a_long_binge(alice):
    # 700 episodes of one show, newest; 20 films before them. The old read of
    # the 600 newest history rows found one title; all 15 seeds are found now.
    import datetime as dt

    from sqlalchemy import select

    from app.core.db import session
    from app.models import HistoryEntry, User
    from app.services import recommend
    await alice.get("/api/v1/me")
    start = dt.datetime(2025, 1, 1, tzinfo=dt.timezone.utc)
    async with session() as db:
        uid = await db.scalar(select(User.id).where(User.username == "alice"))
        db.add_all([HistoryEntry(user_id=uid, media_type="movie", tmdb_id=900 + n, title=f"Movie {900 + n}",
                                 watched_at=start + dt.timedelta(hours=n)) for n in range(20)])
        db.add_all([HistoryEntry(user_id=uid, media_type="episode", tmdb_id=1399, season=1 + n // 10, episode=n % 10 + 1,
                                 title="Show 1399", watched_at=start + dt.timedelta(days=30, minutes=n)) for n in range(700)])
        await db.commit()
        seeds = await recommend.recent_seeds(db, uid, None)
    assert len(seeds) == 15 and seeds[0][:2] == ("tv", 1399) and seeds[1][:2] == ("movie", 919)


async def test_the_rate_prompt_looks_at_every_recent_watch(alice):
    # 25 films watched and rated since; one older one this fortnight unrated.
    import datetime as dt

    from sqlalchemy import select

    from app.core.db import session
    from app.models import HistoryEntry, User, UserRating
    await alice.get("/api/v1/me")
    now = dt.datetime.now(dt.timezone.utc)
    async with session() as db:
        uid = await db.scalar(select(User.id).where(User.username == "alice"))
        db.add(HistoryEntry(user_id=uid, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=now - dt.timedelta(days=10)))
        for n in range(25):
            db.add(HistoryEntry(user_id=uid, media_type="movie", tmdb_id=1000 + n, title=f"Movie {1000 + n}",
                                watched_at=now - dt.timedelta(days=5, minutes=n)))
            db.add(UserRating(user_id=uid, media_type="movie", tmdb_id=1000 + n, rating=7))
        await db.commit()
    item = (await alice.get("/api/v1/rate-prompt")).json()["item"]
    assert item is not None and item["tmdb_id"] == 603


async def test_every_stale_title_is_refreshed_not_400_a_night(up):
    # 1,000 stale titles (and 5 TMDB no longer has): all of them in one run,
    # 400 at a time; the ones TMDB can't send are counted and passed by.
    import datetime as dt

    from sqlalchemy import func, select

    from app.core.db import session
    from app.models import Title
    from app.services import titles
    old = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=40)
    async with session() as db:
        ids = list(range(1, 1001)) + list(range(9_000_000, 9_000_005))
        db.add_all([Title(key=f"movie:{n}", media_type="movie", tmdb_id=n, title=f"Movie {n}", detailed=True, fetched_at=old)
                    for n in ids])
        await db.commit()
        assert await titles.refresh_stale(db, up.tmdb) == (1000, 5)
        assert await db.scalar(select(func.count()).select_from(Title).where(Title.fetched_at == old)) == 5
        assert await titles.refresh_stale(db, up.tmdb) == (0, 5)  # the next run: only those again


async def test_a_tmdb_grid_that_reaches_tmdbs_last_page_says_so(alice):
    # TMDB serves 500 pages of a list (its own limit): the grid stops there
    # and says why, instead of asking for page 501 and ending without a word.
    before = (await alice.get("/api/v1/browse", params={"key": "popular", "page": 499})).json()
    assert before["more"] is True and before["note"] is None
    last = (await alice.get("/api/v1/browse", params={"key": "popular", "page": 500})).json()
    assert last["items"] and last["more"] is False and last["note"].startswith("TMDB lists only the first 10,000 titles")
    discover = (await alice.get("/api/v1/discover", params={"page": 500})).json()
    assert discover["more"] is False and "10,000" in discover["note"]
    mine = (await alice.get("/api/v1/browse", params={"key": "watchlist", "page": 500})).json()
    assert mine["note"] is None  # Movie Night's own lists have no such end


async def test_a_tmdb_page_that_doesnt_come_says_so_instead_of_ending_the_grid(alice, up):
    # A TMDB error on page 3 used to answer an empty page: the grid ended
    # there as if the list had, without a word. Now the grid's next page says
    # why (a 502 with a plain sentence), a Home row says so under its title,
    # and a page TMDB sent earlier still comes from the cache.
    from app.services import rows
    assert (await alice.get("/api/v1/browse", params={"key": "popular", "page": 2})).json()["items"]
    real = {name: getattr(up.tmdb, name) for name in ("discover", "trending", "upcoming", "recommendations")}

    async def down(*args, **kwargs):
        raise RuntimeError("TMDB answered HTTP 503")

    for name in real:
        setattr(up.tmdb, name, down)
    for params in ({"key": "popular", "page": 3}, {"key": "trending", "page": 2}, {"key": "upcoming"},
                   {"key": "recs:movie:603", "page": 2}):
        got = await alice.get("/api/v1/browse", params=params)
        assert got.status_code == 502 and got.json()["detail"] == rows.TMDB_DOWN, params
    assert (await alice.get("/api/v1/discover", params={"page": 4})).json()["detail"] == rows.TMDB_DOWN
    smart = await alice.post("/api/v1/lists/smart", json={"name": "Old films", "filters": {"media": "movie", "year_to": 1960}})
    got = await alice.get(f"/api/v1/lists/{smart.json()['id']}")
    assert got.status_code == 502 and got.json()["detail"] == rows.TMDB_DOWN
    row = (await alice.get("/api/v1/rows/popular")).json()
    assert row == {"rows": [], "note": rows.TMDB_DOWN_ROW}
    assert (await alice.get("/api/v1/hero")).status_code == 200  # the banner goes on with the library alone
    # A page TMDB sent before is still shown from the cache.
    kept = await alice.get("/api/v1/browse", params={"key": "popular", "page": 2})
    assert kept.status_code == 200 and kept.json()["items"]
    # A title's page leaves "More like this" out, as before.
    assert (await alice.get("/api/v1/title/movie/603/related")).json()["recommendations"] == []


async def test_a_title_page_lists_every_service_cast_member_and_credit(alice, up):
    # The old page kept the first 20 of the cast and 3 directors, writers,
    # studios and languages (and the website drew 6 services and 16 actors).
    async def details(media, tid):
        return {**(await up.tmdb.basic(media, tid)),
                "credits": {"cast": [{"id": n, "name": f"Actor {n}", "character": f"Part {n}"} for n in range(1, 41)],
                            "crew": [{"job": "Director", "name": f"Director {n}"} for n in range(5)]
                            + [{"department": "Writing", "name": f"Writer {n}"} for n in range(6)]},
                "production_companies": [{"name": f"Studio {n}"} for n in range(7)],
                "spoken_languages": [{"english_name": f"Language {n}"} for n in range(5)],
                "watch/providers": {"results": {"US": {"flatrate": [{"provider_id": n, "provider_name": f"Service {n}"}
                                                                    for n in range(9)]}}}}
    up.tmdb.details = details
    d = (await alice.get("/api/v1/title/movie/603")).json()
    assert len(d["cast"]) == 40 and len(d["directors"]) == 5 and len(d["writers"]) == 6
    assert len(d["studios"]) == 7 and len(d["languages"]) == 5 and len(d["providers"]["stream"]) == 9
