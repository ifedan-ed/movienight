"""'Picked for you': no retry loop. A rebuild that fails (the model errors,
or none of its picks is on TMDB) backs off (10 minutes, doubling, at most
6 hours) and Home says so in plain words; only the newest rows are kept;
the rows say what of the person's history the AI read."""
import datetime as dt

import pytest
from sqlalchemy import func, select

from app import clients
from app.core.config import settings
from app.core.db import session
from app.models import AIRowCache, HistoryEntry, User
from app.services import rows


class AI:
    enabled = True
    model = "test-model"

    def __init__(self, fail: bool = False):
        self.fail, self.calls = fail, 0

    async def chat_json(self, system, user, attempts=2):
        self.calls += 1
        if self.fail:
            raise RuntimeError("model endpoint answered 500")
        return {"rows": [{"theme": f"Theme {n}", "titles": [f"Film {n}-{i} (1999)" for i in range(3)]} for n in range(6)]}


@pytest.fixture
def ai_on(monkeypatch, up):
    monkeypatch.setattr(settings, "litellm_api_key", "test-key")
    monkeypatch.setattr(settings, "litellm_model", "test-model")

    def use(ai):
        clients.set_overrides(**{**clients._overrides, "litellm": ai})
        return ai
    return use


async def _user_id() -> int:
    async with session() as db:
        return await db.scalar(select(User.id).where(User.username == "alice"))


async def _settle(user_id: int) -> None:
    task = rows._ai_tasks.get(user_id)
    if task is not None:
        await task


async def test_a_failed_rebuild_backs_off_and_home_says_why(alice, ai_on):
    ai = ai_on(AI(fail=True))
    await alice.get("/api/v1/me")
    uid = await _user_id()
    first = (await alice.get("/api/v1/rows/ai")).json()
    assert first["rows"] == [] and "being made" in first["note"]
    await _settle(uid)
    assert ai.calls == 1
    for _ in range(5):  # Home views meanwhile start nothing
        got = (await alice.get("/api/v1/rows/ai")).json()
        await _settle(uid)
    assert ai.calls == 1
    assert "couldn't be made just now: the AI didn't answer" in got["note"]
    assert "tries again by itself in about 10 minutes" in got["note"]
    # Once the wait is over, it is tried again, and a second failure waits twice as long.
    rows._ai_failures[uid]["until"] = dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=1)
    await alice.get("/api/v1/rows/ai")
    await _settle(uid)
    assert ai.calls == 2 and "in about 20 minutes" in (await alice.get("/api/v1/rows/ai")).json()["note"]
    # ...never more than 6 hours.
    for _ in range(10):
        rows._ai_failed(uid, "the AI didn't answer")
    assert rows._ai_failures[uid]["until"] - dt.datetime.now(dt.timezone.utc) <= rows.AI_RETRY_MAX


async def test_picks_tmdb_doesnt_know_count_as_a_failure_and_store_nothing(alice, ai_on, up):
    ai_on(AI())

    async def nothing(query, year=None):
        return []

    up.tmdb.search_movie = nothing
    await alice.get("/api/v1/me")
    uid = await _user_id()
    await alice.get("/api/v1/rows/ai")
    await _settle(uid)
    note = (await alice.get("/api/v1/rows/ai")).json()["note"]
    assert "none of the AI's picks could be found on TMDB" in note
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(AIRowCache)) == 0


async def test_a_good_rebuild_replaces_the_rows_before_and_says_what_it_read(alice, ai_on):
    ai = ai_on(AI())
    await alice.get("/api/v1/me")
    uid = await _user_id()
    async with session() as db:
        db.add_all([HistoryEntry(user_id=uid, media_type="movie", tmdb_id=600 + n, title=f"Movie {600 + n}",
                                 watched_at=dt.datetime(2025, 1, 1, tzinfo=dt.timezone.utc) + dt.timedelta(days=n)) for n in range(70)])
        await db.commit()
    await alice.get("/api/v1/rows/ai")
    await _settle(uid)
    got = (await alice.get("/api/v1/rows/ai")).json()
    assert got["note"] is None and got["rows"]
    assert got["rows"][0]["subtitle"] == "From your 60 latest of 70 films"
    rows._ai_failures.clear()
    await rows._generate(uid)  # a second rebuild
    assert ai.calls == 2
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(AIRowCache).where(AIRowCache.user_id == uid)) == 1


async def test_what_the_ai_reads_is_a_setting_and_said(alice, ai_on, monkeypatch):
    from app.services import recommend
    monkeypatch.setattr(settings, "ai_prompt_titles", 10)
    await alice.get("/api/v1/me")
    uid = await _user_id()
    async with session() as db:
        db.add_all([HistoryEntry(user_id=uid, media_type="movie", tmdb_id=700 + n, title=f"Movie {700 + n}",
                                 watched_at=dt.datetime(2025, 1, 1, tzinfo=dt.timezone.utc) + dt.timedelta(days=n)) for n in range(25)])
        await db.commit()
        prompt, basis = await recommend.history_summary(db, uid)
    assert prompt.count("Movie ") == 10 and "Movie 724" in prompt  # the 10 latest
    assert recommend.basis_line(basis) == "From your 10 latest of 25 films"


async def test_a_shortened_comment_is_said_and_its_length_is_a_setting(alice, monkeypatch):
    from app.models import UserRating
    from app.services import recommend
    monkeypatch.setattr(settings, "ai_prompt_comment_chars", 50)
    await alice.get("/api/v1/me")
    uid = await _user_id()
    async with session() as db:
        db.add_all([HistoryEntry(user_id=uid, media_type="movie", tmdb_id=603, title="Movie 603",
                                 watched_at=dt.datetime(2025, 1, 1, tzinfo=dt.timezone.utc)),
                    UserRating(user_id=uid, media_type="movie", tmdb_id=603, rating=9, review="long " * 30),
                    UserRating(user_id=uid, media_type="movie", tmdb_id=604, rating=7, review="short one")])
        await db.commit()
        from app import clients
        from app.services import titles
        await titles.ensure(db, clients.tmdb(), ["movie:603", "movie:604"])
        prompt, basis = await recommend.history_summary(db, uid)
    assert ("long " * 10).strip() + "…" in prompt and "short one" in prompt  # 50 characters, then a mark
    assert recommend.basis_line(basis) == ("From your 1 film, your 2 film ratings and your 2 comments "
                                           "(1 shortened to its first 50 characters)")
