"""The library sync never empties or shrinks the library on a bad answer from
Jellyfin, and nothing caps how much it reads: the whole library and all of a
person's plays are read page by page. Only after a complete, successful read
are titles Jellyfin no longer lists removed; anything else changes nothing,
is logged and is shown to admins in Profile."""
import json

import httpx
import pytest
from sqlalchemy import func, select

from app.clients.jellyfin import LIBRARY_PAGE, IncompleteRead, JellyfinClient
from app.core.db import session
from app.models import HistoryEntry, LibraryItem, User
from app.services import sync

USER = "c" * 32


def movie(n: int) -> dict:
    return {"Id": f"{n:032x}", "Type": "Movie", "Name": f"Movie {n}", "ProductionYear": 2000,
            "ProviderIds": {"Tmdb": str(n)}, "DateCreated": "2026-09-01T00:00:00Z"}


class Jellyfin:
    """A Jellyfin answering Items queries from `items`, page by page, as the
    real one does. `broken` changes one answer: (page number, how)."""

    def __init__(self, items: list[dict], broken: tuple[int, str] | None = None):
        self.items, self.broken, self.asked = items, broken, []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        p = request.url.params
        start, limit = int(p.get("StartIndex", 0)), int(p.get("Limit", 100))
        page_no = len(self.asked)
        self.asked.append((start, limit))
        body = {"Items": self.items[start:start + limit], "TotalRecordCount": len(self.items)}
        how = self.broken[1] if self.broken and self.broken[0] == page_no else None
        if how == "404":
            return httpx.Response(404)
        if how == "500":
            return httpx.Response(500, text="boom")
        if how == "empty body":
            return httpx.Response(200, content=b"")
        if how == "not json":
            return httpx.Response(200, text="<html>proxy error</html>")
        if how == "no items":
            return httpx.Response(200, json={"TotalRecordCount": len(self.items)})
        if self.broken and self.broken[1] == "short":
            body["TotalRecordCount"] += 5  # counts five titles it never sends
        if how == "stops":
            body["Items"] = []  # a page that comes back with nothing before the end
        if how == "grew":
            body["TotalRecordCount"] += 3  # titles added while the pages were read
        if how == "shifted":
            body["Items"] = self.items[start - 1:start - 1 + limit]  # the list moved by one
        return httpx.Response(200, content=json.dumps(body).encode(), headers={"content-type": "application/json"})


def client(server: Jellyfin) -> JellyfinClient:
    return JellyfinClient("https://jf.test", "key", client=httpx.AsyncClient(transport=httpx.MockTransport(server)))


async def library_count() -> int:
    async with session() as db:
        return await db.scalar(select(func.count()).select_from(LibraryItem))


async def run_sync(server: Jellyfin) -> dict:
    async with session() as db:
        return await sync.sync_library(db, client(server), None, USER)


# -- reading ------------------------------------------------------------------------


async def test_the_whole_library_is_read_page_by_page_past_the_old_20000_cap():
    server = Jellyfin([movie(n) for n in range(1, 25_001)])
    got = await client(server).library_items(USER)
    assert len(got) == 25_000 and got[-1]["Name"] == "Movie 25000"
    assert server.asked[0] == (0, LIBRARY_PAGE) and len(server.asked) == 25_000 // LIBRARY_PAGE
    assert all(limit == LIBRARY_PAGE for _, limit in server.asked)  # never one huge Limit


async def test_all_of_a_persons_plays_are_read_past_the_old_2000_cap():
    server = Jellyfin([movie(n) for n in range(1, 4_501)])
    got = await client(server).played("f" * 32)
    assert len(got) == 4_500
    assert [start for start, _ in server.asked] == [0, 1000, 2000, 3000, 4000]


async def test_a_complete_empty_library_reads_as_empty():
    assert await client(Jellyfin([])).library_items(USER) == []


@pytest.mark.parametrize("how", ["404", "500", "empty body", "not json", "no items", "short", "stops", "grew", "shifted"])
async def test_a_read_that_isnt_whole_is_refused(how):
    page = 0 if how in ("404", "500", "empty body", "not json", "no items") else 1
    server = Jellyfin([movie(n) for n in range(1, 1_201)], broken=(page, how))
    with pytest.raises(IncompleteRead) as err:
        await client(server).library_items(USER)
    assert str(err.value)  # says what went wrong, in words


