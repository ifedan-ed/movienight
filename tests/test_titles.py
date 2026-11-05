"""A title's page and everything you can do to it; each person's marks are
their own."""
from tests.conftest import sync_library


async def test_title_page(alice):
    d = (await alice.get("/api/v1/title/movie/603")).json()
    assert d["title"] == "Movie 603" and d["certification"] == "R"
    assert d["directors"] == ["Dee Rector"] and d["trailer"].endswith("v=abc")
    assert d["providers"]["stream"][0]["name"] == "Netflix"
    assert d["community"] == {"average": None, "count": 0, "mine": None, "others": []}


async def test_unknown_title_is_404(alice):
    assert (await alice.get("/api/v1/title/movie/9999999")).status_code == 404


async def test_watchlist_favorite_rating_are_per_person(alice, bob):
    assert (await alice.post("/api/v1/title/movie/603/watchlist")).json()["watchlist"] is True
    assert (await alice.post("/api/v1/title/movie/603/favorite")).json()["favorite"] is True
    r = (await alice.post("/api/v1/title/movie/603/rating", json={"rating": 9, "review": "Holds up."})).json()
    assert r == {"ok": True, "rating": 9, "review": "Holds up."}
    st = (await alice.get("/api/v1/title/movie/603/status")).json()
    assert st["watchlist"] and st["favorite"] and st["rating"] == 9
    st_b = (await bob.get("/api/v1/title/movie/603/status")).json()
    assert not st_b["watchlist"] and not st_b["favorite"] and st_b["rating"] is None


async def test_ratings_and_comments_show_for_everyone(alice, bob):
    await alice.post("/api/v1/title/movie/603/rating", json={"rating": 9, "review": "Holds up."})
    await bob.post("/api/v1/title/movie/603/rating", json={"rating": 6})
    d = (await bob.get("/api/v1/title/movie/603")).json()["community"]
    assert d["average"] == 7.5 and d["count"] == 2
    assert d["mine"]["rating"] == 6 and d["mine"]["mine"] is True
    assert d["others"][0]["name"] == "alice" and d["others"][0]["review"] == "Holds up."


async def test_rating_without_review_keeps_the_comment(alice):
    await alice.post("/api/v1/title/movie/603/rating", json={"rating": 9, "review": "Holds up."})
    r = (await alice.post("/api/v1/title/movie/603/rating", json={"rating": 8})).json()
    assert r["rating"] == 8 and r["review"] == "Holds up."


async def test_watched_with_a_date_and_unwatch(alice):
    await alice.post("/api/v1/title/movie/603/watchlist")
    w = (await alice.post("/api/v1/title/movie/603/watched", json={"watched_at": "2026-09-30"})).json()
    assert len(w["watches"]) == 1 and w["watches"][0]["watched_at"].startswith("2026-09-30T20:00")
    assert (await alice.get("/api/v1/title/movie/603/status")).json()["watchlist"] is False  # watched -> off the watchlist
    assert (await alice.post("/api/v1/title/movie/603/watched", json={"watched_at": "2099-01-01"})).status_code == 422
    left = (await alice.post("/api/v1/title/movie/603/unwatch", json={"ids": w["watches"][0]["ids"]})).json()
    assert left["watches"] == []


async def test_cannot_remove_someone_elses_watch(alice, bob):
    w = (await alice.post("/api/v1/title/movie/603/watched", json={})).json()
    await bob.post("/api/v1/history/remove", json={"ids": w["watches"][0]["ids"]})
    assert (await alice.get("/api/v1/history")).json()["total"] == 1


async def test_request_in_dry_run(alice):
    r = (await alice.post("/api/v1/title/movie/550/request")).json()
    assert r["state"]["request"] == "dry_run"


async def test_scores_come_from_mdblist(alice):
    s = (await alice.get("/api/v1/title/movie/603/scores")).json()["scores"]
    assert s["imdb_rating"] == 8.7 and s["rt_critics"] == 83 and s["rt_url"] == "https://www.rottentomatoes.com/m/x"


async def test_library_title_state(alice):
    await sync_library()
    st = (await alice.get("/api/v1/title/movie/603/status")).json()
    assert st["in_library"] is True and st["jellyfin_url"].endswith("a" * 32)
