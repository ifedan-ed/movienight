"""Search: the library, the catalog (every rated TMDB title, by meaning and
keywords) and TMDB's live title search, ranked on one scale; the section
holding the best match comes first."""
from app.core.config import settings
from app.services import catalog
from tests import fakes


def cat(tid: int, title: str, score: float, votes: int = 1000) -> dict:
    """A catalog hit as Meilisearch returns it."""
    return {"id": f"m{tid}", "tmdb_id": tid, "media_type": "movie", "title": title, "original_title": title,
            "votes": votes, "_rankingScore": score, "tmdb": {**fakes.movie(tid), "title": title}}


def test_title_match():
    assert catalog.title_match("goonies", "The Goonies") == 1.0
    assert catalog.title_match("The Goonies", "Goonies") == 1.0
    assert catalog.title_match("Amélie", "Amelie") == 1.0
    assert catalog.title_match("goonies", "The Goonies Show") == 0.9
    assert catalog.title_match("goonies", "The Making of The Goonies") == 0.85
    assert catalog.title_match("goonies", "Treasure Planet") == 0.0
    assert catalog.title_match("", "Anything") == 0.0


def test_catalog_doc():
    d = catalog.catalog_doc({"id": 9340, "title": "The Goonies", "original_title": "The Goonies", "release_date": "1985-06-07",
                             "genre_ids": [12, 35], "overview": "Kids  find a\ntreasure map.", "vote_count": 6433,
                             "poster_path": "/g.jpg"}, "movie")
    assert d["id"] == "m9340" and d["year"] == 1985 and d["votes"] == 6433
    assert d["searchText"] == "The Goonies | (1985) | Adventure, Comedy | Kids find a treasure map."
    assert d["tmdb"]["poster_path"] == "/g.jpg" and d["tmdb"]["media_type"] == "movie"
    assert catalog.catalog_doc({"id": 1, "title": " "}, "movie") is None
    show = catalog.catalog_doc({"id": 1399, "name": "Show", "original_name": "Show", "first_air_date": "2011-04-17"}, "tv")
    assert show["id"] == "t1399" and show["year"] == 2011


async def test_best_match_outside_the_library_comes_first(alice, up):
    up.meili.catalog = [cat(9341, "Treasure Kids", 0.80, votes=12), cat(9340, "The Goonies", 0.93, votes=6433)]
    r = (await alice.get("/api/v1/search", params={"q": "kids with a treasure map save their homes"})).json()
    assert r["first"] == "tmdb"
    assert [c["title"] for c in r["tmdb"]][:3] == ["The Goonies", "Treasure Kids", "Movie 777"]
    assert [c["title"] for c in r["library"]] == ["Movie 603"]


async def test_library_first_when_it_matches_best(alice, up):
    up.meili.catalog = [cat(9341, "Treasure Kids", 0.55)]
    r = (await alice.get("/api/v1/search", params={"q": "movie 603"})).json()
    assert r["first"] == "library"
    assert r["library"][0]["title"] == "Movie 603"


async def test_exact_title_from_tmdb_beats_a_vague_catalog_hit(alice, up):
    up.meili.catalog = [cat(9341, "Treasure Kids", 0.7)]
    r = (await alice.get("/api/v1/search", params={"q": "Movie 777"})).json()
    assert r["first"] == "tmdb" and r["tmdb"][0]["title"] == "Movie 777"


async def test_titles_in_the_library_are_not_repeated(alice, up):
    up.meili.catalog = [cat(603, "Movie 603", 0.99)]
    r = (await alice.get("/api/v1/search", params={"q": "movie 603"})).json()
    assert [c["key"] for c in r["library"]] == ["movie:603"]
    assert "movie:603" not in [c["key"] for c in r["tmdb"]]


async def test_media_filter_applies_to_the_catalog(alice, up):
    up.meili.catalog = [cat(9340, "The Goonies", 0.93)]
    r = (await alice.get("/api/v1/search", params={"q": "goonies", "media": "tv"})).json()
    assert all(c["media_type"] == "tv" for c in r["tmdb"])


