"""What the install's admins should know about work Movie Night does by
itself, kept so it can be shown to them instead of only written to the log:
the last library sync (services/sync.py), each linked person's last plays
read from Jellyfin (services/sync.py, "status:plays:<user id>"), the search
catalog's last refresh when part of TMDB couldn't be read
(services/catalog.py, "catalog_status"), and MDBList
refusing Movie Night's requests (clients/mdblist.py: kept in memory,
cleared by MDBList's next answer).

Each is a small JSON object in app_settings under "status:<name>". Admins
see the ones that need them in Profile (GET /api/v1/me, `admin_notices`);
a person whose plays couldn't be read, or who has plays Jellyfin gave no
date for, sees that in their own Profile and on the Jellyfin page
(`jellyfin.plays_notice`). Nothing here stops anything working.
"""
from __future__ import annotations

import datetime as dt
import json
from typing import Any
from zoneinfo import ZoneInfo

from sqlalchemy import delete, select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app.clients.mdblist import refusal as mdblist_refusal
from app.core.config import settings
from app.models import AppSetting, User

PREFIX = "status:"
LIBRARY = "library_sync"
PLAYS = "plays:"  # + the person's numeric id


def plays(user_id: int) -> str:
    """The name of one person's plays-read status."""
    return f"{PLAYS}{user_id}"


def now_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def when(iso: str | None) -> str:
    """'Oct 9, 14:30' in the install's time zone."""
    if not iso:
        return "never"
    try:
        t = dt.datetime.fromisoformat(iso)
    except ValueError:
        return iso
    local = (t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)).astimezone(ZoneInfo(settings.timezone))
    return f"{local.strftime('%b')} {local.day}, {local.strftime('%H:%M')}"


async def get(db: AsyncSession, name: str) -> dict[str, Any] | None:
    row = await db.get(AppSetting, PREFIX + name, populate_existing=True)
    try:
        value = json.loads(row.value) if row else None
    except ValueError:
        return None
    return value if isinstance(value, dict) else None


async def record(db: AsyncSession, name: str, **fields: Any) -> dict[str, Any]:
    """Merge these fields into the status and save it (its own commit)."""
    value = {**(await get(db, name) or {}), **fields}
    text = json.dumps(value)
    stmt = pg_insert(AppSetting).values(key=PREFIX + name, value=text)
    await db.execute(stmt.on_conflict_do_update(index_elements=[AppSetting.key], set_={"value": text}))
    await db.commit()
    return value


async def forget(db: AsyncSession, name: str) -> None:
    """Drop a status (its own commit): a person who disconnects Jellyfin."""
    await db.execute(delete(AppSetting).where(AppSetting.key == PREFIX + name))
    await db.commit()


def plays_problem(value: dict[str, Any] | None, whose: str = "Your") -> str | None:
    """A plain sentence when a person's last plays read failed, else None."""
    if not value or value.get("ok") is not False:
        return None
    last = value.get("last_good_at")
    hint = (" A 404 usually means the Jellyfin account was deleted or renamed: connect it again in Profile → Jellyfin."
            if value.get("status") == 404 else "")
    return (f"{whose} Jellyfin plays couldn't be read at {when(value.get('at'))}: {value.get('problem')}. "
            f"Nothing was added; Movie Night tries again every 5 minutes by itself"
            f"{f' (last complete read: {when(last)})' if last else ''}.{hint}")


def plays_undated(value: dict[str, Any] | None) -> str | None:
    """A plain sentence when Jellyfin lists plays without a date, else None:
    a play needs its date to go into the history, so those aren't added."""
    n = (value or {}).get("undated") or 0
    if not n:
        return None
    if n == 1:
        return ("1 of your Jellyfin plays has no date in Jellyfin (it was marked played without one), so it isn't in "
                "your history here. Mark it watched here, with a date, to add it.")
    return (f"{n:,} of your Jellyfin plays have no date in Jellyfin (they were marked played without one), so they "
            "aren't in your history here. Mark them watched here, with a date, to add them.")


async def plays_notice(db: AsyncSession, user: User) -> str | None:
    """What this person should know about their plays coming in from
    Jellyfin (Profile, the Jellyfin page), or None."""
    if not user.jellyfin_user_id:
        return None
    value = await get(db, plays(user.id))
    return " ".join(x for x in (plays_problem(value), plays_undated(value)) if x) or None


async def notices(db: AsyncSession) -> list[str]:
    """Plain sentences for the admins: only what needs their attention."""
    out: list[str] = []
    lib = await get(db, LIBRARY)
    if lib and lib.get("ok") is False:
        out.append(f"The Jellyfin library sync at {when(lib.get('at'))} didn't finish: {lib.get('problem')}. "
                   f"Nothing was removed; Movie Night's library was left as it was ({lib.get('kept', 0):,} titles). "
                   f"Last complete sync: {when(lib.get('last_good_at'))}.")
    # Each linked person whose last plays read failed (by display name here:
    # this is shown to admins, never written to the log).
    failed = list(await db.execute(
        select(AppSetting.value, User).join(User, AppSetting.key == PREFIX + PLAYS + User.id.cast(AppSetting.key.type))
        .where(AppSetting.key.like(PREFIX + PLAYS + "%"), User.jellyfin_user_id.isnot(None))
        .order_by(User.id)))
    for raw, user in failed:
        try:
            value = json.loads(raw)
        except ValueError:
            continue
        sentence = plays_problem(value if isinstance(value, dict) else None,
                                 whose=f"{user.display_name or user.username}'s (user {user.id})")
        if sentence:
            out.append(sentence)
    # The search catalog's last refresh, when some of TMDB couldn't be read
    # (services/catalog.py keeps it under "catalog_status").
    row = await db.get(AppSetting, "catalog_status", populate_existing=True)
    try:
        cat = json.loads(row.value) if row else None
    except ValueError:
        cat = None
    if isinstance(cat, dict) and cat.get("failed_pages"):
        n = cat["failed_pages"]
        out.append(f"The search catalog's refresh at {when(cat.get('at'))} couldn't read everything from TMDB "
                   f"({n:,} {'read' if n == 1 else 'reads'} failed; the log says which). New and changed titles were "
                   "added; none was removed. It runs again on Sunday at 03:00, or now with "
                   "`docker compose exec web python -m app.services.catalog`.")
    refused = mdblist_refusal()
    if refused:
        out.append(f"MDBList refused Movie Night's requests at {when(refused['at'])} ({refused['why']}: its own limit "
                   "for this install's key). Title pages show the scores kept from before, with a note; lists keep "
                   "their titles. Movie Night asks again every 10 minutes by itself; this goes away once MDBList answers.")
    return out