# -- syncing ------------------------------------------------------------------------


async def test_a_large_library_is_kept_whole():
    await run_sync(Jellyfin([movie(n) for n in range(1, 21_001)]))
    assert await library_count() == 21_000


async def test_titles_go_only_after_a_complete_read_no_longer_lists_them():
    everything = [movie(n) for n in range(1, 1_201)]
    await run_sync(Jellyfin(everything))
    got = await run_sync(Jellyfin([m for m in everything if m["Name"] != "Movie 7"]))
    assert got["removed"] == 1 and await library_count() == 1_199


@pytest.mark.parametrize("how", ["404", "500", "empty body", "not json", "no items", "short", "stops", "grew", "shifted"])
async def test_a_failed_or_partial_read_leaves_the_library_as_it_was(how, owner, alice):
    everything = [movie(n) for n in range(1, 1_201)]
    await run_sync(Jellyfin(everything))
    page = 0 if how in ("404", "500", "empty body", "not json", "no items") else 1
    with pytest.raises(sync.LibraryNotRead):
        await run_sync(Jellyfin(everything, broken=(page, how)))
    assert await library_count() == 1_200
    # Admins are told, in plain words; nobody else is.
    notices = (await owner.get("/api/v1/me")).json()["admin_notices"]
    assert len(notices) == 1 and "didn't finish" in notices[0] and "1,200 titles" in notices[0]
    assert (await alice.get("/api/v1/me")).json()["admin_notices"] == []
    # The next good sync clears the notice.
    await run_sync(Jellyfin(everything))
    assert (await owner.get("/api/v1/me")).json()["admin_notices"] == []


async def test_an_empty_answer_never_empties_the_library(owner):
    await run_sync(Jellyfin([movie(n) for n in range(1, 51)]))
    with pytest.raises(sync.LibraryNotRead, match="no movies or series"):
        await run_sync(Jellyfin([]))  # a complete read that lists nothing (a user who lost access, say)
    assert "no movies or series" in (await owner.get("/api/v1/me")).json()["admin_notices"][0]
    with pytest.raises(sync.LibraryNotRead, match="none of them with an id"):
        await run_sync(Jellyfin([{"Type": "Movie", "Name": "nameless"}]))
    assert await library_count() == 50


async def test_an_admin_rescan_says_why_nothing_changed(owner, up):
    from app import clients
    everything = [movie(n) for n in range(1, 11)]
    await run_sync(Jellyfin(everything))
    clients.set_overrides(**{**clients._overrides, "jellyfin": client(Jellyfin(everything, broken=(0, "404")))})
    r = await owner.post("/api/v1/sync/library")
    assert r.status_code == 502
    assert "HTTP 404" in r.json()["detail"] and "left as it was (10 titles)" in r.json()["detail"]
    assert await library_count() == 10


async def test_every_play_comes_into_history(alice, up):
    await alice.get("/api/v1/me")
    plays = [{**movie(n), "UserData": {"LastPlayedDate": f"2025-01-01T{n // 3600 % 24:02d}:{n // 60 % 60:02d}:{n % 60:02d}Z"}}
             for n in range(1, 2_601)]
    up.jellyfin.played_items = plays
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        user.jellyfin_user_id = "f" * 32
        await db.commit()
        assert await sync.sync_plays(db, up.jellyfin, user) == 2_600
        assert await db.scalar(select(func.count()).select_from(HistoryEntry)) == 2_600


async def test_a_plays_read_that_isnt_whole_adds_nothing(alice):
    await alice.get("/api/v1/me")
    server = Jellyfin([{**movie(n), "UserData": {"LastPlayedDate": "2025-01-01T20:00:00Z"}} for n in range(1, 1_501)],
                      broken=(1, "stops"))
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        user.jellyfin_user_id = "f" * 32
        await db.commit()
        with pytest.raises(IncompleteRead):
            await sync.sync_plays(db, client(server), user)
        assert await db.scalar(select(func.count()).select_from(HistoryEntry)) == 0


async def _link_alice() -> int:
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        user.jellyfin_user_id = "f" * 32
        await db.commit()
        return user.id