async def test_catalog_sync_sends_only_new_and_changed_titles(up):
    res = await catalog.sync(up.tmdb, up.meili, 25, 15)
    # 1985: 6 movies and 6 shows, plus what TMDB's default filter hides:
    # Cheeky (not marked adult) and a title it marks adult. Nothing is left out.
    assert res["titles"] == 14 and res["changed"] == 14 and res["failed_pages"] == 0
    assert {d["id"] for d in up.meili.sent} >= {"m9010", "t9022", "m22705", "m613683"}
    up.meili.catalog = [dict(d) for d in up.meili.sent] + [{"id": "m1", "h": "old", "media_type": "movie"}]
    up.meili.sent.clear()
    res = await catalog.sync(up.tmdb, up.meili, 25, 15)
    assert res["changed"] == 0 and res["removed"] == 1 and up.meili.deleted == ["m1"]
    assert (await catalog.status())["titles"] == 14


async def test_hide_adult_titles_leaves_out_only_what_tmdb_marks_adult(up, monkeypatch):
    monkeypatch.setattr(settings, "hide_adult_titles", True)
    await catalog.sync(up.tmdb, up.meili, 25, 15)
    sent = {d["id"] for d in up.meili.sent}
    assert "m22705" in sent and "m613683" not in sent


async def test_a_library_title_found_in_the_catalog_counts_as_yours(alice, up):
    from tests.conftest import sync_library
    await sync_library()  # the library holds movie 603 and show 1399
    up.meili.catalog = [{"id": "t1399", "tmdb_id": 1399, "media_type": "tv", "title": "Show 1399", "original_title": "Show 1399",
                         "votes": 900, "_rankingScore": 0.95, "tmdb": {**fakes.show(1399)}}]
    r = (await alice.get("/api/v1/search", params={"q": "dragons and a throne"})).json()
    assert r["library"][0]["key"] == "tv:1399" and r["first"] == "library"
    assert "tv:1399" not in [c["key"] for c in r["tmdb"]]


async def test_the_ai_is_off_unless_a_model_is_set(alice):
    r = (await alice.get("/api/v1/search", params={"q": "kids with a treasure map save their homes"})).json()
    assert r["ai"] is False
    assert (await alice.get("/api/v1/search/ai", params={"q": "kids with a treasure map"})).json() == {"enabled": False, "matches": [], "note": None}


async def test_the_ai_names_the_film_for_a_described_plot(alice, up, monkeypatch):
    from app import clients
    from app.core.config import settings
    monkeypatch.setattr(settings, "search_ai_model", "test-search-model")
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    ai = fakes.FakeSearchAI()
    clients.set_overrides(**{**clients._overrides, "search_ai": ai})
    r = (await alice.get("/api/v1/search", params={"q": "kids with a treasure map save their homes"})).json()
    assert r["ai"] is True
    assert (await alice.get("/api/v1/search", params={"q": "Movie 777"})).json()["ai"] is False  # a title, not a description
    got = (await alice.get("/api/v1/search/ai", params={"q": "kids with a treasure map save their homes"})).json()
    assert got["enabled"] is True and [m["card"]["title"] for m in got["matches"]] == ["Movie Named"]
    assert got["matches"][0]["why"] == "kids, a map, the treasure"
    await alice.get("/api/v1/search/ai", params={"q": "Kids with a treasure map, save their homes!"})
    assert ai.calls == 1  # the same description again comes from the cache


async def test_the_ai_has_no_daily_or_per_person_limit(alice, bob, up, monkeypatch):
    """The owner's rule: no usage caps. Every new description is asked, by
    anyone, however many there are (the old SEARCH_AI_DAILY_LIMIT is gone)."""
    from app import clients
    from app.core.config import settings
    assert not hasattr(settings, "search_ai_daily_limit")
    monkeypatch.setattr(settings, "search_ai_model", "test-search-model")
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    ai = fakes.FakeSearchAI()
    clients.set_overrides(**{**clients._overrides, "search_ai": ai})
    for n in range(350):  # more than the old 300 a day, and the old 40 an hour
        who = alice if n % 2 else bob
        got = (await who.get("/api/v1/search/ai", params={"q": f"description number {n} of a film"})).json()
        assert [m["card"]["title"] for m in got["matches"]] == ["Movie Named"], n
    assert ai.calls == 350


class FlakySearchAI(fakes.FakeSearchAI):
    """Fails, then names nothing, then names a film."""

    def __init__(self):
        super().__init__()
        self.replies = ["fail", "nothing", "film"]

    async def chat_json(self, system, user, attempts=2):
        self.calls += 1
        reply = self.replies[min(self.calls, len(self.replies)) - 1]
        if reply == "fail":
            raise RuntimeError("model endpoint answered 500")
        if reply == "nothing":
            return {"matches": []}
        return {"matches": [{"title": "Movie Named", "year": 1985, "type": "movie", "why": "it fits"}]}


