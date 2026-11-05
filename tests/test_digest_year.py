"""The year in review: last calendar year summed up — month by month, the
busiest month, the best-rated films, the most-watched shows, the first and
the last film — and offered beside the weekly and monthly emails."""
import datetime as dt

from sqlalchemy import select

from app.core.db import session
from app.models import HistoryEntry, User, UserRating
from app.services import digest


def at(month: int, day: int = 10) -> dt.datetime:
    return dt.datetime(2025, month, day, 20, tzinfo=dt.timezone.utc)


async def _year_of_watching(username: str = "alice") -> None:
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == username))
        db.add_all([
            HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=603, title="Movie 603", watched_at=at(2)),
            HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=604, title="Movie 604", watched_at=at(3)),
            HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=605, title="Movie 605", watched_at=at(11)),
            *[HistoryEntry(user_id=user.id, media_type="episode", tmdb_id=1399, season=1, episode=e, title="Show 1399",
                           watched_at=at(3, 10 + e)) for e in range(1, 6)],
            # The year before and the year after are not in it.
            HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=606, title="Movie 606", watched_at=dt.datetime(2024, 12, 31, 12, tzinfo=dt.timezone.utc)),
            HistoryEntry(user_id=user.id, media_type="movie", tmdb_id=607, title="Movie 607", watched_at=dt.datetime(2026, 1, 2, 12, tzinfo=dt.timezone.utc)),
            UserRating(user_id=user.id, media_type="movie", tmdb_id=604, rating=9, rated_at=at(3)),
            UserRating(user_id=user.id, media_type="movie", tmdb_id=603, rating=7, rated_at=at(2)),
        ])
        await db.commit()


def test_last_year_is_the_period():
    now = dt.datetime(2026, 1, 1, 10, 30, tzinfo=dt.timezone.utc)
    s, e = digest.last_period("year", now)
    assert (s.date().isoformat(), e.date().isoformat()) == ("2025-01-01", "2026-01-01")
    assert digest.period_label("year", s, e) == "2025"


async def test_the_year_summed_up(alice):
    await alice.get("/api/v1/me")
    await _year_of_watching()
    start, end = digest.last_period("year", dt.datetime(2026, 1, 1, 10, tzinfo=dt.timezone.utc))
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        data = await digest.gather(db, user, "year", start, end)
        words, _ = await digest.write_words(data)
        page, text = digest.render(data, words)
    y = data["year"]
    assert data["totals"]["films"] == 3 and data["totals"]["episodes"] == 5
    assert [m["films"] + m["episodes"] for m in y["months"]] == [0, 1, 6, 0, 0, 0, 0, 0, 0, 0, 1, 0]
    assert y["busiest"] == "March"
    assert [f["title"] for f in y["best_films"]] == ["Movie 604", "Movie 603"]
    assert y["first_film"]["title"] == "Movie 603" and y["last_film"]["title"] == "Movie 605"
    assert [s["title"] for s in y["top_shows"]] == ["Show 1399"] and y["top_shows"][0]["episodes"] == 5
    assert "Your year in review" in page and "Month by month" in page and "Your best films" in page
    assert "Most watched shows" in page and "First and last" in page
    assert "Movie 606" not in page and "Movie 607" not in page  # other years
    assert "Busiest month: March" in text and "- Movie 604: 9/10" in text


async def test_offered_and_switchable_beside_the_others(alice):
    got = (await alice.get("/api/v1/me/emails")).json()
    assert got["yearly"] is True  # on unless switched off, like the others
    assert (await alice.put("/api/v1/me/emails", json={"yearly": False})).json()["yearly"] is False
    assert (await alice.get("/api/v1/me/emails")).json()["yearly"] is False
    made = await alice.post("/api/v1/me/digests/year")
    assert made.status_code == 200 and made.json()["kind"] == "year"
    page = await alice.get(f"/api/v1/me/digests/{made.json()['id']}/html")
    assert "Your year in review" in page.text
