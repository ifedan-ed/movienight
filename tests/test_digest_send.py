"""Digest emails: on by default for everyone with an address, never sent
empty (nothing watched, rated or favourited, nothing new in the library),
and every one carries a link (and a List-Unsubscribe header) that switches
that email off. A list cut short in the email says how many more."""
import datetime as dt

from sqlalchemy import func, select

from app.core.db import session
from app.models import Digest, HistoryEntry, LibraryItem, User
from app.services import digest


async def _alice() -> User:
    async with session() as db:
        return await db.scalar(select(User).where(User.username == "alice"))


def _in_last_week() -> dt.datetime:
    start, _ = digest.last_period("week")
    return start + dt.timedelta(days=2, hours=20)


async def _run(kind: str = "week") -> int:
    async with session() as db:
        return await digest.run_scheduled(db, kind)


async def _stored() -> int:
    async with session() as db:
        return await db.scalar(select(func.count()).select_from(Digest))


async def test_on_by_default(alice):
    got = (await alice.get("/api/v1/me/emails")).json()
    assert got["weekly"] is True and got["monthly"] is True and got["yearly"] is True
    assert got["address"] == "alice@example.org"


async def test_an_empty_digest_is_never_sent(alice, up):
    await alice.get("/api/v1/me")
    assert await _run("week") == 0 and await _run("month") == 0
    assert up.mailer.sent == [] and await _stored() == 0  # nor written (no model call) nor kept


async def test_a_preview_made_earlier_doesnt_make_an_empty_one_go_out(alice, up):
    assert (await alice.post("/api/v1/me/digests/week")).status_code == 200  # the person asked to see it
    assert await _run("week") == 0 and up.mailer.sent == []


async def test_a_week_with_a_watch_is_sent_with_a_way_to_turn_it_off(alice, up):
    await alice.get("/api/v1/me")
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
    assert await _run("week") == 1
    assert up.mailer.sent[0][0] == "alice@example.org"
    off = "https://mn.test/settings/emails?off=week"
    assert up.mailer.headers[0]["List-Unsubscribe"] == f"<{off}>"
    async with session() as db:
        d = await db.scalar(select(Digest))
    assert f'href="{off}"' in d.html and "Turn this email off" in d.html
    # The link switches only that one off.
    assert (await alice.put("/api/v1/me/emails", json={"weekly": False})).json()["weekly"] is False
    assert (await alice.get("/api/v1/me/emails")).json()["monthly"] is True


async def test_something_new_in_the_library_is_worth_an_email(alice, up):
    await alice.get("/api/v1/me")
    async with session() as db:
        db.add(LibraryItem(jellyfin_id="d" * 32, media_type="movie", tmdb_id=604, title="Movie 604", date_added=_in_last_week()))
        await db.commit()
    assert await _run("week") == 1


async def test_switched_off_means_not_sent(alice, up):
    await alice.put("/api/v1/me/emails", json={"weekly": False})
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
    assert await _run("week") == 0 and up.mailer.sent == []


async def test_a_list_cut_short_says_how_many_more(alice):
    await alice.get("/api/v1/me")
    user = await _alice()
    when = _in_last_week()
    async with session() as db:
        db.add_all([HistoryEntry(user_id=user.id, media_type="episode", tmdb_id=1000 + n, season=1, episode=1,
                                 title=f"Show {1000 + n}", watched_at=when) for n in range(15)])
        await db.commit()
        start, end = digest.last_period("week")
        data = await digest.gather(db, user, "week", start, end)
    assert len(data["shows"]) == 12 and data["more"]["shows"] == 3
    page, text = digest.render(data, digest.plain_words(data))
    assert "…and 3 more shows" in page and "...and 3 more shows" in text


async def test_a_long_comment_is_shortened_with_a_mark(alice):
    from app.models import UserRating
    await alice.get("/api/v1/me")
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        db.add(UserRating(user_id=user.id, media_type="movie", tmdb_id=603, rating=9, review="word " * 200,
                          rated_at=_in_last_week()))
        await db.commit()
        start, end = digest.last_period("week")
        data = await digest.gather(db, user, "week", start, end)
    review = data["films"][0]["review"]
    assert review.endswith("…") and len(review) <= 401


class _CountingAI:
    enabled = True
    model = "test-model"

    def __init__(self):
        self.calls = 0

    async def chat_json(self, system, user, attempts=2):
        self.calls += 1
        return {}