async def test_a_failed_or_empty_ai_answer_is_not_kept_for_a_day(alice, up, monkeypatch):
    """Asked again on the next search (after a minute's back-off at most),
    and the page is told why there is no guess."""
    from app import clients
    from app.core import cache
    from app.core.config import settings
    from app.services import identify
    monkeypatch.setattr(settings, "search_ai_model", "test-search-model")
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    ai = FlakySearchAI()
    clients.set_overrides(**{**clients._overrides, "search_ai": ai})
    q = {"q": "kids with a treasure map save their homes"}
    assert identify.RETRY_AFTER <= 60

    failed = (await alice.get("/api/v1/search/ai", params=q)).json()
    assert failed["matches"] == [] and failed["note"] == identify.FAILED
    assert (await alice.get("/api/v1/search/ai", params=q)).json()["note"] == identify.FAILED  # within the minute: kept
    assert ai.calls == 1

    def a_minute_later():
        for k, (expires, value) in list(cache._store.items()):
            if k.startswith("ai-guess:"):
                cache._store[k] = (expires - identify.RETRY_AFTER - 1, value)

    a_minute_later()
    nothing = (await alice.get("/api/v1/search/ai", params=q)).json()
    assert ai.calls == 2 and nothing["matches"] == [] and nothing["note"] == identify.NOTHING
    a_minute_later()
    found = (await alice.get("/api/v1/search/ai", params=q)).json()
    assert ai.calls == 3 and [m["card"]["title"] for m in found["matches"]] == ["Movie Named"] and found["note"] is None
    a_minute_later()  # a real answer is kept the day
    await alice.get("/api/v1/search/ai", params=q)
    assert ai.calls == 3


async def test_search_shows_every_match_page_by_page(alice, up):
    # Nothing is cut without a word: each section says when it has more, and
    # page 2, 3, ... brings them (the matches before included).
    up.meili.catalog = [cat(5000 + n, f"Batman {n}", 0.9 - n * 0.001) for n in range(70)]
    library = [{"id": f"{n:032x}", "tmdb_id": 3000 + n, "media_type": "movie", "title": f"Batman Library {n}",
                "_rankingScore": 0.95 - n * 0.001} for n in range(30)]
    catalog_search = up.meili.search

    async def search(query, semantic_ratio=0.8, limit=20, media_type=None, index="library", scores=False):
        if index == "library":
            return library[:limit]
        return await catalog_search(query, semantic_ratio, limit, media_type, index, scores)
    up.meili.search = search

    people = [{"id": 900 + n, "media_type": "person", "name": f"Person {n}", "profile_path": None} for n in range(30)]

    async def search_multi_page(query, page=1):
        return people[(page - 1) * 15:page * 15], 2
    up.tmdb.search_multi_page = search_multi_page

    one = (await alice.get("/api/v1/search", params={"q": "batman"})).json()
    assert len(one["library"]) == 24 and len(one["tmdb"]) == 30 and len(one["people"]) == 15
    assert one["page"] == 1 and one["more"] == {"library": True, "tmdb": True, "people": True}
    two = (await alice.get("/api/v1/search", params={"q": "batman", "page": 2})).json()
    assert len(two["library"]) == 30 and len(two["tmdb"]) == 60 and len(two["people"]) == 30
    assert [c["key"] for c in two["library"][:24]] == [c["key"] for c in one["library"]]  # the first page, then more
    assert two["more"] == {"library": False, "tmdb": True, "people": False}
    three = (await alice.get("/api/v1/search", params={"q": "batman", "page": 3})).json()
    assert len(three["tmdb"]) == 70 and three["more"]["tmdb"] is False and three["note"] is None


