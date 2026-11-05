"""The Kodi add-on's own logic, run against stand-ins for Kodi's modules
(tests/kodi_stubs): what it reports, when, and what it keeps for later."""
import datetime as dt
import os
import sys

import pytest

HERE = os.path.dirname(__file__)
sys.path.insert(0, os.path.join(HERE, "kodi_stubs"))
sys.path.insert(0, os.path.join(HERE, "..", "kodi", "service.movienight"))

import xbmc  # noqa: E402
import xbmcaddon  # noqa: E402
import xbmcgui  # noqa: E402

from resources.lib import movienight, service  # noqa: E402

STARTED = dt.datetime(2026, 10, 2, 20, 0, tzinfo=dt.timezone.utc)


def test_library_movie_reports_its_ids():
    r = movienight.build_report({"type": "movie", "title": "The Goonies", "uniqueid": {"tmdb": "9340", "imdb": "tt0089218"}}, None, STARTED)
    assert r["media_type"] == "movie" and r["tmdb_id"] == 9340 and r["imdb_id"] == "tt0089218" and r["source"] == "kodi"
    assert r["ref"] == "kodi:m:9340::%d" % int(STARTED.timestamp())


def test_library_episode_uses_the_shows_ids():
    item = {"type": "episode", "showtitle": "Friends", "season": 4, "episode": 23, "tvshowid": 7, "uniqueid": {"tvdb": "303916"}}
    r = movienight.build_report(item, {"title": "Friends", "uniqueid": {"tvdb": "79168", "imdb": "tt0108778"}}, STARTED)
    assert (r["media_type"], r["season"], r["episode"], r["tvdb_id"], r["imdb_id"]) == ("episode", 4, 23, 79168, "tt0108778")
    assert "tmdb_id" not in r and r["title"] == "Friends"


def test_library_item_without_ids_goes_by_title():
    r = movienight.build_report({"type": "movie", "title": "Home Video Night"}, None, STARTED)
    assert r["title"] == "Home Video Night" and "tmdb_id" not in r


def test_a_stream_needs_an_id():
    assert movienight.build_report({"type": "unknown", "title": "Some YouTube clip"}, None, STARTED) is None
    r = movienight.build_report({"type": "unknown", "title": "Mud", "uniqueid": {"tmdb": "103731"}}, None, STARTED)
    assert r["media_type"] == "movie" and r["tmdb_id"] == 103731


def test_only_far_enough_and_long_enough_counts():
    assert not movienight.progress_done(250, 280, 85)  # a trailer
    assert not movienight.progress_done(84 * 60, 100 * 60, 85)
    assert movienight.progress_done(85 * 60, 100 * 60, 85)


def test_queue_keeps_order_and_drops_refusals(tmp_path, monkeypatch):
    q = movienight.Queue(str(tmp_path / "queue.json"))
    for n in (1, 2, 3):
        q.add({"ref": f"r{n}"})
    q.add({"ref": "r1"})  # the same play twice is kept once
    answers = {"r1": 200, "r2": 422, "r3": 0}
    monkeypatch.setattr(movienight, "http", lambda method, url, body, token: (answers[body["ref"]], {}))
    assert movienight.flush("https://mn.test", "tok", q) == 1  # r1 sent, r2 refused for good, r3 waits
    assert [i["ref"] for i in q.load()] == ["r3"]
    assert movienight.send("https://mn.test", "", {"ref": "x"})[0] == "retry"  # not connected yet


def test_watcher_reports_once_past_the_point(tmp_path, monkeypatch):
    xbmc.rpc_replies.update({
        "Player.GetActivePlayers": [{"playerid": 1, "type": "video"}],
        "Player.GetItem": {"item": {"type": "movie", "title": "The Goonies", "uniqueid": {"tmdb": "9340"}}},
    })
    sent = []
    monkeypatch.setattr(movienight, "send", lambda server, token, report: (sent.append(report), ("sent", "The Goonies"))[1])
    w = service.Watcher(movienight.Queue(str(tmp_path / "q.json")))
    w.onAVStarted()
    xbmc.Player.total = 114 * 60
    xbmc.Player.time = 60 * 60
    w.tick()
    assert sent == []
    xbmc.Player.time = 100 * 60
    w.tick()
    w.tick()
    w.onPlayBackStopped()
    assert len(sent) == 1 and sent[0]["tmdb_id"] == 9340
    assert "Added The Goonies to your history" in xbmcgui.shown


def test_flush_stops_and_signals_on_a_signed_out_token(tmp_path, monkeypatch):
    q = movienight.Queue(str(tmp_path / "queue.json"))
    for n in (1, 2):
        q.add({"ref": f"r{n}"})
    monkeypatch.setattr(movienight, "http", lambda method, url, body, token: (401, {"detail": "invalid token"}))
    calls = []
    sent = movienight.flush("https://mn.test", "tok", q, on_signed_out=calls.append)
    assert sent == 0 and calls == ["invalid token"]  # Movie Night's own words, for the TV to show
    assert [i["ref"] for i in q.load()] == ["r1", "r2"]  # neither sent nor dropped; waits for a new token


def test_flush_never_sends_one_persons_waiting_reports_to_another(tmp_path, monkeypatch):
    q = movienight.Queue(str(tmp_path / "queue.json"))
    q.add({"ref": "alice-1", "owner": "alice"})
    q.add({"ref": "nobody-1", "owner": ""})  # kept while nobody was connected
    q.add({"ref": "bob-1", "owner": "bob"})
    bodies = []
    monkeypatch.setattr(movienight, "http", lambda method, url, body, token: (bodies.append(body), (200, {}))[1])
    sent = movienight.flush("https://mn.test", "bobs-token", q, account="bob")
    assert sent == 2 and q.load() == []
    assert [b["ref"] for b in bodies] == ["nobody-1", "bob-1"]  # alice's never goes into bob's history
    assert all("owner" not in b for b in bodies)  # the tag stays on this Kodi


