"""Talking to Movie Night: the add-on's settings, the scrobble call, what to
report for the item that is playing, and a queue for reports that could not
be sent (no network, server down), retried later.

Kept free of Kodi calls, so it can be tested without Kodi.
"""
import datetime as dt
import json
import os
import urllib.error
import urllib.request

DEFAULT_SERVER = "https://movienight.example.com"
USER_AGENT = "MovieNight-Kodi/1.0"
# The add-on's timers are listed in Movie Night's docs/limits.md (Kodi add-on).
# Every report that can't be sent is kept until it can (no cap on how many).
TIMEOUT = 15  # seconds for each call to Movie Night
MIN_LENGTH = 5 * 60  # seconds; trailers and extras are not reported


def http(method, url, body=None, token=None):
    """(status, JSON body); status 0 when the server could not be reached."""
    headers = {"Content-Type": "application/json", "Accept": "application/json", "User-Agent": USER_AGENT}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw.decode("utf-8")) if raw else {})
    except urllib.error.HTTPError as exc:
        try:
            payload = json.loads(exc.read().decode("utf-8") or "{}")
        except ValueError:
            payload = {}
        return exc.code, payload if isinstance(payload, dict) else {}
    except (urllib.error.URLError, OSError, ValueError) as exc:
        return 0, {"detail": str(getattr(exc, "reason", exc))}


def server_url(value):
    url = (value or "").strip().rstrip("/") or DEFAULT_SERVER
    if not url.startswith(("http://", "https://")):
        url = "https://" + url
    return url


def _id(value):
    text = str(value or "").strip()
    return int(text) if text.isdigit() and int(text) > 0 else None


def build_report(item, show=None, started_at=None):
    """What to send for a finished movie or episode, from Kodi's
    Player.GetItem (and the show's details for an episode); None when there
    is nothing to identify it by. Items from Kodi's library may go by title
    alone; anything else (a stream, a home video) needs a TMDB, IMDb or TVDB
    id, so random videos never land in the history."""
    kind = item.get("type") or "unknown"
    season, episode = item.get("season"), item.get("episode")
    is_episode = kind == "episode" or (kind != "movie" and bool(item.get("showtitle")) and _is_num(season) and _is_num(episode))
    if is_episode:
        if not (_is_num(season) and _is_num(episode)):
            return None
        ids = dict((show or {}).get("uniqueid") or {})
        if kind != "episode":
            ids.update({k: v for k, v in (item.get("uniqueid") or {}).items() if v})
        report = {"media_type": "episode", "season": int(season), "episode": int(episode),
                  "title": item.get("showtitle") or (show or {}).get("title") or None}
    else:
        ids = dict(item.get("uniqueid") or {})
        report = {"media_type": "movie", "title": item.get("title") or item.get("label") or None}
    tmdb, tvdb = _id(ids.get("tmdb")), _id(ids.get("tvdb"))
    imdb = str(ids.get("imdb") or "").strip()
    if tmdb:
        report["tmdb_id"] = tmdb
    if imdb.startswith("tt") and imdb[2:].isdigit():
        report["imdb_id"] = imdb
    if tvdb and is_episode:
        report["tvdb_id"] = tvdb
    has_id = bool(tmdb or report.get("imdb_id") or report.get("tvdb_id"))
    from_library = kind in ("movie", "episode")
    if not has_id and not (from_library and report.get("title")):
        return None
    if not report.get("title"):
        report.pop("title", None)
    started = started_at or dt.datetime.now(dt.timezone.utc)
    report["watched_at"] = dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat()
    report["source"] = "kodi"
    who = tmdb or report.get("imdb_id") or report.get("tvdb_id") or report.get("title")
    where = "%s:%s" % (season, episode) if is_episode else ""
    report["ref"] = ("kodi:%s:%s:%s:%d" % (report["media_type"][0], who, where, int(started.timestamp())))[:100]
    return report


def _is_num(value):
    try:
        return int(value) >= 0
    except (TypeError, ValueError):
        return False


class Queue:
    """Reports waiting to be sent, kept in the add-on's profile folder so a
    restart doesn't lose them."""

    def __init__(self, path):
        self.path = path

    def load(self):
        try:
            with open(self.path, "r", encoding="utf-8") as f:
                items = json.load(f)
            return items if isinstance(items, list) else []
        except (OSError, ValueError):
            return []

    def save(self, items):
        os.makedirs(os.path.dirname(self.path), exist_ok=True)
        tmp = self.path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(items, f)
        os.replace(tmp, self.path)

    def add(self, report):
        items = self.load()
        if all(i.get("ref") != report.get("ref") for i in items):
            items.append(report)
            self.save(items)


def send(server, token, report):
    """'sent', 'retry' (keep it for later), 'signed_out' (the token no
    longer works; keep it for later too, once reconnected) or 'drop' (Movie
    Night refused it for good), with the server's detail."""
    if not token:
        return "retry", "Not connected to Movie Night yet"
    status, body = http("POST", server_url(server) + "/api/v1/scrobble", report, token)
    detail = body.get("detail") if isinstance(body, dict) else None
    if status == 200:
        return "sent", detail
    if status == 401:
        return "signed_out", detail or "Movie Night signed this out"
    if status in (0, 408, 429) or status >= 500:
        return "retry", detail or ("No answer from Movie Night" if status == 0 else "Movie Night answered %d" % status)
    return "drop", detail or "Movie Night refused it (%d)" % status


def flush(server, token, queue, on_signed_out=None, account="", on_dropped=None):
    """Send what is waiting; returns how many went through. Stops at the
    first one that has to wait, so the order is kept. on_signed_out, if
    given, is called (once) when the token turns out not to work any more;
    whatever hasn't been sent stays in the queue either way.

    on_signed_out gets Movie Night's own words for why ("This TV's sign-in
    ran out: it wasn't used for 90 days. Sign in again.").

    A report kept while this Kodi was connected names that account's
    username ("owner"); once another account is connected (`account`) it is
    dropped, never sent into someone else's history. One kept while nobody
    was connected goes to whoever connects. Nothing is dropped without a
    word: on_dropped(report, why), if given, is told of each report dropped
    (Movie Night refused it for good, or it was someone else's)."""
    items = queue.load()
    sent = 0
    while items:
        owner = items[0].get("owner")
        if owner and account and owner != account:
            dropped = items.pop(0)
            if on_dropped:
                on_dropped(dropped, "it was kept for another account, and a different one is connected now")
            continue
        report = {k: v for k, v in items[0].items() if k != "owner"}
        outcome, detail = send(server, token, report)
        if outcome == "signed_out":
            if on_signed_out:
                on_signed_out(detail)
            break
        if outcome == "retry":
            break
        items.pop(0)
        if outcome == "drop" and on_dropped:
            on_dropped(report, detail)
        sent += outcome == "sent"
    queue.save(items)
    return sent


def progress_done(position, total, threshold):
    """Has enough of it played to count as watched?"""
    if not total or total < MIN_LENGTH:
        return False
    return position / total * 100.0 >= threshold
