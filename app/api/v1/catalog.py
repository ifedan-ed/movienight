"""Finding titles: search (your library, every rated title on TMDB by meaning
and keywords, TMDB's live title search and people, with MDBList filling in
titles TMDB's search misses; best match first), a person's films, poster back-fill
for big lists, and the Jellyfin image proxy (so the Jellyfin API key never
reaches the browser)."""
from __future__ import annotations

import asyncio
import logging

from fastapi import APIRouter, Depends, HTTPException, Path, Query
from fastapi.responses import Response
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db, user_state
from app.core import cache
from app.core.config import settings
from app.models import User
from app.clients.meili import CATALOG
from app.services import catalog, identify, spelling, titles
from app.services.cards import cards

log = logging.getLogger("movienight.catalog")
router = APIRouter(tags=["catalog"])


# Search answers in pages of matches, best first: page N holds the best
# LIBRARY_PAGE × N library titles, OTHERS_PAGE × N other titles (of
# CATALOG_PAGE × N read from the catalog index, plus TMDB's own search pages
# 1 to N) and every person on those TMDB pages. `more` says, per section,
# that there are more matches, and the website and the phone then offer
# "Show more" (page N + 1): nothing is cut without a word. See
# docs/limits.md (Search).
LIBRARY_PAGE = 24
OTHERS_PAGE = 30
CATALOG_PAGE = 40
# Meilisearch answers at most SEARCH_MAX_HITS hits for one search (its index
# setting pagination.maxTotalHits, which Movie Night sets from it); when a
# section stops there the answer says so (`note`). A technical limit:
# docs/limits.md.
# The last page: SEARCH_MAX_PAGES, 25. Why so low: page N holds every match
# of pages 1 to N (the website and the phone show each answer in place of
# the one before), so it needs TMDB's search pages 1 to N, for each
# wording: TMDB sends 20 results a page and has no call for "the first
# 20 x N". A request for a deep page the 10-minute cache doesn't hold
# therefore costs 2 x N TMDB calls (page 500, TMDB's last, was about
# 1,000). At 25 that is at most 50, and 25 is where the other titles'
# catalog hits stop anyway (40 a page, Meilisearch's 1,000 at most:
# SEARCH_MAX_HITS); by then a search shows 600 library titles, 750 others
# and every person on TMDB's first 500 results. Paging there one "Show
# more" at a time reads one new TMDB page per wording each step. At the
# last page `more` is false and the answer says search stops there
# (LAST_PAGE_NOTE); a higher page is refused in plain words ("Page: Input
# should be less than or equal to 25."). A technical limit: docs/limits.md.
SEARCH_LAST_PAGE = settings.search_max_pages
LAST_PAGE_NOTE = f"page {SEARCH_LAST_PAGE} is as far as one search goes"
# TMDB's pages of one search are read at most this many at a time (both
# wordings together; a page read in the last 10 minutes comes from the
# cache and doesn't wait), so one deep "Show more" can't burst hundreds of
# calls at TMDB at once. docs/limits.md.
TMDB_PAGES_AT_ONCE = 4