async def test_search_stops_at_page_25_so_one_request_costs_at_most_50_tmdb_calls(alice, up, monkeypatch):
    # Before: page N read TMDB's pages 1 to N for each wording, up to page
    # 500: one request for page 500 cost about 1,000 TMDB calls. Page N
    # still needs pages 1 to N (it holds every match before it), so search
    # now stops at page 25: at most 25 TMDB pages per wording, 50 in all,
    # read a few at a time, and paging on reads one new page per step.
    import asyncio

    from app.api.v1 import catalog as search_api
    from app.services import spelling
    assert search_api.SEARCH_LAST_PAGE == 25
    calls, running, most = [], 0, 0

    async def search_multi_page(query, page=1):
        nonlocal running, most
        running += 1
        most = max(most, running)
        await asyncio.sleep(0.001)
        calls.append((query, page))
        running -= 1
        # TMDB reports more pages than the 500 it serves.
        return [{"id": 90000 + page, "media_type": "person", "name": f"Person {page}", "profile_path": None}], 10_000
    up.tmdb.search_multi_page = search_multi_page
    monkeypatch.setattr(spelling, "correct", lambda q, vocab: "batman")  # "batmn" is searched in both wordings

    huge = await alice.get("/api/v1/search", params={"q": "batmn", "page": 1_000_000})
    assert huge.status_code == 422 and huge.json()["detail"] == "Page: Input should be less than or equal to 25."
    assert (await alice.get("/api/v1/search", params={"q": "batmn", "page": 26})).status_code == 422
    assert (await alice.get("/api/v1/search", params={"q": "batmn", "page": 500})).status_code == 422
    assert calls == []

    # Paging one "Show more" at a time: one new TMDB page per wording each step.
    for page in (1, 2, 3):
        assert (await alice.get("/api/v1/search", params={"q": "batmn", "page": page})).status_code == 200
    assert sorted(calls) == sorted((q, n) for q in ("batmn", "batman") for n in (1, 2, 3))

    # The deepest page, nothing in the cache: 25 pages per wording, each once.
    calls.clear()
    from app.core import cache
    cache._store.clear()
    last = await alice.get("/api/v1/search", params={"q": "batmn", "page": search_api.SEARCH_LAST_PAGE})
    assert last.status_code == 200 and len(calls) == 50
    assert sorted(calls) == sorted((q, n) for q in ("batmn", "batman") for n in range(1, 26))
    assert most <= search_api.TMDB_PAGES_AT_ONCE
    body = last.json()
    assert len(body["people"]) == 25
    # TMDB has more, but there is no "Show more" to a page 26: the answer says why.
    assert body["more"] == {"library": False, "tmdb": False, "people": False}
    assert body["note"] == "That's as far as one search goes (page 25); add words to narrow it down."
    calls.clear()
    again = await alice.get("/api/v1/search", params={"q": "batmn", "page": 3})
    assert again.status_code == 200 and calls == []  # pages read in the last 10 minutes come from the cache
    assert again.json()["more"]["people"] is True and again.json()["note"] is None


async def test_a_section_stopped_by_meilisearch_says_so(alice, up, monkeypatch):
    # Meilisearch gives at most SEARCH_MAX_HITS hits for one search (Movie
    # Night sets its maxTotalHits to it): a section that stops there says so.
    from app.api.v1 import catalog as search_api
    from app.clients.meili import MeiliClient
    monkeypatch.setattr(settings, "search_max_hits", 60)
    many = [{"id": f"{n:032x}", "tmdb_id": 3000 + n, "media_type": "movie", "title": f"Film {n}",
             "_rankingScore": 0.9} for n in range(500)]

    async def search(query, semantic_ratio=0.8, limit=20, media_type=None, index="library", scores=False):
        return many[:min(limit, settings.search_max_hits)] if index == "library" else []
    up.meili.search = search
    two = (await alice.get("/api/v1/search", params={"q": "film", "page": 2})).json()
    assert len(two["library"]) == 2 * search_api.LIBRARY_PAGE and two["more"]["library"] is True and two["note"] is None
    three = (await alice.get("/api/v1/search", params={"q": "film", "page": 3})).json()  # asks for 72 of at most 60
    assert len(three["library"]) == 60 and three["more"]["library"] is False
    assert three["note"] == ("These are the 60 best matches in your library (the most one search gives); "
                             "add words to narrow it down.")
    # The index setting comes from the same value.
    import httpx
    sent = []

    def handler(request):
        sent.append(request)
        return httpx.Response(200, json={"taskUid": 1})
    client = MeiliClient("http://meili.test", "", "model", client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
                         max_hits=settings.search_max_hits)
    await client.ensure_index()
    import json
    assert json.loads(sent[-1].content)["pagination"] == {"maxTotalHits": 60}