async def test_a_year_with_only_a_library_addition_is_not_sent(alice, up):
    # The year in review has no "New in the library" section, so a year in
    # which the person watched, rated and favourited nothing is empty, however
    # much the library grew: no email, no model call, nothing kept.
    from app import clients
    ai = _CountingAI()
    clients.set_overrides(**{**clients._overrides, "litellm": ai})
    await alice.get("/api/v1/me")
    start, _ = digest.last_period("year")
    async with session() as db:
        db.add(LibraryItem(jellyfin_id="e" * 32, media_type="movie", tmdb_id=604, title="Movie 604",
                           date_added=start + dt.timedelta(days=40)))
        await db.commit()
    assert await _run("year") == 0
    assert up.mailer.sent == [] and ai.calls == 0 and await _stored() == 0


async def test_a_year_with_a_watch_is_sent(alice, up):
    await alice.get("/api/v1/me")
    user = await _alice()
    start, _ = digest.last_period("year")
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603",
                            watched_at=start + dt.timedelta(days=40)))
        await db.commit()
    assert await _run("year") == 1 and up.mailer.sent[0][1].startswith("Your year in film")


async def test_a_run_missed_while_the_app_was_down_is_made_up_once(alice, up):
    # APScheduler keeps its schedule in memory: a deploy across Monday 09:00
    # used to lose that week's digests. The next start makes the run, once.
    from app import jobs
    from app.models import AppSetting
    async with session() as db:
        assert await digest.missed(db) == []  # the first start takes the latest periods as done: nothing old is sent
        assert {r.key for r in await db.scalars(select(AppSetting))} >= {"digest_done:week", "digest_done:month", "digest_done:year"}
    await alice.get("/api/v1/me")
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        week_before = digest.last_period("week")[0] - dt.timedelta(days=7)
        await digest.mark_done(db, "week", week_before)  # the last run made was for the week before: Monday's was missed
        start, due_at = digest.due("week")
        assert await digest.missed(db, due_at - dt.timedelta(minutes=1)) == []  # not due yet: the schedule will run it
        assert await digest.missed(db, due_at + dt.timedelta(minutes=1)) == ["week"]
    await jobs.digest_catch_up(due_at + dt.timedelta(minutes=1))
    assert len(up.mailer.sent) == 1 and up.mailer.sent[0][0] == "alice@example.org"
    async with session() as db:
        assert await digest.missed(db, due_at + dt.timedelta(minutes=1)) == []
        await digest.mark_done(db, "week", week_before)
    await jobs.digest_catch_up(due_at + dt.timedelta(minutes=1))  # made twice: nothing is sent twice
    assert len(up.mailer.sent) == 1


async def test_a_catch_up_during_the_scheduled_run_sends_nothing_twice(alice, up, monkeypatch):
    # Started a few minutes before Monday 09:00, the catch-up (4 min 20 s
    # after start) can come while the scheduled run is still writing and
    # sending. The two never run at once, so nobody gets the digest twice.
    import asyncio

    from app import jobs
    await alice.get("/api/v1/me")
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
        await digest.mark_done(db, "week", digest.last_period("week")[0] - dt.timedelta(days=7))
    _, due_at = digest.due("week")
    send = up.mailer.send

    async def slow_send(*args, **kwargs):  # an SMTP server that takes its time
        await asyncio.sleep(0.3)
        return await send(*args, **kwargs)

    monkeypatch.setattr(up.mailer, "send", slow_send)
    monkeypatch.setattr(jobs, "_digest_lock", asyncio.Lock())  # this test's own event loop
    await asyncio.gather(jobs.digest_job("week"), jobs.digest_catch_up(due_at + dt.timedelta(minutes=1)))
    assert len(up.mailer.sent) == 1 and up.mailer.sent[0][0] == "alice@example.org"


async def test_one_persons_failed_digest_doesnt_stop_the_others(alice, bob, up, monkeypatch):
    # A failed send rolls the session back, which expires everyone loaded with
    # it: the people after the failing one still get theirs.
    await alice.get("/api/v1/me")
    await bob.get("/api/v1/me")
    async with session() as db:
        for user in await db.scalars(select(User).where(User.username.in_(["alice", "bob"]))):
            user.email = f"{user.username}@example.org"
            db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
    send = up.mailer.send

    async def refuse_alice(to, *args, **kwargs):
        if to.startswith("alice"):
            raise RuntimeError("SMTP refused the address")
        return await send(to, *args, **kwargs)

    monkeypatch.setattr(up.mailer, "send", refuse_alice)
    assert await _run("week") == 1
    assert [to for to, _ in up.mailer.sent] == ["bob@example.org"]


