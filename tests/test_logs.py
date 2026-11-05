"""The log keeps only what debugging needs: request ids, route patterns,
status codes, timings, error messages and people's numeric ids. Never search
text, titles watched, names, usernames, email addresses or query strings
(app/core/logs.py)."""
import logging

from app.core import logs

SEARCH = "kids with a treasure map trying to save their homes"


def everything(caplog) -> str:
    """Every line as the server would write it (formatter included)."""
    fmt = logs.RedactingFormatter()
    return "\n".join(fmt.format(r) for r in caplog.records)


async def test_one_access_line_per_request_without_the_query_or_the_person(alice, caplog):
    caplog.set_level(logging.INFO)
    me = (await alice.get("/api/v1/me")).json()
    r = await alice.get("/api/v1/search", params={"q": SEARCH})
    assert r.status_code == 200 and len(r.headers["x-request-id"]) == 12
    lines = [rec.getMessage() for rec in caplog.records if rec.name == "movienight.access"]
    assert any(line.startswith("GET /api/v1/search 200 ") for line in lines), lines
    assert all("user=" in line for line in lines)
    uid = next(line for line in lines if "/api/v1/search" in line).rsplit("user=", 1)[1]
    assert uid.isdigit()
    text = everything(caplog)
    for secret in ("treasure", "alice", "alice@example.org", me["user"]):
        assert secret not in text, secret


async def test_a_title_page_is_logged_by_its_route_not_its_ids(alice, caplog):
    caplog.set_level(logging.INFO)
    assert (await alice.get("/api/v1/title/movie/603")).status_code == 200
    lines = [rec.getMessage() for rec in caplog.records if rec.name == "movienight.access"]
    assert any(line.startswith("GET /api/v1/title/{media}/{tmdb_id} 200 ") for line in lines), lines
    assert "/movie/603" not in "\n".join(lines)


async def test_a_reported_watch_is_logged_without_its_title_or_whose_it_is(alice, caplog):
    caplog.set_level(logging.INFO)
    token = (await alice.post("/api/v1/me/scrobble-token")).json()["token"]
    from tests.conftest import client
    async with client(csrf=False, Authorization=f"Bearer {token}") as kodi:
        r = await kodi.post("/api/v1/scrobble", json={"media_type": "movie", "title": "A Very Private Film", "source": "kodi",
                                                      "ref": "x1"})
    assert r.status_code == 200 and r.json()["recorded"] is True
    text = everything(caplog)
    assert "A Very Private Film" not in text and "alice" not in text
    assert "scrobble from kodi for user " in text


def test_query_strings_emails_and_row_values_are_cut_from_every_line():
    tmdb_error = ("Client error '401 Unauthorized' for url "
                  "'https://api.themoviedb.org/3/search/multi?query=kids+treasure+map&api_key=SECRETKEY'")
    out = logs.redact(tmdb_error)
    assert "treasure" not in out and "SECRETKEY" not in out and "https://api.themoviedb.org/3/search/multi?…" in out
    assert logs.redact("GET /api/v1/search?q=kids%20treasure HTTP/1.1") == "GET /api/v1/search?… HTTP/1.1"
    assert logs.redact("digest sent to alice@example.org") == "digest sent to [email]"
    assert "alice" not in logs.redact('DETAIL:  Key (username)=(alice) already exists.')


def test_a_traceback_is_redacted_too():
    try:
        raise RuntimeError("failed for url 'https://jf.test/Users/x/Items?api_key=SECRETKEY&searchTerm=private'")
    except RuntimeError:
        import sys
        record = logging.LogRecord("movienight.test", logging.ERROR, __file__, 1, "upstream failed", None, sys.exc_info())
    out = logs.RedactingFormatter().format(record)
    assert "RuntimeError" in out and "SECRETKEY" not in out and "private" not in out


def test_uvicorns_own_access_line_is_off():
    uvicorn_access = logging.getLogger("uvicorn.access")
    assert uvicorn_access.disabled or not uvicorn_access.filter(
        logging.LogRecord("uvicorn.access", logging.INFO, __file__, 1, "%s", ("GET /api/v1/search?q=x",), None))


async def test_a_database_errors_failing_row_is_cut_too():
    # Postgres quotes the whole row in a NOT NULL or CHECK violation ("Failing
    # row contains (...)"), with names, titles and comments in it; SQLAlchemy's
    # hide_parameters doesn't touch that DETAIL. The real error, from the real
    # database, as a traceback would show it.
    import sys

    import pytest
    from sqlalchemy import select
    from sqlalchemy.exc import IntegrityError

    from app.core.db import session
    from app.models import User, UserRating

    async with session() as db:
        db.add(User(username=None, display_name="Alice Private", email="alice@example.org"))
        with pytest.raises(IntegrityError) as caught:
            await db.commit()
        await db.rollback()
        raw = str(caught.value)
        assert "Failing row contains" in raw and "Alice Private" in raw  # what Postgres really sends
        assert "Alice Private" not in logs.redact(raw) and "Failing row contains (…)" in logs.redact(raw)

        db.add(User(username="bob"))
        await db.commit()
        bob = await db.scalar(select(User.id).where(User.username == "bob"))
        db.add(UserRating(user_id=bob, media_type="movie", tmdb_id=603, rating=None,
                          review="A Very Private Film (seen twice).\nMy own words."))
        try:
            await db.commit()
        except IntegrityError:
            record = logging.LogRecord("movienight.test", logging.ERROR, __file__, 1, "saving failed", None, sys.exc_info())
        await db.rollback()
    out = logs.RedactingFormatter().format(record)
    assert "IntegrityError" in out and "Failing row contains (…)" in out
    for secret in ("Private", "own words", "seen twice"):
        assert secret not in out, out


def test_other_quoted_database_values_are_cut():
    assert "Batman" not in logs.redact('invalid input syntax for type integer: "Batman Begins"')
    assert "alice" not in logs.redact("Key (a)=(1) conflicts with existing key (a)=(alice).")