async def test_a_description_in_any_script_gets_the_ai(alice, up, monkeypatch):
    # Words are counted in every script (the old count was ASCII only, so a
    # description in Russian, Greek or Japanese never got a best guess).
    from app.core.config import settings
    from app.services import identify
    monkeypatch.setattr(settings, "search_ai_model", "test-search-model")
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    for q in ("дети ищут карту сокровищ", "Παιδιά με χάρτη θησαυρού", "宝の地図を持った子供たちが家を救う", "보물 지도를 가진 아이들"):
        assert (await alice.get("/api/v1/search", params={"q": q})).json()["ai"] is True, q
    assert identify.words("don't know it") == 3 and identify.words("千と千尋") == 2  # two characters a word without spaces
    assert (await alice.get("/api/v1/search", params={"q": "карта сокровищ"})).json()["ai"] is False  # two words


class _BigYearTMDB:
    """TMDB's discover for a catalog year with more titles than its 500 pages
    serve: `per_day` titles a day in 2001, 20 a page, at most 500 pages."""

    def __init__(self, per_day: dict[str, int] | int):
        self.per_day = per_day

    def _days(self, lo, hi):
        import datetime as dt
        d, out = lo, []
        while d <= hi:
            out.append(d)
            d += dt.timedelta(days=1)
        return out

    def _count(self, day) -> int:
        return self.per_day.get(day.isoformat(), 0) if isinstance(self.per_day, dict) else self.per_day

    async def discover_page(self, media, page=1, include_adult=False, **f):
        import datetime as dt
        if f.get("primary_release_year") == 2001:
            days = self._days(dt.date(2001, 1, 1), dt.date(2001, 12, 31))
        elif "primary_release_date.gte" in f:
            days = self._days(dt.date.fromisoformat(f["primary_release_date.gte"]), dt.date.fromisoformat(f["primary_release_date.lte"]))
        else:
            return {"results": [], "total_pages": 0}
        ids = [day.toordinal() * 100_000 + n for day in days for n in range(self._count(day))]
        assert page <= 500, "TMDB serves no page past 500"
        return {"results": [fakes.movie(i) for i in ids[(page - 1) * 20:page * 20]], "total_pages": -(-len(ids) // 20)}


async def test_a_catalog_year_past_tmdbs_500_pages_is_read_whole():
    # 30 titles a day: 10,950 in the year, 548 pages. Read in halves of the
    # year, every title comes in and nothing counts as failed.
    got, failed = await catalog.fetch(_BigYearTMDB(30), "movie", 5, first_year=2001)
    assert failed == 0 and len({r["id"] for r in got}) == 365 * 30


async def test_a_day_past_tmdbs_500_pages_counts_as_failed_so_nothing_is_removed():
    got, failed = await catalog.fetch(_BigYearTMDB({"2001-06-01": 10_020}), "movie", 5, first_year=2001)
    assert failed == 1 and len(got) == 10_000


async def test_admins_hear_of_a_catalog_refresh_that_couldnt_read_everything(owner, alice, up):
    up.tmdb.discover_page = _BigYearTMDB({"2001-06-01": 10_020}).discover_page
    from app.services import catalog as catalog_service
    original = catalog_service.FIRST_YEAR
    catalog_service.FIRST_YEAR = 2001
    try:
        await catalog_service.sync(up.tmdb, up.meili)
    finally:
        catalog_service.FIRST_YEAR = original
    notices = (await owner.get("/api/v1/me")).json()["admin_notices"]
    assert any("couldn't read everything from TMDB (1 read failed" in n and "none was removed" in n for n in notices), notices
    assert (await alice.get("/api/v1/me")).json()["admin_notices"] == []


async def test_an_ai_that_runs_out_of_room_says_so(alice, up, monkeypatch):
    # A reasoning model can think its whole allowance (SEARCH_AI_MAX_TOKENS)
    # away: Best guess says that, not "the AI didn't answer".
    import httpx

    from app import clients
    from app.clients.litellm import LiteLLMClient
    from app.core.config import settings
    monkeypatch.setattr(settings, "search_ai_model", "test-search-model")
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    monkeypatch.setattr(settings, "search_ai_max_tokens", 64)
    asked = []

    def handler(request):
        import json
        asked.append(json.loads(request.content)["max_tokens"])
        return httpx.Response(200, json={"choices": [{"message": {"content": ""}, "finish_reason": "length"}]})
    clients.set_overrides(**{k: v for k, v in clients._overrides.items() if k != "search_ai"})
    monkeypatch.setattr(clients, "http", lambda: httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    assert isinstance(clients.search_ai(), LiteLLMClient)
    got = (await alice.get("/api/v1/search/ai", params={"q": "a reasoning model thinks for too long"})).json()
    assert asked == [64] and got["matches"] == []
    assert got["note"].startswith("The AI ran out of room before it answered")