async def test_a_run_whose_sends_failed_is_tried_again_every_hour(alice, bob, up, monkeypatch):
    # Before: the run was recorded as done however many sends failed (SMTP
    # down, say), so those digests never went out. Now it stays not done and
    # the hourly catch-up sends what is still unsent, and nothing twice.
    import asyncio

    from app import jobs
    from app.models import AppSetting
    await alice.get("/api/v1/me")
    await bob.get("/api/v1/me")
    async with session() as db:
        for user in await db.scalars(select(User).where(User.username.in_(["alice", "bob"]))):
            user.email = f"{user.username}@example.org"
            db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
        start, due_at = digest.due("week")
        await digest.mark_done(db, "week", start - dt.timedelta(days=7))  # the week before went out in full
    send = up.mailer.send

    async def refuse_alice(to, *args, **kwargs):
        if to.startswith("alice"):
            raise RuntimeError("SMTP refused the address")
        return await send(to, *args, **kwargs)

    monkeypatch.setattr(up.mailer, "send", refuse_alice)
    monkeypatch.setattr(jobs, "_digest_lock", asyncio.Lock())  # this test's own event loop
    await jobs.digest_job("week")  # the scheduled run
    assert [to for to, _ in up.mailer.sent] == ["bob@example.org"]
    async with session() as db:
        assert (await db.get(AppSetting, digest.DONE_KEY + "week")).value != start.isoformat()
        assert await digest.missed(db, due_at + dt.timedelta(hours=1)) == ["week"]  # not done: tried again
    await jobs.digest_catch_up(due_at + dt.timedelta(hours=1))  # still failing: tried, not done
    assert [to for to, _ in up.mailer.sent] == ["bob@example.org"]
    monkeypatch.setattr(up.mailer, "send", send)  # the failure clears
    await jobs.digest_catch_up(due_at + dt.timedelta(hours=2))
    assert [to for to, _ in up.mailer.sent] == ["bob@example.org", "alice@example.org"]  # Bob's not twice
    async with session() as db:
        assert await digest.missed(db, due_at + dt.timedelta(hours=3)) == []  # done now
    await jobs.digest_catch_up(due_at + dt.timedelta(hours=3))
    assert len(up.mailer.sent) == 2


async def test_the_digest_catch_up_runs_every_hour():
    # Not only once after a start: a failed run is retried by it.
    from apscheduler.triggers.interval import IntervalTrigger

    from app import jobs
    scheduler = jobs.start_scheduler()
    try:
        job = scheduler.get_job("digest-catch-up")
        assert isinstance(job.trigger, IntervalTrigger) and job.trigger.interval == dt.timedelta(hours=1)
        first = job.next_run_time - dt.datetime.now(dt.timezone.utc)
        assert dt.timedelta(minutes=4) < first <= dt.timedelta(minutes=4, seconds=21)  # first 4 min 20 s after start
    finally:
        scheduler.shutdown(wait=False)


def test_the_year_in_reviews_ai_reads_every_film_of_the_year():
    # It used to read the year's best and only 40 more by name, without a word.
    films = [{"title": f"Film {n}", "rating": None} for n in range(120)]
    data = {"kind": "year", "label": "2025", "films": films, "shows": [], "totals": {"films": 120, "episodes": 0, "hours": 240},
            "rated": [], "top_genres": [], "picks": [], "coming": [],
            "year": {"best_films": [{"title": "Film 0", "rating": 9}], "months": [{"films": 10, "episodes": 0}] * 12,
                     "busiest": None, "genres": [], "first_film": None, "last_film": None}}
    import json
    sent = json.loads(digest._ai_input(data))["films"]
    assert sorted(f["title"] for f in sent) == sorted(f["title"] for f in films)


async def test_a_long_subject_from_the_model_ends_with_a_mark():
    from app import clients

    class LongSubject:
        enabled = True
        model = "test-model"

        async def chat_json(self, system, user, attempts=2):
            return {"subject": "A " + "very " * 40 + "long subject"}

    clients.set_overrides(**{**clients._overrides, "litellm": LongSubject()})
    data = {"kind": "week", "label": "6–12 Oct", "films": [], "shows": [], "totals": {"films": 1, "episodes": 0, "hours": 2},
            "rated": [], "top_genres": [], "picks": [], "coming": [], "library": [], "more": {}}
    words, model = await digest.write_words(data)
    assert model == "test-model" and words["subject"].endswith("…") and len(words["subject"]) <= 121


async def _alice_with_a_week(alice) -> User:
    await alice.get("/api/v1/me")
    user = await _alice()
    async with session() as db:
        db.add(HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=_in_last_week()))
        await db.commit()
        await digest.mark_done(db, "week", digest.last_period("week")[0] - dt.timedelta(days=7))
    return user


