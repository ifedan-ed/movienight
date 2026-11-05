"""Play in VLC, Infuse, Outplayer, Jellyfin or a player the person adds.

The link hands the player the original file from Jellyfin (the person's own
Jellyfin token in the URL, as Jellyfin's apps do) and the chosen subtitle as
its own file, which the player loads as a normal track. Only the original
file is ever sent: a stream remuxed on the fly has no file size and can't be
seeked, and VLC and Infuse refuse it ("Server didn't report the size of the
file").

Each Play starts a play session. "Finished it?" (a yes records the watch and
marks it played in Jellyfin) is asked once at most per Play, and only once the
film could have ended: 60% of its runtime after Play, at least ten minutes.
Starting another film closes the one before without a question, and so does
the player sending the person back sooner. It can be switched off.
"""
from __future__ import annotations

import datetime as dt
import re
import secrets
from typing import Any
from urllib.parse import quote

from sqlalchemy import select, update
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.core import cache
from app.core.config import settings
from app.models import PlaySession, User
from app.services import episodes, history, titles, users
from app.services.state import library_map

PLAYERS_KEY = "players"
DEFAULT_PLAYER_KEY = "default_player"
ENABLED_KEY = "enabled_players"  # which players the Play sheet offers
SUB_LANG_KEY = "subtitle_language"
ASK_KEY = "ask_finished"  # ask "Finished it?" after a Play (on unless switched off)

GRACE = dt.timedelta(minutes=3)  # a return this soon is the player still opening
MIN_DUE = dt.timedelta(minutes=10)
DUE_SHARE = 0.6  # of the runtime: when the film could have ended
DEFAULT_MINUTES = {"movie": 100, "episode": 30}

# No link back to Movie Night ({done}) for the built-in players: Infuse calls
# a film's link back when the next film replaces it, which pulled people out
# of the film they had just started and into the browser. "Finished it?" is
# asked when they come back by themselves. A player someone adds can still
# use {done}.
BUILTIN_PLAYERS: dict[str, dict[str, str]] = {
    "vlc": {"name": "VLC", "template": "vlc-x-callback://x-callback-url/stream?url={url}&sub={sub}"},
    "infuse": {"name": "Infuse", "template": "infuse://x-callback-url/play?url={url}&sub={sub}&filename={filename}"},
    "outplayer": {"name": "Outplayer", "template": "outplayer://x-callback-url/play?url={url}&sub={sub}"},
    "vlc_android": {"name": "VLC (Android)", "template": "intent://{url_noscheme}#Intent;scheme={url_scheme};type=video/*;package=org.videolan.vlc;S.title={title};S.subtitles_location={sub};end"},
    "jellyfin": {"name": "Jellyfin", "template": "{web}"},
    # Swiftfin (the Jellyfin app for iPhone, iPad and Apple TV) opens a title
    # from swiftfin://<server id>/<user id>/item/<item id> (since 1.5), and
    # Streamyfin (iPhone and Android) from streamyfin://items/page?id=<item id>.
    # The official Jellyfin apps have no such link, so "Jellyfin" opens the
    # website.
    "swiftfin": {"name": "Swiftfin", "template": "{swiftfin}"},
    "streamyfin": {"name": "Streamyfin", "template": "streamyfin://items/page?id={jellyfin_id}"},
}
# Apps that play through Jellyfin itself: Jellyfin records those plays, so
# nobody is asked "Finished it?" about them.
JELLYFIN_APPS = ("jellyfin", "swiftfin", "streamyfin")
BLOCKED_SCHEMES = {"javascript", "data", "vbscript", "file", "about", "blob"}
PLACEHOLDERS = {"url", "url_raw", "url_noscheme", "url_scheme", "sub", "sub_raw", "title", "filename", "done", "web", "swiftfin", "jellyfin_id"}


class PlayError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


SAMPLE = {"url": "https%3A%2F%2Fexample.org%2Fv.mkv", "url_raw": "https://example.org/v.mkv", "url_noscheme": "example.org/v.mkv",
          "url_scheme": "https", "sub": "", "sub_raw": "", "title": "Title", "filename": "Title.mkv", "done": "", "web": "https://example.org/web",
          "swiftfin": "swiftfin://server/user/item/item", "jellyfin_id": "0123456789abcdef0123456789abcdef"}


