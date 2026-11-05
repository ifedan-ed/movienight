"""Movie Night keeps going when an outside service doesn't: IMDb ratings come
from IMDb's own file kept here, MDBList's scores, followed lists and lists
page are answered from what is kept here, and search goes on without
MDBList. Search also asks TMDB for every title, hidden ones included."""
import gzip
import importlib

import httpx
import pytest
from sqlalchemy import text, update

from app.clients.mdblist import MDBListClient, MDBListError, MDBListRefused
from app.clients.tmdb import TMDBClient
from app.core.db import engine, session
from app.models import Title
from app.services import imdb, lists

RATINGS = "tconst\taverageRating\tnumVotes\ntt0000603\t7.9\t123456\ntt0001399\t9.2\t2400000\n"


async def chunks(data: bytes, size: int = 7):
    for i in range(0, len(data), size):
        yield data[i:i + size]


async def load(tsv: str, size: int = 7) -> int:
    return await imdb.load(chunks(gzip.compress(tsv.encode()), size))


async def ratings() -> dict:
    async with engine.connect() as conn:
        rows = await conn.execute(text("SELECT tconst, rating, votes FROM imdb_ratings"))
        return {t: (r, v) for t, r, v in rows}


async def down(*args, **kwargs):
    raise MDBListError(503, "MDBList didn't answer")


async def stale_scores():
    """A week on: the kept scores are due to be asked for again."""
    async with session() as db:
        await db.execute(text("UPDATE title_extras SET fetched_at = now() - interval '8 days'"))
        await db.commit()


# -- IMDb's own ratings -----------------------------------------------------------


async def test_imdb_file_loads_and_replaces_the_one_before():
    assert await load(RATINGS) == 2
    assert (await ratings())["tt0000603"] == (7.9, 123456)
    assert await load(RATINGS.replace("7.9\t123456", "8.0\t123999")) == 2  # the next night's file
    assert (await ratings())["tt0000603"] == (8.0, 123999)
    async with engine.connect() as conn:
        pk = await conn.scalar(text("SELECT conname FROM pg_constraint WHERE conrelid = 'imdb_ratings'::regclass AND contype = 'p'"))
        assert pk == "imdb_ratings_pkey"
        assert await conn.scalar(text("SELECT to_regclass('imdb_ratings_load')")) is None


@pytest.mark.parametrize("bad", ["not imdb's", "cut short", "far fewer titles", "empty"])
async def test_a_broken_imdb_file_keeps_the_ratings_already_here(bad):
    many = "tconst\taverageRating\tnumVotes\n" + "".join(f"tt{n:07d}\t7.0\t{n}\n" for n in range(1, 101))
    await load(many)
    data = {"not imdb's": gzip.compress(b"id\trating\tvotes\ntt1\t1\t1\n"),
            "cut short": gzip.compress(many.encode())[:-30],
            "far fewer titles": gzip.compress(RATINGS.encode()),
            "empty": gzip.compress(b"")}[bad]
    with pytest.raises(imdb.ImdbFileError):
        await imdb.load(chunks(data, 64))
    assert len(await ratings()) == 100


async def test_imdb_file_is_downloaded_from_imdb():
    seen = []

    def handler(request):
        seen.append(str(request.url))
        return httpx.Response(200, content=gzip.compress(RATINGS.encode()))

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as http:
        assert await imdb.refresh(http) == 2
    assert seen == ["https://datasets.imdbws.com/title.ratings.tsv.gz"] and await imdb.loaded()


async def test_title_page_shows_imdbs_own_rating(alice, up):
    plain = up.tmdb.details

    async def details(media, tid):
        return {**await plain(media, tid), "imdb_id": f"tt{tid:07d}"}

    up.tmdb.details = details
    await load(RATINGS)
    d = (await alice.get("/api/v1/title/movie/603")).json()
    assert d["scores"]["imdb_rating"] == 7.9 and d["scores"]["imdb_votes"] == 123456
    s = (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]
    assert s["imdb_rating"] == 7.9 and s["rt_critics"] == 83  # IMDb's own over MDBList's 8.7; the rest from MDBList


async def test_imdbs_own_rating_needs_no_mdblist(alice, up):
    up.mdblist.media_info = down
    await alice.get("/api/v1/title/movie/603")
    async with session() as db:
        await db.execute(update(Title).where(Title.key == "movie:603").values(imdb_id="tt0000603"))
        await db.commit()
    await load(RATINGS)
    s = (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]
    assert s["imdb_rating"] == 7.9 and s["rt_critics"] is None