async def test_a_send_the_server_may_have_taken_is_never_sent_again(alice, up, monkeypatch, caplog):
    # Before: a connection that broke after the email was handed over (a
    # timeout after DATA, a hang-up) counted as a failed send, the run stayed
    # not done, and the hourly catch-up sent the same digest again every
    # hour, though the server had it every time. Through the real mailer and
    # a real SMTP conversation (tests/smtp_server.py).
    import asyncio

    from app import clients, jobs
    from app.clients.mailer import Mailer
    from tests.smtp_server import SMTPServer
    await _alice_with_a_week(alice)
    server = SMTPServer("drop_after_data")
    try:
        mailer = Mailer("127.0.0.1", server.port, "", "", "Movie Night <mn@example.org>", starttls=False)
        clients.set_overrides(**{**clients._overrides, "mailer": mailer})
        monkeypatch.setattr(jobs, "_digest_lock", asyncio.Lock())  # this test's own event loop
        await jobs.digest_job("week")
        _, due_at = digest.due("week")
        for hours in (1, 2, 3):
            await jobs.digest_catch_up(due_at + dt.timedelta(hours=hours))
        assert len(server.received) == 1  # once, not once an hour
    finally:
        server.close()
    async with session() as db:
        d = await db.scalar(select(Digest))
        assert d.sent_at is not None and d.sent_to == "alice@example.org" and d.sending_since is None
        assert await digest.missed(db, due_at + dt.timedelta(hours=4)) == []  # the run is done
    assert any("counted as sent, not sent again" in r.getMessage() for r in caplog.records)


async def test_a_clear_failure_is_tried_at_most_three_times(alice, up, monkeypatch, caplog):
    # Before: no cap, so a send failing every time (an address the server
    # always refuses) was tried every hour until the next period.
    import asyncio
    import logging

    from app import jobs
    from app.core.config import settings
    assert settings.digest_send_tries == 3
    await _alice_with_a_week(alice)
    tried, send = [], up.mailer.send

    async def refuse(to, *args, **kwargs):
        tried.append(to)
        raise ConnectionRefusedError("SMTP down")

    monkeypatch.setattr(up.mailer, "send", refuse)
    monkeypatch.setattr(jobs, "_digest_lock", asyncio.Lock())  # this test's own event loop
    caplog.set_level(logging.INFO)
    _, due_at = digest.due("week")
    await jobs.digest_job("week")  # try 1
    async with session() as db:
        assert await digest.missed(db, due_at + dt.timedelta(hours=1)) == ["week"]  # under the cap: tried again
    for hours in (1, 2, 3, 4):  # tries 2 and 3, then nothing
        await jobs.digest_catch_up(due_at + dt.timedelta(hours=hours))
    assert tried == ["alice@example.org"] * 3
    gave_up = [r for r in caplog.records if "not sent after 3 tries" in r.getMessage()]
    assert len(gave_up) == 1 and gave_up[0].levelno == logging.ERROR
    async with session() as db:
        d = await db.scalar(select(Digest))
        assert d.send_tries == 3 and d.sent_at is None and d.sending_since is None
        assert await digest.missed(db, due_at + dt.timedelta(hours=5)) == []  # given up: the run is done
    # "Send it to me now" is the person's own: it still goes, and isn't counted.
    monkeypatch.setattr(up.mailer, "send", send)
    assert (await alice.post(f"/api/v1/me/digests/{d.id}/send")).json()["sent_to"] == "alice@example.org"
    async with session() as db:
        assert (await db.get(Digest, d.id)).send_tries == 3


async def test_a_send_that_never_reported_back_is_taken_as_sent(alice, up, caplog):
    # The app stopped in the middle of a send (a deploy at 09:00), or the
    # commit after a successful send failed: the digest has a send started
    # and no outcome. It may have gone out, so the next run doesn't send it.
    user = await _alice_with_a_week(alice)
    async with session() as db:
        d = await digest.build(db, user, "week", skip_empty=True)
        started = dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=5)
        d.sending_since, d.send_tries = started, 1
        await db.commit()
    assert await _run("week") == 0 and up.mailer.sent == []
    async with session() as db:
        d = await db.scalar(select(Digest))
        assert d.sent_at == started and d.sending_since is None and d.sent_to == "alice@example.org"
    assert any("never reported back" in r.getMessage() for r in caplog.records)


async def test_send_it_to_me_now_says_when_it_may_have_arrived(alice, up, monkeypatch):
    # The person's own send whose fate is unknown: recorded as sent (so the
    # schedule doesn't send it again) and said in plain words.
    from app.clients.mailer import MaybeSent
    await _alice_with_a_week(alice)
    d = (await alice.post("/api/v1/me/digests/week")).json()
    send = up.mailer.send

    async def lost(*args, **kwargs):
        raise MaybeSent("the connection broke after the email was handed over (TimeoutError)")

    monkeypatch.setattr(up.mailer, "send", lost)
    r = await alice.post(f"/api/v1/me/digests/{d['id']}/send")
    assert r.status_code == 502 and "probably arrived" in r.json()["detail"]
    monkeypatch.setattr(up.mailer, "send", send)
    assert await _run("week") == 0 and up.mailer.sent == []