async def test_a_plays_read_that_keeps_failing_is_shown_to_admins_and_the_person(alice, owner, up, caplog):
    # A person whose Jellyfin account was deleted: every read answers 404.
    import logging
    await alice.get("/api/v1/me")
    await owner.get("/api/v1/me")
    uid = await _link_alice()
    caplog.set_level(logging.WARNING)
    async with session() as db:
        assert await sync.sync_all_plays(db, client(Jellyfin([], broken=(0, "404")))) == {}
    lines = [r for r in caplog.records if "plays sync for user" in r.getMessage()]
    assert len(lines) == 1 and lines[0].exc_info is None  # one line, no traceback every 5 minutes
    assert lines[0].getMessage() == f"plays sync for user {uid}: nothing added: Jellyfin answered HTTP 404"
    notices = (await owner.get("/api/v1/me")).json()["admin_notices"]
    assert any(f"alice's (user {uid}) Jellyfin plays couldn't be read" in n and "HTTP 404" in n for n in notices), notices
    mine = (await alice.get("/api/v1/me")).json()
    assert mine["admin_notices"] == []
    assert mine["jellyfin"]["plays_notice"].startswith("Your Jellyfin plays couldn't be read")
    assert "connect it again" in (await alice.get("/api/v1/me/jellyfin")).json()["plays_notice"]
    # The next complete read clears it, for both.
    async with session() as db:
        assert await sync.sync_all_plays(db, client(Jellyfin([]))) == {uid: 0}
    assert not any("plays" in n for n in (await owner.get("/api/v1/me")).json()["admin_notices"])
    assert (await alice.get("/api/v1/me")).json()["jellyfin"]["plays_notice"] is None


async def test_disconnecting_jellyfin_forgets_a_failed_read(alice, owner, up):
    await alice.get("/api/v1/me")
    await _link_alice()
    async with session() as db:
        await sync.sync_all_plays(db, client(Jellyfin([], broken=(0, "500"))))
    assert any("plays couldn't be read" in n for n in (await owner.get("/api/v1/me")).json()["admin_notices"])
    assert (await alice.delete("/api/v1/me/jellyfin")).status_code == 200
    assert not any("plays" in n for n in (await owner.get("/api/v1/me")).json()["admin_notices"])


async def test_plays_without_a_date_are_counted_and_the_person_is_told(alice, up):
    await alice.get("/api/v1/me")
    await _link_alice()
    up.jellyfin.played_items = [{**movie(1), "UserData": {"LastPlayedDate": "2025-01-01T20:00:00Z"}},
                                {**movie(2), "UserData": {}}, {**movie(3), "UserData": {"Played": True}}]
    r = await alice.post("/api/v1/sync/plays")
    assert r.status_code == 200 and r.json()["counts"] == {"plays": 1}
    assert r.json()["note"].startswith("2 of your Jellyfin plays have no date in Jellyfin")
    assert "2 of your Jellyfin plays have no date" in (await alice.get("/api/v1/me/jellyfin")).json()["plays_notice"]


async def test_one_persons_failed_plays_read_doesnt_stop_the_others(alice, bob, up):
    # A failed read rolls the session back, which expires every person loaded
    # with it: the people after the failing one must still be synced.
    await alice.get("/api/v1/me")
    await bob.get("/api/v1/me")
    async with session() as db:
        people = {u.username: u for u in await db.scalars(select(User).where(User.username.in_(["alice", "bob"])))}
        people["alice"].jellyfin_user_id, people["bob"].jellyfin_user_id = "a" * 32, "b" * 32
        await db.commit()
        ids = {name: u.id for name, u in people.items()}
    played = {**movie(7), "UserData": {"LastPlayedDate": "2025-01-01T20:00:00Z"}}

    def server(request: httpx.Request) -> httpx.Response:
        if "a" * 32 in request.url.path:
            return httpx.Response(404)
        return httpx.Response(200, json={"Items": [played], "TotalRecordCount": 1})

    jf = JellyfinClient("https://jf.test", "key", client=httpx.AsyncClient(transport=httpx.MockTransport(server)))
    async with session() as db:
        got = await sync.sync_all_plays(db, jf)
    assert got == {ids["bob"]: 1}
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(HistoryEntry).where(HistoryEntry.user_id == ids["bob"])) == 1