def check_template(template: str) -> str:
    """A custom player link: an app URL scheme with {placeholders}, checked
    as the finished link (nPlayer's is nplayer-{url_raw}, for example)."""
    t = (template or "").strip()
    unknown = set(re.findall(r"\{([a-z_]+)\}", t)) - PLACEHOLDERS
    if unknown:
        raise PlayError(422, f"Unknown placeholder: {{{sorted(unknown)[0]}}}")
    if "{url" not in t and "{web}" not in t:
        raise PlayError(422, "The link needs {url} (the video) somewhere")
    if len(t) > 1000:
        raise PlayError(422, "A player link can be at most 1,000 characters")
    m = re.match(r"^([a-zA-Z][a-zA-Z0-9+.\-]*):", _fill(t, SAMPLE))
    if not m or m.group(1).lower() in BLOCKED_SCHEMES:
        raise PlayError(422, "A player link starts with the app's scheme, like vlc:// or infuse://")
    return t


async def player_settings(db: AsyncSession, user: User) -> dict[str, Any]:
    custom = await users.get_setting(db, user.id, PLAYERS_KEY, []) or []
    players = [{"id": pid, "name": p["name"], "builtin": True} for pid, p in BUILTIN_PLAYERS.items()]
    players += [{"id": p["id"], "name": p["name"], "builtin": False, "template": p["template"]} for p in custom]
    ids = [p["id"] for p in players]
    enabled = [i for i in (await users.get_setting(db, user.id, ENABLED_KEY) or ids) if i in ids] or ids
    for p in players:
        p["enabled"] = p["id"] in enabled
    default = await users.get_setting(db, user.id, DEFAULT_PLAYER_KEY, "vlc")
    if default not in enabled:
        default = enabled[0]
    return {"players": players, "default_player": default,
            "subtitle_language": await users.get_setting(db, user.id, SUB_LANG_KEY, "eng"),
            "ask_finished": await asks(db, user)}


async def save_player_settings(db: AsyncSession, user: User, custom: list[dict[str, str]], default: str | None,
                               subtitle_language: str | None, enabled: list[str] | None = None,
                               ask_finished: bool | None = None) -> dict[str, Any]:
    clean = []
    # A limit the owner keeps (docs/limits.md): at most 20 players of your
    # own and 30 ticked (the API refuses more first, app/api/schemas.py).
    for i, p in enumerate(custom[:20]):
        name = (p.get("name") or "").strip()
        if not name:
            raise PlayError(422, "Each player needs a name")
        clean.append({"id": f"custom{i + 1}", "name": name, "template": check_template(p.get("template", ""))})
    await users.set_setting(db, user.id, PLAYERS_KEY, clean)
    if default:
        await users.set_setting(db, user.id, DEFAULT_PLAYER_KEY, default)
    if enabled is not None:
        await users.set_setting(db, user.id, ENABLED_KEY, [str(i) for i in enabled[:30]])
    if subtitle_language is not None:
        lang = subtitle_language.strip().lower()[:3]
        await users.set_setting(db, user.id, SUB_LANG_KEY, lang if lang.isalpha() else "")
    if ask_finished is not None:
        await users.set_setting(db, user.id, ASK_KEY, bool(ask_finished))
    return await player_settings(db, user)


async def asks(db: AsyncSession, user: User) -> bool:
    return await users.get_setting(db, user.id, ASK_KEY, True) is not False


async def find_item(db: AsyncSession, user: User, media: str, tmdb_id: int, season: int | None, episode: int | None) -> str | None:
    """The Jellyfin item to play: the movie, or the show's episode."""
    if media == "movie":
        return (await library_map(db)).get(("movie", tmdb_id))
    if season is None or episode is None:
        nxt = await episodes.next_episode(db, user, tmdb_id)
        if not nxt:
            return None
        season, episode = nxt["season"], nxt["episode"]
    item = (await episodes.jellyfin_episodes(db, user, tmdb_id, season)).get((season, episode))
    return item.get("Id") if item else None


def _subtitles(source: dict[str, Any]) -> list[dict[str, Any]]:
    out = []
    for s in source.get("MediaStreams") or []:
        if s.get("Type") != "Subtitle":
            continue
        out.append({
            "index": s.get("Index"),
            "label": s.get("DisplayTitle") or s.get("Language") or f"Track {s.get('Index')}",
            "language": (s.get("Language") or "").lower() or None,
            "external": bool(s.get("IsExternal")),
            "text": bool(s.get("IsTextSubtitleStream")),
            "default": s.get("Index") == source.get("DefaultSubtitleStreamIndex"),
        })
    return out


def pick_subtitle(subs: list[dict[str, Any]], language: str | None) -> int | None:
    """The person's language: an external file first (the player can't see
    those on its own), then an embedded track."""
    if not language:
        return None
    mine = [s for s in subs if s["language"] == language and s["text"]]
    for s in mine:
        if s["external"]:
            return s["index"]
    return mine[0]["index"] if mine else None