def test_watcher_signs_out_and_keeps_the_report_on_a_401(tmp_path, monkeypatch):
    xbmc.rpc_replies.update({
        "Player.GetActivePlayers": [{"playerid": 1, "type": "video"}],
        "Player.GetItem": {"item": {"type": "movie", "title": "Mud", "uniqueid": {"tmdb": "103731"}}},
    })
    monkeypatch.setattr(movienight, "http", lambda *a, **k: (401, {"detail": "invalid token"}))
    xbmcaddon.settings.update({"account": "alice", "user": "Alice"})
    q = movienight.Queue(str(tmp_path / "q.json"))
    w = service.Watcher(q)
    w.onAVStarted()
    xbmc.Player.total, xbmc.Player.time = 130 * 60, 125 * 60
    w.tick()
    w.onPlayBackEnded()
    kept = q.load()
    assert len(kept) == 1 and kept[0]["tmdb_id"] == 103731  # goes out once paired again
    assert kept[0]["owner"] == "alice"  # by username, not the name shown
    assert xbmcaddon.settings["token"] == "" and xbmcaddon.settings["user"] == "" and xbmcaddon.settings["account"] == ""
    assert "invalid token. Open the Movie Night add-on to connect it again." in xbmcgui.shown  # the server's words first


def test_watcher_keeps_reports_it_cannot_send(tmp_path, monkeypatch):
    xbmc.rpc_replies.update({
        "Player.GetActivePlayers": [{"playerid": 1, "type": "video"}],
        "Player.GetItem": {"item": {"type": "movie", "title": "Mud", "uniqueid": {"tmdb": "103731"}}},
    })
    monkeypatch.setattr(movienight, "http", lambda *a, **k: (0, {"detail": "offline"}))
    q = movienight.Queue(str(tmp_path / "q.json"))
    w = service.Watcher(q)
    w.onAVStarted()
    xbmc.Player.total, xbmc.Player.time = 130 * 60, 125 * 60
    w.tick()
    w.onPlayBackEnded()
    assert w.current is None
    kept = q.load()
    assert len(kept) == 1 and kept[0]["tmdb_id"] == 103731  # goes out with the next flush
    monkeypatch.setattr(movienight, "http", lambda *a, **k: (200, {"recorded": True}))
    assert movienight.flush("https://mn.test", "mn_kodi_x", q) == 1 and q.load() == []


@pytest.fixture(autouse=True)
def _reset():
    xbmc.Player.time = xbmc.Player.total = 0.0
    xbmcgui.shown.clear()
    xbmcaddon.settings.update({"token": "mn_kodi_x", "threshold": "85", "notify": "true"})
    yield


def test_every_waiting_report_is_kept(tmp_path):
    """No cap: the old 500 (oldest dropped without a word) is gone."""
    q = movienight.Queue(str(tmp_path / "queue.json"))
    for n in range(1, 1201):
        q.add({"ref": f"r{n}"})
    kept = q.load()
    assert len(kept) == 1200 and kept[0]["ref"] == "r1"


def test_a_dropped_report_is_said_on_the_tv_and_not_logged_by_title(tmp_path, monkeypatch):
    q = movienight.Queue(str(tmp_path / "queue.json"))
    q.add({"ref": "r1", "title": "A Very Private Film"})
    q.add({"ref": "r2", "title": "Someone Else's Film", "owner": "alice"})
    monkeypatch.setattr(movienight, "http", lambda method, url, body, token: (422, {"detail": "watched_at is in the future"}))
    logged = []
    monkeypatch.setattr(xbmc, "log", lambda message, level=0: logged.append(message), raising=False)
    xbmcgui.shown.clear()
    assert movienight.flush("https://mn.test", "tok", q, account="bob", on_dropped=service.dropped) == 0
    assert q.load() == []
    assert "Movie Night couldn't add A Very Private Film: watched_at is in the future" in xbmcgui.shown
    assert any("Someone Else's Film" in m and "another account" in m for m in xbmcgui.shown)
    assert logged and not any("Private" in m or "Someone" in m for m in logged)


def test_a_sign_in_that_ran_out_is_said_in_movie_nights_words(tmp_path, monkeypatch):
    # The server's 401 says why (90 days unused); the TV shows that, not only
    # "open the add-on", and the add-on says it again when it is opened.
    why = "This TV's sign-in ran out: it wasn't used for 90 days. Sign in again."
    monkeypatch.setattr(movienight, "http", lambda *a, **k: (401, {"detail": why}))
    xbmcaddon.settings.update({"token": "mn_kodi_x", "account": "alice", "user": "Alice"})
    q = movienight.Queue(str(tmp_path / "q.json"))
    q.add({"ref": "r1", "owner": "alice"})
    xbmcgui.shown.clear()
    movienight.flush("https://mn.test", "mn_kodi_x", q, on_signed_out=service.sign_out, account="alice")
    told = "This TV's sign-in ran out: it wasn't used for 90 days. Open the Movie Night add-on to connect it again."
    assert xbmcgui.shown == [told] and xbmcaddon.settings["token"] == ""
    assert xbmc.logged and "it wasn't used for 90 days" in xbmc.logged[-1]
    from resources.lib import connect
    monkeypatch.setattr(movienight, "http", lambda *a, **k: (0, {"detail": "offline"}))
    connect.run()  # opened again: the reason first, then the pairing (here: Movie Night can't be reached)
    assert xbmcgui.shown[1] == told and xbmcaddon.settings["signed_out"] == ""