@router.get("/search", response_model=s.SearchOut)
async def search(q: str = Query(max_length=settings.search_max_chars), media: s.MediaType | None = None,
                 page: int = Query(1, ge=1, le=SEARCH_LAST_PAGE), db: AsyncSession = Depends(get_db),
                 user: User = Depends(current_user)):
    """Your library, every rated title on TMDB (the catalog index: by meaning
    and keywords, so a described plot works) and TMDB's live title search
    (new releases, exact names), ranked on one scale. `first` says which
    section holds the best match. `page` 2, 3, ... answers with more of each
    section (the matches before included); `more` says which have more."""
    # The text may be SEARCH_MAX_CHARS long (a technical limit). MDBList's
    # search adds up to 8 more only when fewer than 3 other titles came back
    # and the query has 3 or more characters. `ai` tells the page to ask
    # /search/ai as well (identify.worth_asking). See docs/limits.md.
    q = q.strip()
    if not q:
        return {"library": [], "tmdb": [], "people": [], "first": "library", "corrected": None, "ai": False}
    lib_limit, cat_limit, shown = LIBRARY_PAGE * page, CATALOG_PAGE * page, OTHERS_PAGE * page
    max_hits = settings.search_max_hits
    m = media.value if media else None
    tmdb, meili, mdb = clients.tmdb(), clients.meili(), clients.mdblist()
    # Typos fixed against the catalog's own words; both wordings are searched.
    fixed = spelling.correct(q, await spelling.vocab())
    queries = [q] if fixed == q else [q, fixed]

    async def lib(text):
        try:
            return await meili.search(text, semantic_ratio=settings.meili_semantic_ratio, limit=lib_limit, media_type=m, scores=True)
        except Exception:
            log.warning("library search failed", exc_info=True)
            return []

    async def everything(text):
        if not settings.catalog_enabled:
            return []
        try:
            return await meili.search(text, semantic_ratio=settings.meili_semantic_ratio, limit=cat_limit, media_type=m,
                                      index=CATALOG, scores=True)
        except Exception:
            log.info("catalog search unavailable", exc_info=True)
            return []

    tmdb_more = False
    tmdb_gate = asyncio.Semaphore(TMDB_PAGES_AT_ONCE)

    async def from_tmdb(text):
        # TMDB's pages 1 to `page` (each kept 10 minutes; `page` is at most
        # SEARCH_LAST_PAGE), never past its last, TMDB_PAGES_AT_ONCE at a time.
        nonlocal tmdb_more

        async def fetch(n):
            async with tmdb_gate:
                return await tmdb.search_multi_page(text, n)
        try:
            def one(n):
                return cache.cached(f"search:{n}:{text.lower()}", 600.0, lambda: fetch(n))
            out, pages = await one(1)
            rest = await asyncio.gather(*(one(n) for n in range(2, min(page, pages) + 1)))
            tmdb_more = tmdb_more or pages > page  # at SEARCH_LAST_PAGE: said in the note, no "Show more"
            return out + [r for got, _ in rest for r in got]
        except Exception:
            log.warning("TMDB search failed", exc_info=True)
            return []

    got = await asyncio.gather(*(job(text) for text in queries for job in (lib, everything, from_tmdb)))
    hits = [h for i in range(0, len(got), 3) for h in got[i]]
    chits = [h for i in range(1, len(got), 3) for h in got[i]]
    results = [r for i in range(2, len(got), 3) for r in got[i]]
    # A Meilisearch search that came back full may have more behind it.
    lib_full = [len(got[i]) for i in range(0, len(got), 3) if len(got[i]) >= min(lib_limit, max_hits)]
    cat_full = [len(got[i]) for i in range(1, len(got), 3) if len(got[i]) >= min(cat_limit, max_hits)]

    def named(*names):
        return max(catalog.title_match(text, *names) for text in queries)

    st = await user_state(db, user)

    # Library: Meilisearch's score, or the title match if that is better.
    lib_scores: dict[str, float] = {}
    for h in hits:
        if h.get("tmdb_id") and h.get("media_type") in ("movie", "tv"):
            k = titles.tkey(h["media_type"], h["tmdb_id"])
            score = max(float(h.get("_rankingScore") or 0), named(h.get("title"), h.get("original_title")))
            lib_scores[k] = max(lib_scores.get(k, 0.0), score)

    # Everything else: catalog hits and TMDB's title search, best score wins.
    scored: dict[str, float] = {}
    raw: dict[str, dict] = {}
    for h in chits:
        r = h.get("tmdb") or {}
        if not r.get("id") or h.get("media_type") not in ("movie", "tv"):
            continue
        k = titles.tkey(h["media_type"], r["id"])
        score = max(float(h.get("_rankingScore") or 0), named(h.get("title"), h.get("original_title"))) \
            + catalog.popularity_bonus(h.get("votes"))
        scored[k] = max(scored.get(k, -1.0), score)
        raw[k] = {**r, "media_type": h["media_type"]}
    for i, r in enumerate(x for x in results if x.get("media_type") in ("movie", "tv")):
        k = titles.tkey(r["media_type"], r["id"])
        name = r.get("title") or r.get("name")
        original = r.get("original_title") or r.get("original_name")
        score = max(named(name, original), 0.5 - 0.01 * i) + catalog.popularity_bonus(r.get("vote_count"))
        if score > scored.get(k, -1.0):
            scored[k] = score
        raw.setdefault(k, r)
    # Whatever is in the library goes in the library section, however it was
    # found, with the best score any of the searches gave it.
    for k in [k for k in scored if k in lib_scores or titles.split_key(k) in st.library]:
        lib_scores[k] = max(lib_scores.get(k, 0.0), scored.pop(k))
    lib_ranked = [k for k in sorted(lib_scores, key=lambda k: -lib_scores[k]) if m is None or k.startswith(m)]
    lib_keys = lib_ranked[:lib_limit]
    await titles.upsert_results(db, [raw[k] for k in lib_keys if k in raw])
    metas = await titles.ensure(db, tmdb, lib_keys, max_fetch=len(lib_keys))
    library = cards([metas.get(k) for k in lib_keys], st)
    owned = {c["key"] for c in library} | set(lib_scores)
    order = [k for k in sorted(scored, key=lambda k: -scored[k]) if k not in owned and (m is None or k.startswith(m))]
    tmetas = {mm["key"]: mm for mm in await titles.upsert_results(db, [raw[k] for k in order[:cat_limit]])}
    found = [c for c in cards([tmetas.get(k) for k in order[:cat_limit]], st) if c["key"] not in owned]
    # MDBList's search can find titles the others miss. Asked only when the
    # rest come up short. (Cheeky, once the example, was hidden by TMDB's
    # adult filter, not by its name; search no longer uses that filter.)
    if len(found) < 3 and mdb.enabled and len(q) >= 3:
        try:
            extra = await cache.cached(f"mdbsearch:{q.lower()}", 3600.0, lambda: mdb.search(q, limit=8))
            keys = [titles.tkey(em, t) for em, t, _, _ in extra if m is None or em == m]
            seen = owned | {c["key"] for c in found}
            emetas = await titles.ensure(db, tmdb, [k for k in keys if k not in seen], max_fetch=8)
            found += cards([emetas.get(k) for k in keys if k not in seen], st)
            for k in keys:
                scored.setdefault(k, 0.45)
        except Exception:
            log.info("MDBList search fallback unavailable", exc_info=True)
    # Every person on the TMDB pages read (no cut): page N + 1 reads TMDB's next page.
    people = list({r.get("id"): {"id": r.get("id"), "name": r.get("name"), "photo": r.get("profile_path"),
                                 "known_for": r.get("known_for_department")}
                   for r in results if r.get("media_type") == "person"}.values())
    best_named = max([named(r.get("title") or r.get("name"), r.get("original_title") or r.get("original_name"))
                      for r in results if r.get("media_type") in ("movie", "tv")]
                     + [named(h.get("title"), h.get("original_title")) for h in hits + chits], default=0.0)
    best_lib = max((lib_scores.get(c["key"], 0.0) for c in library), default=-1.0)
    best_other = max((scored.get(c["key"], 0.0) for c in found), default=-1.0)
    more = {"library": len(lib_ranked) > lib_limit or (bool(lib_full) and lib_limit < max_hits),
            "tmdb": len(found) > shown or tmdb_more or (bool(cat_full) and cat_limit < max_hits),
            "people": tmdb_more}
    # The last page offers no "Show more" (a page past it is refused), and
    # says so when a section had more.
    cut = page >= SEARCH_LAST_PAGE and any(more.values())
    if page >= SEARCH_LAST_PAGE:
        more = dict.fromkeys(more, False)
    # Meilisearch gives at most its 1,000 best hits for one search: say so
    # where a section stopped there rather than end it without a word.
    stopped = [what for what, full, limit in (("your library", lib_full, lib_limit), ("other titles", cat_full, cat_limit))
               if full and limit >= max_hits]
    why = [f"These are the {max_hits:,} best matches in {' and '.join(stopped)} (the most one search gives)"] if stopped else []
    if cut:
        why.append(LAST_PAGE_NOTE if why else f"That's as far as one search goes (page {SEARCH_LAST_PAGE})")
    note = (", and ".join(why) + "; add words to narrow it down.") if why else None
    return {"library": library, "tmdb": found[:shown], "people": people, "first": "tmdb" if best_other > best_lib else "library",
            "corrected": fixed if fixed != q else None, "ai": identify.worth_asking(q, best_named),
            "page": page, "more": more, "note": note}