async def play_info(db: AsyncSession, user: User, media: str, tmdb_id: int, season: int | None, episode: int | None) -> dict[str, Any]:
    item_id = await find_item(db, user, media, tmdb_id, season, episode)
    prefs = await player_settings(db, user)
    prefs["players"] = [p for p in prefs["players"] if p.get("enabled", True)]
    out: dict[str, Any] = {"available": bool(item_id), "linked": bool(user.jellyfin_token_enc), "jellyfin_item_id": item_id,
                           "web_url": clients.jellyfin().web_url(item_id) if item_id else None, "subtitles": [],
                           "chosen_subtitle": None, "resume_seconds": 0, **prefs}
    if not item_id:
        return out
    uid = episodes.library_user(user)
    item = await clients.jellyfin().item(uid, item_id, fields="MediaSources,ProviderIds") if uid else None
    source = ((item or {}).get("MediaSources") or [{}])[0]
    out["subtitles"] = _subtitles(source)
    out["chosen_subtitle"] = pick_subtitle(out["subtitles"], prefs["subtitle_language"])
    out["resume_seconds"] = round(((item or {}).get("UserData") or {}).get("PlaybackPositionTicks", 0) / 10_000_000)
    if item and item.get("Type") == "Episode":
        out["title"] = f"{item.get('SeriesName')} · S{item.get('ParentIndexNumber', 0):02d}E{item.get('IndexNumber', 0):02d} · {item.get('Name')}"
        out["season"], out["episode"] = item.get("ParentIndexNumber"), item.get("IndexNumber")
    else:
        out["title"] = (item or {}).get("Name")
    return out


def _fill(template: str, values: dict[str, str]) -> str:
    return re.sub(r"\{([a-z_]+)\}", lambda m: values.get(m.group(1), ""), template)


async def start(db: AsyncSession, user: User, media: str, tmdb_id: int, season: int | None, episode: int | None,
                player: str, subtitle: int | None, return_to: str = "web") -> dict[str, Any]:
    info = await play_info(db, user, media, tmdb_id, season, episode)
    if not info["available"]:
        raise PlayError(404, "This isn't in the library yet")
    jf = clients.jellyfin()
    item_id = info["jellyfin_item_id"]
    web = info["web_url"]
    templates = {p["id"]: p.get("template") or BUILTIN_PLAYERS.get(p["id"], {}).get("template") for p in info["players"]}
    template = templates.get(player)
    if not template:
        raise PlayError(422, "Unknown player")
    token = users.jellyfin_token(user)
    if not token and player not in JELLYFIN_APPS:
        raise PlayError(409, "Connect your Jellyfin account first (Profile → Jellyfin)")
    uid = episodes.library_user(user)
    item = await jf.item(uid, item_id, fields="MediaSources") if uid else None
    source = ((item or {}).get("MediaSources") or [{}])[0]
    ms_id = source.get("Id") or item_id
    # Starting a film closes the ones before it: nobody is asked about a film
    # they have moved on from.
    await db.execute(update(PlaySession)
                     .where(PlaySession.user_id == user.id, PlaySession.finished_at.is_(None))
                     .values(finished_at=dt.datetime.now(dt.timezone.utc), outcome="not_finished"))
    session = PlaySession(id=secrets.token_urlsafe(24), user_id=user.id, media_type="movie" if media == "movie" else "episode",
                          tmdb_id=tmdb_id, season=info.get("season"), episode=info.get("episode"),
                          title=info.get("title") or "", jellyfin_item_id=item_id, player=player[:40])
    db.add(session)
    await db.commit()
    # The player sends you back here when it's done: the web app, or the phone app.
    done = f"movienight://play/{session.id}" if return_to == "app" else f"{settings.public_url.rstrip('/')}/play/{session.id}"
    values = {"web": web, "done": quote(done, safe=""), "title": quote(info.get("title") or "", safe=""),
              "jellyfin_id": str(item_id).replace("-", "")}
    if player == "swiftfin" and user.jellyfin_user_id:
        server = await cache.cached("jf:server-id", 86400.0, jf.server_id)
        if server:
            ids = [str(x).replace("-", "") for x in (server, user.jellyfin_user_id, item_id)]
            values["swiftfin"] = "swiftfin://{}/{}/item/{}".format(*ids)
    if token:
        valid_subs = {s["index"] for s in info["subtitles"]}
        sub_index = subtitle if subtitle in valid_subs else None
        url = jf.stream_url(item_id, ms_id, source.get("Container"), token)
        sub_url = jf.subtitle_url(item_id, ms_id, sub_index, token) if sub_index is not None else ""
        scheme, _, rest = url.partition("://")
        values.update(url=quote(url, safe=""), url_raw=url, url_noscheme=rest, url_scheme=scheme,
                      sub=quote(sub_url, safe=""), sub_raw=sub_url,
                      filename=quote(f"{info.get('title') or 'video'}.{(source.get('Container') or 'mkv').split(',')[0]}", safe=""))
    link = _fill(template, values)
    # Drop empty optional parameters (an empty &sub= confuses some players).
    link = re.sub(r"&(sub|filename)=(?=&|$)", "", link)
    link = link.replace("S.subtitles_location=;", "")
    return {"session_id": session.id, "url": link, "web_url": web}