# -- MDBList scores ---------------------------------------------------------------


async def test_when_mdblist_is_down_the_kept_scores_show(alice, up):
    assert (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]["rt_critics"] == 83
    await stale_scores()
    up.mdblist.media_info = down
    s = (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]
    assert s["rt_critics"] == 83 and s["imdb_rating"] == 8.7


async def test_a_score_mdblist_leaves_out_is_kept(alice, up):
    await alice.get("/api/v1/title/movie/603/scores")
    await stale_scores()

    async def partial(media, tid):
        return {"score": 80, "ratings": [{"source": "imdb", "value": 8.8, "votes": 120}]}

    up.mdblist.media_info = partial
    s = (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]
    assert s["imdb_rating"] == 8.8 and s["mdblist_score"] == 80
    assert s["rt_critics"] == 83 and s["rt_url"] == "https://www.rottentomatoes.com/m/x"


# -- MDBList lists ----------------------------------------------------------------


async def test_a_followed_list_keeps_its_titles_when_mdblist_cant_send_them(alice, up):
    out = (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()
    assert out["count"] == 2

    async def empty(list_id):
        return []

    up.mdblist.list_items = empty
    r = await alice.post(f"/api/v1/lists/{out['id']}/sync")
    assert r.status_code == 502 and "kept" in r.json()["detail"]
    up.mdblist.list_items = down
    assert (await alice.post(f"/api/v1/lists/{out['id']}/sync")).status_code == 502
    async with session() as db:  # the nightly sync
        assert await lists.sync_all_mdblist(db, up.mdblist) == 0
    assert len((await alice.get(f"/api/v1/lists/{out['id']}")).json()["items"]) == 2


async def test_the_mdblist_lists_page_works_from_the_copy_while_mdblist_is_down(alice, up):
    # Seen once while MDBList answers: the top lists and one list's titles are kept.
    assert (await alice.get("/api/v1/mdblist/lists")).json()["results"][0]["name"] == "Latest TV Shows"
    assert (await alice.get("/api/v1/mdblist/lists/2194")).json()["count"] == 2
    from app.core import cache
    cache._store.clear()
    for name in ("top_lists", "search_lists", "get_list", "list_items"):
        setattr(up.mdblist, name, down)
    top = (await alice.get("/api/v1/mdblist/lists")).json()
    assert top["mode"] == "browse" and top["results"][0]["mdblist_id"] == 2194 and not top.get("error")
    found = (await alice.get("/api/v1/mdblist/lists", params={"q": "latest tv"})).json()
    assert [x["mdblist_id"] for x in found["results"]] == [2194]
    link = (await alice.get("/api/v1/mdblist/lists", params={"q": "https://mdblist.com/lists/gary/latest-tv-shows"})).json()
    assert link["mode"] == "link" and link["results"][0]["mdblist_id"] == 2194
    preview = (await alice.get("/api/v1/mdblist/lists/2194")).json()
    assert preview["count"] == 2 and {c["key"] for c in preview["items"]} == {"tv:1399", "movie:603"}
    followed = (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()
    assert followed["count"] == 2
    # A list never seen while MDBList answered still says MDBList didn't answer.
    assert (await alice.get("/api/v1/mdblist/lists/999")).status_code == 502


async def test_without_an_mdblist_key_the_saved_lists_still_show(alice, up):
    assert (await alice.get("/api/v1/mdblist/lists")).json()["results"]  # seen once: the copy is kept
    assert (await alice.get("/api/v1/mdblist/lists/2194")).json()["count"] == 2
    from app.core import cache
    cache._store.clear()
    up.mdblist.enabled = False  # the key is gone: the real client refuses every call
    for name in ("top_lists", "search_lists", "get_list", "list_items"):
        setattr(up.mdblist, name, down)
    top = (await alice.get("/api/v1/mdblist/lists")).json()
    assert top["results"][0]["mdblist_id"] == 2194 and top["enabled"] is True
    assert (await alice.get("/api/v1/mdblist/lists/2194")).json()["count"] == 2
    assert (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()["count"] == 2


async def test_without_a_key_or_a_copy_the_page_is_empty_with_no_notice(alice, up):
    up.mdblist.enabled = False
    r = (await alice.get("/api/v1/mdblist/lists")).json()
    assert r["results"] == [] and r["error"] is None and r["enabled"] is True
    assert (await alice.get("/api/v1/mdblist/lists/2194")).status_code == 503


async def test_the_nightly_copy_takes_every_top_list_and_its_titles(up):
    from app.services import mdblist_copy
    async with session() as db:
        assert await mdblist_copy.refresh(db, up.mdblist) == 1
        up.mdblist.list_items = down
        assert await mdblist_copy.WithCopy(up.mdblist, db).list_items(2194) == [("tv", 1399, "Show 1399", 2011),
                                                                                ("movie", 603, "Movie 603", 2000)]

        async def refused():
            raise MDBListRefused("MDBList refused")

        up.mdblist.top_lists = refused
        assert await mdblist_copy.refresh(db, up.mdblist) == 0  # stops; the copy stays
        assert (await mdblist_copy.WithCopy(up.mdblist, db).top_lists())[0]["mdblist_id"] == 2194


# -- Search -----------------------------------------------------------------------


async def test_search_goes_on_when_mdblist_is_down(alice, up):
    up.mdblist.search = down
    r = await alice.get("/api/v1/search", params={"q": "cheeky"})
    assert r.status_code == 200 and "movie:777" in [c["key"] for c in r.json()["tmdb"]]


async def test_mdblist_is_left_alone_for_a_while_after_it_fails_to_answer(monkeypatch):
    # the module, not app.clients.mdblist() (the factory of the same name)
    monkeypatch.setattr(importlib.import_module("app.clients.mdblist"), "_down_until", 0.0)
    calls = []

    def handler(request):
        calls.append(request.url.path)
        raise httpx.ConnectError("no route", request=request)

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as http:
        mdb = MDBListClient("key", client=http)
        with pytest.raises(MDBListError):
            await mdb.media_info("movie", 603)
        with pytest.raises(MDBListError):
            await mdb.search("cheeky")
    assert calls == ["/tmdb/movie/603"]  # the second call didn't wait on MDBList


async def test_tmdb_asks_for_every_title(monkeypatch):
    def handler(request):
        assert request.url.params["include_adult"] == "true"
        return httpx.Response(200, json={"results": [
            {"id": 42565, "media_type": "tv", "name": "Cheeky Angel", "adult": False},
            {"id": 22705, "media_type": "movie", "title": "Cheeky", "adult": False},  # hidden by TMDB's own filter
            {"id": 613683, "media_type": "movie", "title": "Cheeky", "adult": True}]})

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as http:
        tmdb = TMDBClient("key", client=http)
        assert [r["id"] for r in await tmdb.search_multi("cheeky")] == [42565, 22705, 613683]
        # Home, Discover and smart lists (discover) and Coming soon ask the same way.
        assert 22705 in [r["id"] for r in await tmdb.discover("movie", with_genres="10749")]
        assert 22705 in [r["id"] for r in await tmdb.upcoming("movie")]
        hiding = TMDBClient("key", client=http, hide_adult=True)  # HIDE_ADULT_TITLES=true
        assert [r["id"] for r in await hiding.search_multi("cheeky")] == [42565, 22705]
        assert 613683 not in [r["id"] for r in await hiding.discover("movie")]


# -- No allowance of Movie Night's own; MDBList's own refusals handled ------------


def mdblist_answering(handler) -> MDBListClient:
    return MDBListClient("key", client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))


async def test_mdblist_has_no_allowance_of_movie_nights_own(monkeypatch):
    """The old MDBLIST_DAILY_BUDGET (900 a day) is gone: every call is made."""
    from app.core.config import settings
    assert not hasattr(settings, "mdblist_daily_budget")
    calls = []

    def handler(request):
        calls.append(request.url.path)
        return httpx.Response(200, json={"score": 80, "ratings": []})

    mdb = mdblist_answering(handler)
    for n in range(1, 1_201):
        assert (await mdb.media_info("movie", n))["score"] == 80
    assert len(calls) == 1_200


@pytest.mark.parametrize("answer", [httpx.Response(429, text="Too Many Requests"),
                                    httpx.Response(403, json={"error": "API Limit Reached!"})])
async def test_mdblist_refusing_is_said_plainly_remembered_for_admins_and_left_alone(answer, owner, alice):
    mdblist_module = importlib.import_module("app.clients.mdblist")
    calls = []

    def refuse(request):
        calls.append(request.url.path)
        return answer

    with pytest.raises(MDBListRefused) as err:
        await mdblist_answering(refuse).media_info("movie", 603)
    assert "refused" in str(err.value) and "its own limit" in str(err.value)
    with pytest.raises(MDBListRefused):  # left alone meanwhile: no second request
        await mdblist_answering(refuse).media_info("movie", 604)
    assert len(calls) == 1
    notices = (await owner.get("/api/v1/me")).json()["admin_notices"]
    assert any("MDBList refused" in n for n in notices)
    assert (await alice.get("/api/v1/me")).json()["admin_notices"] == []
    # Once MDBList answers again, the notice goes.
    mdblist_module._down_until = 0.0
    await mdblist_answering(lambda request: httpx.Response(200, json={"score": 1})).media_info("movie", 605)
    assert mdblist_module.refusal() is None
    assert not any("MDBList" in n for n in (await owner.get("/api/v1/me")).json()["admin_notices"])


async def test_when_mdblist_refuses_the_title_page_keeps_its_scores_and_says_why(alice, up):
    assert (await alice.get("/api/v1/title/movie/603/scores")).json()["note"] is None
    await stale_scores()

    async def refused(media, tid):
        raise MDBListRefused("MDBList refused")

    up.mdblist.media_info = refused
    got = (await alice.get("/api/v1/title/movie/603/scores")).json()
    assert got["scores"]["rt_critics"] == 83  # the last known scores
    assert "refused" in got["note"] and "showing the ones kept from" in got["note"]
    fresh = (await alice.get("/api/v1/title/movie/604/scores")).json()  # never fetched before
    assert "refused" in fresh["note"] and "none kept" in fresh["note"]
    up.mdblist.media_info = down
    assert "didn't answer" in (await alice.get("/api/v1/title/movie/605/scores")).json()["note"]


async def test_a_list_is_read_whole_past_the_old_3000_cap():
    total = 4_500

    def handler(request):
        offset = int(request.url.params.get("offset") or request.url.params.get("cursor") or 0)
        rows = [{"ids": {"tmdb": n}, "mediatype": "movie", "title": f"Movie {n}"} for n in range(offset + 1, min(offset + 1000, total) + 1)]
        more = offset + len(rows) < total
        return httpx.Response(200, json=rows, headers={"x-has-more": "true" if more else "false",
                                                       **({"x-next-cursor": str(offset + len(rows))} if more else {})})

    got = await mdblist_answering(handler).list_items(2194)
    assert len(got) == total and got[-1][1] == total


async def test_a_refused_follow_says_so_plainly(alice, up):
    async def refused(*args, **kwargs):
        raise MDBListRefused("MDBList refused")

    for name in ("get_list", "list_items", "top_lists", "search_lists"):
        setattr(up.mdblist, name, refused)
    r = await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})
    assert r.status_code == 503 and "refused" in r.json()["detail"]
    page = (await alice.get("/api/v1/mdblist/lists")).json()
    assert "refused" in page["error"]


