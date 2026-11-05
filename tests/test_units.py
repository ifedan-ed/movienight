"""Pure functions: rotation, parsing, filters, digest periods."""
import datetime as dt
from zoneinfo import ZoneInfo

from app.clients.litellm import parse_json
from app.clients.mdblist import parse_list_url, ratings_of
from app.services import digest, lists, recommend, rows

NY = ZoneInfo("America/New_York")


def pool(n, recent=0):
    now = dt.datetime(2026, 10, 2, 20, tzinfo=dt.timezone.utc)
    return [("movie", i, f"T{i}", now - dt.timedelta(hours=1 if i < recent else 72 + i)) for i in range(n)]


def test_because_sets_mix_recent_and_older_and_pin_the_last_day():
    p = pool(15)
    sets = [recommend.because_set(p, k, now=dt.datetime(2026, 10, 2, 20, tzinfo=dt.timezone.utc)) for k in range(3)]
    assert [[t for _, t, _ in s] for s in sets] == [[0, 3, 6, 9, 12], [1, 4, 7, 10, 13], [2, 5, 8, 11, 14]]
    p = pool(15, recent=1)  # title 0 watched an hour ago leads every set
    sets = [recommend.because_set(p, k, now=dt.datetime(2026, 10, 2, 20, tzinfo=dt.timezone.utc)) for k in range(3)]
    assert all(s[0][1] == 0 and len(s) == 5 for s in sets)


def test_ai_sets():
    r = [{"theme": str(i), "keys": []} for i in range(15)]
    assert [x["theme"] for x in recommend.ai_set(r, 1)] == ["5", "6", "7", "8", "9"]
    assert recommend.ai_set(r[:4], 2) == r[:4]


def test_rotation_morning_noon_night_moves_on_each_day():
    at = lambda d, h: dt.datetime(2026, 10, d, h, tzinfo=NY)  # noqa: E731
    day1 = [rows.rotation_index(at(2, h)) for h in (8, 14, 21)]
    day2 = [rows.rotation_index(at(3, h)) for h in (8, 14, 21)]
    assert sorted(day1) == [0, 1, 2] and sorted(day2) == [0, 1, 2]
    assert day2 == [(i + 1) % 3 for i in day1]
    assert rows.rotation_index(at(3, 2)) == rows.rotation_index(at(2, 23))  # the small hours belong to the evening before
    nights = {rows.rotation_index(at(d, 21)) for d in (2, 3, 4)}
    assert nights == {0, 1, 2}  # someone who only opens it at night sees every set in three days


def test_mdblist_parsing():
    assert parse_list_url("https://mdblist.com/lists/gary/latest-tv-shows") == {"user": "gary", "slug": "latest-tv-shows"}
    assert parse_list_url("mdblist.com/lists/a/b") == {"user": "a", "slug": "b"}
    assert parse_list_url("2194") == {"id": "2194"}
    assert parse_list_url("https://evil.example/lists/a/b") is None
    r = ratings_of({"score": 82, "ratings": [{"source": "tomatoes", "value": None, "score": 83, "url": "/m/x"},
                                             {"source": "trakt", "value": 87, "votes": 5}, {"source": "letterboxd", "value": 4.2}]})
    assert r["rt_critics"] == 83 and r["trakt_rating"] == 8.7 and r["letterboxd"] == 4.2


def test_model_json_is_parsed_through_fences():
    assert parse_json('```json\n{"rows": []}\n```') == {"rows": []}
    assert parse_json('Here you go: {"a": 1} thanks') == {"a": 1}


def test_smart_filters_to_tmdb():
    f = lists.clean_filters({"media": "tv", "genre": "18", "year_from": "1990", "min_rating": "7", "sort": "primary_release_date.desc",
                             "language": "ko", "provider": "x"})
    p = lists.tmdb_filters(f, "US")
    assert p == {"with_genres": "18", "first_air_date.gte": "1990-01-01", "vote_average.gte": 7.0, "vote_count.gte": 100,
                 "with_original_language": "ko", "sort_by": "first_air_date.desc"}


def test_digest_periods():
    now = dt.datetime(2026, 10, 2, 15, tzinfo=NY)
    s, e = digest.last_period("week", now)
    assert (s.date().isoformat(), e.date().isoformat()) == ("2026-09-21", "2026-09-28")
    s, e = digest.last_period("month", now)
    assert (s.date().isoformat(), e.date().isoformat()) == ("2026-09-01", "2026-10-01")
    assert digest.period_label("month", s, e) == "September 2026"