# Only a Play started in the last 12 hours is asked about. An older open
# Play is never asked about and stays open until the next Play closes it.
# The timings above and this are listed in docs/limits.md.
async def open_session(db: AsyncSession, user: User, within_hours: int = 12) -> PlaySession | None:
    since = dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=within_hours)
    return await db.scalar(select(PlaySession)
                           .where(PlaySession.user_id == user.id, PlaySession.finished_at.is_(None),
                                  PlaySession.player.notin_(JELLYFIN_APPS), PlaySession.started_at >= since)
                           .order_by(PlaySession.started_at.desc()).limit(1))


def _aware(t: dt.datetime) -> dt.datetime:
    return t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)


async def _due_after(db: AsyncSession, ps: PlaySession) -> dt.timedelta:
    """How long after Play the film could have ended."""
    minutes = None
    if ps.tmdb_id:
        key = titles.tkey("movie" if ps.media_type == "movie" else "tv", ps.tmdb_id)
        meta = (await titles.ensure(db, clients.tmdb(), [key], max_fetch=1)).get(key) or {}
        minutes = meta.get("runtime")  # for a show, its episode length
    minutes = minutes or DEFAULT_MINUTES.get(ps.media_type, 30)
    return max(MIN_DUE, dt.timedelta(minutes=minutes * DUE_SHARE))


async def returned(db: AsyncSession, user: User, session_id: str | None = None,
                   now: dt.datetime | None = None) -> tuple[PlaySession | None, bool]:
    """The person is back from a player: (the Play, whether to ask now).

    With `session_id` the player sent them back from that Play (its x-success
    link), so playback has stopped. Without it they simply came back to Movie
    Night: the latest open Play counts, once the first minutes have passed.
    """
    now = now or dt.datetime.now(dt.timezone.utc)
    if session_id:
        ps = await db.get(PlaySession, session_id)
        if ps is None or ps.user_id != user.id:
            raise PlayError(404, "Play not found")
        if ps.finished_at is not None:
            return ps, False
    else:
        ps = await open_session(db, user)
        if ps is None or now - _aware(ps.started_at) < GRACE:
            return ps, False
    if ps.asked_at is not None:
        return ps, False
    if not await asks(db, user):
        ps.finished_at, ps.outcome = now, "dismissed"
        await db.commit()
        return ps, False
    if now - _aware(ps.started_at) >= await _due_after(db, ps):
        ps.asked_at = now
        await db.commit()
        return ps, True
    if session_id:  # the player stopped before the film could have ended
        ps.finished_at, ps.outcome = now, "not_finished"
        await db.commit()
    return ps, False


async def finish(db: AsyncSession, user: User, session_id: str, outcome: str) -> PlaySession:
    ps = await db.get(PlaySession, session_id)
    if ps is None or ps.user_id != user.id:
        raise PlayError(404, "Play not found")
    if ps.finished_at is not None:
        return ps
    now = dt.datetime.now(dt.timezone.utc)
    ps.finished_at, ps.outcome = now, outcome
    await db.commit()
    if outcome != "watched":
        return ps
    media = "movie" if ps.media_type == "movie" else "tv"
    title = ps.title
    if ps.tmdb_id:
        meta = (await titles.ensure(db, clients.tmdb(), [titles.tkey(media, ps.tmdb_id)], max_fetch=1)).get(titles.tkey(media, ps.tmdb_id))
        title = (meta or {}).get("title") or title
    await history.mark_watched(db, user.id, media, ps.tmdb_id, title, now, season=ps.season, episode=ps.episode,
                               episode_title=ps.title.rsplit(" · ", 1)[-1] if ps.episode is not None else None, source="player")
    if user.jellyfin_user_id:
        try:
            await clients.jellyfin().mark_played(user.jellyfin_user_id, ps.jellyfin_item_id, now)
        except Exception:
            pass  # the watch is recorded here either way
        cache.invalidate(f"jf:continue:{user.id}")
    return ps
