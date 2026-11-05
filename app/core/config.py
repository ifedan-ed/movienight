"""Install-wide settings, all from the environment (.env). Each person's own
settings (Jellyfin account, players, Home rows, digests) live in the
database instead."""
from __future__ import annotations

import os

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    # Tests set MOVIENIGHT_NO_ENV_FILE so the install's real .env never leaks in.
    model_config = SettingsConfigDict(env_file=None if os.environ.get("MOVIENIGHT_NO_ENV_FILE") else ".env",
                                      env_file_encoding="utf-8", extra="ignore")

    # The defaults below are placeholders for a new install. Set each one in
    # .env; a setting left out silently takes its placeholder. Unknown keys in
    # .env are ignored (extra="ignore"). Every setting is described in
    # docs/configuration.md.

    # Database (Postgres through psycopg 3, used async). Under docker compose
    # it is always the one compose builds from POSTGRES_PASSWORD (its
    # environment: beats env_file), so a DATABASE_URL in .env has no effect.
    database_url: str = "postgresql+psycopg://movienight:movienight@postgres:5432/movienight"

    # Encrypts per-person secrets at rest (Jellyfin tokens) and signs links.
    # Generate with: python -c "import secrets; print(secrets.token_urlsafe(48))"
    secret_key: str = ""

    # Sign-in is the app's own OIDC (the OIDC_* settings, app/api/v1/auth_oidc.py):
    # the browser signs in at the provider and Movie Night keeps a signed session
    # cookie. The forward-auth proxy headers (AUTH_*/TRUSTED_PROXIES/
    # MOVIENIGHT_PROXY_SECRET) were removed with that switch (2026-10-16).
    auth_dev_allowed: bool = False
    # Local development and tests only: while AUTH_DEV_ALLOWED is set, act
    # as the user in X-Dev-User (so a test can be several people at once),
    # or AUTH_DEV_USER when no header is sent. Never on a reachable server.
    auth_dev_user: str = ""
    # Usernames that may run install-wide actions (library rescan, MDBList
    # resync of every list, removing a catalog list); OWNER_USERNAME always
    # may. Checked on every request, both ways (users.follow_admins).
    admin_users: str = ""
    # One person with two sign-in accounts: "other=main,other2=main2" makes
    # "other" use main's Movie Night (same watches, lists, settings).
    user_aliases: str = ""

    # ---- Sign-in with OIDC (the app's own sign-in) ----
    # When OIDC_ISSUER and OIDC_CLIENT_ID are set, Movie Night can do the OIDC
    # login itself (app/api/v1/auth_oidc.py): it redirects to the provider,
    # exchanges the code, reads the ID token and keeps its own signed session
    # cookie. The header path above keeps working at the same time — a valid
    # session wins; otherwise the proxy header is used.
    oidc_issuer: str = ""
    oidc_client_id: str = ""
    oidc_client_secret: str = ""
    oidc_scopes: str = "openid email profile"
    # Where the provider sends the person back. Default: PUBLIC_URL + the
    # callback path. Register this exact URL on the provider.
    oidc_redirect_url: str = ""
    # Button label on the sign-in screen.
    oidc_button_label: str = "Sign in"
    # Claim to use as the username. Empty: preferred_username, then the email's
    # local part, then the subject ("sub").
    oidc_username_claim: str = ""
    # How long a Movie Night OIDC session lasts before signing in again.
    oidc_session_days: int = 30

    # ---- Sign-in with a username and password (the app's own accounts) ----
    # On: /signin shows a username and password form (and the OIDC button
    # too when OIDC is set up, so both can be offered). Accounts are made by
    # the install's admin (python -m app.cli user add NAME); there is no
    # sign-up page. Passwords are stored as scrypt hashes
    # (app/services/passwords.py). Off by default.
    password_signin: bool = False
    # Shortest password accepted when one is set.
    password_min_length: int = 10

    # Jellyfin (the install's media server). The API key reads the library;
    # people connect their own Jellyfin accounts for plays and playback.
    jellyfin_base_url: str = "https://jellyfin.example.com"
    jellyfin_public_url: str = "https://jellyfin.example.com"
    jellyfin_api_key: str = ""
    # A Jellyfin user who sees every library. The library sync (jobs.py) and
    # the Jellyfin critic score (services/scores.py) need it, episode views
    # fall back to it, and migration 0002 linked the first person to it.
    jellyfin_user_id: str = ""
    owner_username: str = "admin"

    # Seerr (optional): requests and download status.
    seerr_base_url: str = ""
    seerr_api_key: str = ""

    # TMDB (required): titles, discovery, people, images.
    tmdb_api_key: str = ""
    tmdb_language: str = "en-US"
    watch_region: str = "US"

    # MDBList (optional): curated lists, outside scores, search fallback.
    # No allowance of Movie Night's own on these calls (MDBLIST_DAILY_BUDGET
    # is gone). When MDBList itself refuses (its own limit for the key), the
    # kept scores and lists go on showing, admins see it in Profile and
    # MDBList is asked again 10 minutes later (clients/mdblist.py).
    mdblist_api_key: str = ""

    # AI (optional): "Picked for you" rows and digests, through LiteLLM.
    litellm_base_url: str = "http://litellm:4000/v1"
    litellm_api_key: str = ""
    litellm_model: str = ""
    litellm_max_tokens: int = 8000  # output tokens per reply; reasoning models need room to think
    # "Picked for you" rows are rebuilt in the background once they are this
    # many hours old, after this many new watches, or when LITELLM_MODEL
    # changes; the old rows show meanwhile (services/rows.py). These model
    # calls have no allowance. A rebuild that fails or finds none of its picks
    # backs off (10 minutes, doubling, at most 6 hours) and Home says why;
    # only each person's newest rows are kept in ai_row_cache.
    ai_rows_cache_ttl_hours: int = 24
    ai_refresh_after_watches: int = 3
    # What "Picked for you" reads of a person, to stay within the model's
    # context window: their AI_PROMPT_TITLES latest films and best film
    # ratings, and their AI_PROMPT_COMMENTS latest comments (the first
    # AI_PROMPT_COMMENT_CHARS characters of each). Never cut without a word:
    # the first row says what was read ("From your 60 latest of 340 films,
    # ... and 20 latest of 45 comments (3 shortened to 300 characters)").
    # docs/limits.md.
    ai_prompt_titles: int = 60
    ai_prompt_comments: int = 20
    ai_prompt_comment_chars: int = 300

    # Shared secret for the Jellyfin Webhook plugin (/api/v1/scrobble/jellyfin)
    # and the Movie Night plugin for Jellyfin (/api/v1/scrobble/jellyfin/events),
    # both sent as the X-MovieNight-Token header. Empty: both are refused (503).
    jellyfin_webhook_secret: str = ""
    # Phone app development: also accept Expo Go (exp://) sign-in redirects.
    app_dev_redirects: bool = False
    # A phone's or TV's sign-in (device token) stops working after this many
    # days unused, and this many days after it signed in, whichever comes
    # first. A security measure, not a usage limit: the sign-in provider never
    # sees these tokens, so a lost phone, or someone removed from Authentik,
    # must not keep access for good. Profile -> Signed-in devices shows each
    # device's date; an expired one is told so in plain words. See
    # docs/limits.md, "Technical limits".
    device_idle_days: int = 90
    device_max_days: int = 365
    # Protection against sign-in floods from the internet on TV pairing (the
    # one sign-in the proxy lets through without Authentik): at most this
    # many new TV codes per address (an IPv6 /64 counts as one) in 10
    # minutes, and this many codes waiting at once for the whole install.
    # Security measures, not usage limits; both answer 429 in plain words.
    # See docs/limits.md, "Technical limits".
    pair_starts_per_address: int = 10
    pair_codes_waiting: int = 200
    # How long a comment or a list description (TEXT_MAX_CHARS) and a search
    # or AI description (SEARCH_MAX_CHARS) may be. They keep one value from
    # growing without bound (Movie Night has no overall request size limit);
    # longer is refused in plain words ("Your comment is too long: at most
    # 5,000 characters"); the phone's boxes count down to it and say by how
    # much a longer text is over (they never cut it).
    # See docs/limits.md, "Technical limits".
    text_max_chars: int = 5000
    search_max_chars: int = 200
    # The other request shapes, one setting per kind (technical limits, not
    # usage caps: they keep one request from carrying unbounded data). Longer
    # or more is refused with a plain sentence naming the field and the
    # limit; the website and the phone split anything bigger into several
    # calls. TEXT_MAX_CHARS also covers a player's link and the Jellyfin
    # password typed once to connect. See docs/limits.md, "Technical limits".
    # - A name a person types: a list's name, an MDBList list id or link, the
    #   list explorer's search, a player's name, a Jellyfin username, a sort.
    name_max_chars: int = 300
    # - What a player, the Kodi add-on or the Jellyfin plugin reports: a title,
    #   a series or episode name, the app and device names, the sender's own
    #   reference for a play, provider ids.
    reported_max_chars: int = 500
    # - Items in one call: watches or episodes to mark or remove, Home rows,
    #   seasons in a request, titles to fill posters for.
    batch_max_items: int = 1000

    # Meilisearch: library search by meaning (bge-base won the 2026-10-02
    # benchmark: right film first 77%, in the top 5 92%).
    meili_base_url: str = "http://meilisearch:7700"
    meili_api_key: str = ""
    meili_embedder_model: str = "BAAI/bge-base-en-v1.5"
    meili_semantic_ratio: float = 0.8
    # The most hits Meilisearch gives for one search (its index setting
    # pagination.maxTotalHits, which Movie Night sets to this on both indexes
    # at each library sync and catalog build). Search pages through every
    # match up to it; a section that stops there says so. A technical limit:
    # docs/limits.md.
    search_max_hits: int = 1000
    # The last page of one search ("Show more" stops there, and the answer
    # says so). Page N holds every match of pages 1 to N, so it reads TMDB's
    # search pages 1 to N for each wording: a deep page the cache doesn't
    # hold costs up to 2 x N TMDB calls (25: at most 50). At most 500, the
    # last page TMDB serves. A technical limit: docs/limits.md (Search).
    search_max_pages: int = Field(25, ge=1, le=500)
    # Search also covers every TMDB title with this many ratings (the
    # "catalog" index, rebuilt weekly), not only the library.
    catalog_enabled: bool = True
    # The AI's best guess for a described plot: a model (as LiteLLM names it)
    # reads the description and names the films it fits. Off while empty (it
    # also needs LITELLM_API_KEY). The website and the phone ask only when
    # /search says ai: true (4 or more words, no title matched:
    # identify.worth_asking); /search/ai itself doesn't check. A real answer
    # is cached in memory for 24 h, a failed or empty one for a minute
    # (services/identify.py). There is no cap on
    # these calls, per person or for the install (owner's rule: no usage
    # limits; any future limit will be set per person). See docs/limits.md.
    search_ai_model: str = ""
    # The search AI's answer may be this many output tokens long (a short JSON
    # list of up to four titles; a reasoning model also thinks within it).
    # When a model runs out of room before answering, "Best guess" says so.
    # A technical limit: docs/limits.md.
    search_ai_max_tokens: int = 1500
    # The catalog's vote threshold: TMDB titles with fewer votes than this
    # are left out of search's catalog index (CATALOG_ENABLED above).
    catalog_movie_min_votes: int = 5
    catalog_tv_min_votes: int = 5
    # Search, the catalog, Home, Discover and smart lists ask TMDB for every
    # title. true: leave out the ones TMDB marks adult (pornography).
    hide_adult_titles: bool = False
    # IMDb's own ratings: IMDb's free daily file (every rated title, about
    # 9 MB) loaded each night, so the IMDb score needs no outside service.
    imdb_ratings_enabled: bool = True

    # Email (optional): weekly digest, month in review and year in review.
    # Empty SMTP_HOST: no emails at all.
    smtp_host: str = ""
    smtp_port: int = 587
    smtp_user: str = ""
    smtp_password: str = ""
    smtp_from: str = "Movie Night <movienight@example.com>"
    smtp_starttls: bool = True
    # How many times the schedule (and its hourly catch-up) tries to send one
    # person's digest of one period when the mail server clearly fails (no
    # connection, a refusal); after the last it gives up on that one, with an
    # ERROR in the log, and "Send it to me now" in Profile still works. A send
    # whose fate is unknown (the connection broke after the email was handed
    # over) counts as sent and is never tried again. docs/limits.md,
    # "Background jobs".
    digest_send_tries: int = Field(3, ge=1)

    # Behaviour
    dry_run: bool = False  # true: Request buttons simulate; nothing reaches Seerr
    # The background jobs' times, Home's morning/noon/night row sets, digest
    # periods and a watch date given without a time all use this zone.
    timezone: str = "America/New_York"
    public_url: str = "https://movienight.example.com"
    run_jobs: bool = True  # background syncs; off in tests and one-off scripts

    @property
    def oidc_enabled(self) -> bool:
        return bool(self.oidc_issuer and self.oidc_client_id)

    @property
    def signin_enabled(self) -> bool:
        """Whether the browser has any way to sign in (password, OIDC or both)."""
        return self.password_signin or self.oidc_enabled

    @property
    def oidc_callback_url(self) -> str:
        return self.oidc_redirect_url or (self.public_url.rstrip("/") + "/api/v1/auth/oidc/callback")

    @property
    def admins(self) -> set[str]:
        names = {u.strip().lower() for u in self.admin_users.split(",") if u.strip()}
        return names | {self.owner_username.lower()}

    @property
    def aliases(self) -> dict[str, str]:
        out = {}
        for pair in self.user_aliases.split(","):
            alias, _, main = pair.partition("=")
            if alias.strip() and main.strip():
                out[alias.strip().lower()] = main.strip().lower()
        return out

    @property
    def seerr_enabled(self) -> bool:
        return bool(self.seerr_base_url and self.seerr_api_key)

    @property
    def mdblist_enabled(self) -> bool:
        return bool(self.mdblist_api_key)

    @property
    def ai_enabled(self) -> bool:
        return bool(self.litellm_api_key and self.litellm_model)

    @property
    def email_enabled(self) -> bool:
        return bool(self.smtp_host)


settings = Settings()