async def test_list_items_without_a_tmdb_id_are_counted_and_the_list_says_so(alice, up):
    # Movie Night knows titles by their TMDB id: MDBList items without one
    # can't be shown, so they are counted and the list page and the preview
    # say how many (they used to vanish without a word).
    from app.clients.mdblist import ListItems

    def handler(request):
        rows = [{"ids": {"tmdb": 603}, "mediatype": "movie", "title": "Movie 603"},
                {"ids": {"imdb": "tt0000001"}, "mediatype": "movie", "title": "No TMDB id"},
                {"ids": {}, "mediatype": "show", "title": "Nor this"}]
        return httpx.Response(200, json=rows, headers={"x-has-more": "false"})
    got = await mdblist_answering(handler).list_items(2194)
    assert list(got) == [("movie", 603, "Movie 603", None)] and got.no_tmdb == 2

    async def items(list_id):
        return ListItems([("movie", 603, "Movie 603", 2000)], no_tmdb=2)
    up.mdblist.list_items = items
    followed = (await alice.post("/api/v1/mdblist/follow", json={"ref": "2194"})).json()
    page = (await alice.get(f"/api/v1/lists/{followed['id']}")).json()
    assert page["count"] == 1 and page["not_shown"] == 2
    preview = (await alice.get("/api/v1/mdblist/lists/2194")).json()
    assert preview["count"] == 1 and preview["not_shown"] == 2