@router.get("/search/ai", response_model=s.AiGuessOut)
async def search_ai(q: str = Query(max_length=settings.search_max_chars), media: s.MediaType | None = None, db: AsyncSession = Depends(get_db),
                    user: User = Depends(current_user)):
    """The AI's best guess for a described plot (only when SEARCH_AI_MODEL is
    set): the films it names, as cards, each with a few words on why."""
    # No cap of any kind here, per person or for the install (the owner's
    # rule: no usage limits; docs/limits.md). Nor is identify.worth_asking
    # checked here: the 4-word rule is only the `ai` hint /search gives the
    # website and the phone, so any text sent straight to this route is asked.
    q = q.strip()
    if not q or not identify.enabled():
        return {"enabled": identify.enabled(), "matches": []}
    m = media.value if media else None
    got, note = await identify.best_guesses(clients.tmdb(), q, m)
    st = await user_state(db, user)
    by_key = {c["key"]: c for c in cards(await titles.upsert_results(db, [r for r, _ in got]), st)}
    matches = [{"card": by_key[k], "why": why} for k, why in ((titles.tkey(r["media_type"], r["id"]), why) for r, why in got)
               if k in by_key]
    # No matches: say why in plain words (the page shows it instead of hiding the section).
    return {"enabled": True, "matches": matches, "note": None if matches else (note or identify.NOTHING)}


