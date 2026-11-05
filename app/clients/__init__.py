"""Shared upstream clients over one pooled httpx client, created lazily in
the web process. Tests replace these with fakes via set_overrides()."""
from __future__ import annotations

from typing import Any

import httpx

from app.core.config import settings
from app.clients.jellyfin import JellyfinClient
from app.clients.litellm import LiteLLMClient
from app.clients.mailer import Mailer
from app.clients.mdblist import MDBListClient
from app.clients.meili import MeiliClient
from app.clients.seerr import SeerrClient
from app.clients.tmdb import TMDBClient

_http: httpx.AsyncClient | None = None
_overrides: dict[str, Any] = {}


def set_overrides(**clients: Any) -> None:
    _overrides.clear()
    _overrides.update(clients)


def http() -> httpx.AsyncClient:
    # Every upstream call shares this client: 25 s for each read, write or
    # wait for a free connection, 10 s to connect, at most 40 connections at
    # once (20 kept open). The clients below get it, so their own default
    # timeouts are not used; a call that passes timeout= overrides it
    # (Jellyfin's library read 120 s, MDBList 6-30 s, the AI 180 s,
    # Meilisearch uploads 60-120 s). See docs/limits.md.
    global _http
    if _http is None:
        _http = httpx.AsyncClient(
            timeout=httpx.Timeout(25.0, connect=10.0),
            limits=httpx.Limits(max_connections=40, max_keepalive_connections=20),
            follow_redirects=True,
        )
    return _http


def tmdb() -> TMDBClient:
    if "tmdb" in _overrides:
        return _overrides["tmdb"]
    return TMDBClient(settings.tmdb_api_key, settings.tmdb_language, settings.watch_region, client=http(),
                      hide_adult=settings.hide_adult_titles)


def mdblist() -> MDBListClient:
    if "mdblist" in _overrides:
        return _overrides["mdblist"]
    return MDBListClient(settings.mdblist_api_key, client=http())


def seerr() -> SeerrClient:
    if "seerr" in _overrides:
        return _overrides["seerr"]
    return SeerrClient(settings.seerr_base_url, settings.seerr_api_key, client=http(), dry_run=settings.dry_run)


def jellyfin() -> JellyfinClient:
    if "jellyfin" in _overrides:
        return _overrides["jellyfin"]
    return JellyfinClient(settings.jellyfin_base_url, settings.jellyfin_api_key, settings.jellyfin_public_url, client=http())


def meili() -> MeiliClient:
    if "meili" in _overrides:
        return _overrides["meili"]
    return MeiliClient(settings.meili_base_url, settings.meili_api_key, settings.meili_embedder_model, client=http(),
                       max_hits=settings.search_max_hits)


def litellm() -> LiteLLMClient:
    if "litellm" in _overrides:
        return _overrides["litellm"]
    return LiteLLMClient(settings.litellm_base_url, settings.litellm_api_key, settings.litellm_model,
                         settings.litellm_max_tokens, client=http())


def search_ai() -> LiteLLMClient:
    """The model that reads described plots in search (SEARCH_AI_MODEL)."""
    # SEARCH_AI_MAX_TOKENS (1,500) output tokens at most: the answer is a
    # short JSON list of up to four titles. Same endpoint and key as
    # LITELLM_*, 180 s timeout (docs/limits.md, Search).
    if "search_ai" in _overrides:
        return _overrides["search_ai"]
    return LiteLLMClient(settings.litellm_base_url, settings.litellm_api_key, settings.search_ai_model,
                         settings.search_ai_max_tokens, client=http())


def mailer() -> Mailer:
    if "mailer" in _overrides:
        return _overrides["mailer"]
    return Mailer(settings.smtp_host, settings.smtp_port, settings.smtp_user, settings.smtp_password,
                  settings.smtp_from, settings.smtp_starttls)


async def close() -> None:
    global _http
    if _http is not None:
        await _http.aclose()
        _http = None