@router.get("/person/{person_id}", response_model=s.PersonOut)
async def person(person_id: int = Path(gt=0, le=100_000_000), db: AsyncSession = Depends(get_db),
                 user: User = Depends(current_user)):
    tmdb = clients.tmdb()
    try:
        p = await cache.cached(f"person:{person_id}", 6 * 3600.0, lambda: tmdb.person(person_id))
    except Exception as exc:
        raise HTTPException(status_code=502, detail="Couldn't reach TMDB") from exc
    if not p:
        raise HTTPException(status_code=404, detail="Person not found")
    st = await user_state(db, user)
    credits = p.get("combined_credits") or {}
    cast = credits.get("cast") or []
    crew = [c for c in credits.get("crew") or [] if c.get("job") in ("Director", "Screenplay", "Writer", "Creator")]
    ranked = sorted(cast + crew, key=lambda r: (r.get("vote_count") or 0), reverse=True)
    return {
        "id": p.get("id"), "name": p.get("name"), "photo": p.get("profile_path"),
        "biography": p.get("biography"), "birthday": p.get("birthday"), "place_of_birth": p.get("place_of_birth"),
        "known_for": p.get("known_for_department"),
        "items": cards(await titles.upsert_results(db, ranked), st),
    }


@router.post("/titles/fill", response_model=s.FillOut)
async def fill(body: s.KeysIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Posters for cards that arrived without one (big lists)."""
    metas = await titles.ensure(db, clients.tmdb(), body.keys, max_fetch=len(body.keys))
    return {"titles": {k: {"poster": m.get("poster_path"), "backdrop": m.get("backdrop_path"), "year": m.get("year"),
                           "title": m.get("title"), "vote": round(m["vote_average"], 1) if m.get("vote_average") else None}
                       for k, m in metas.items()}}


@router.get("/img/jf/{item_id}", response_class=Response)
async def jellyfin_image(item_id: str = Path(pattern=r"^[0-9a-f]{32}$"),
                         kind: str = Query("Primary", pattern=r"^(Primary|Backdrop|Thumb)$"),
                         w: int = Query(400, ge=80, le=1280), user: User = Depends(current_user)):
    got = await clients.jellyfin().image(item_id, kind, w)
    if got is None:
        raise HTTPException(status_code=404, detail="No image")
    content, ctype = got
    if not ctype.startswith("image/"):
        raise HTTPException(status_code=502, detail="Not an image")
    return Response(content, media_type=ctype, headers={"Cache-Control": "private, max-age=604800"})
