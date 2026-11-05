# Configuration

Every install-wide setting comes from the environment, which
`docker compose` fills from `.env` (`env_file` in `docker-compose.yml`). The
settings and their defaults are in `app/core/config.py`; `.env.example` is the
template to copy. Each person's own settings (Jellyfin account, players, Home
rows, emails) are in the database and changed in the app, not here.

After changing `.env`: `docker compose up -d` (the container is recreated
with the new values).

**Read this first on a new install.** Several defaults are the original
owner's own values: `JELLYFIN_BASE_URL` and `JELLYFIN_PUBLIC_URL`
(`https://jellyfin.example.com`), `OWNER_USERNAME` (`admin`), `PUBLIC_URL`
(`https://movienight.example.com`), `SMTP_FROM` (`@example.com`) and
`LITELLM_MODEL` (`your-model`). A line missing from `.env` silently
takes that value, so set every address and name. A line that is present but
empty (`NAME=`) means empty, not the default, for a text setting; for a
number or a true/false setting (`RUN_JOBS`, `SMTP_PORT`,
`LITELLM_MAX_TOKENS`, ...) an empty value stops the app from starting (a validation
error in the log), so give it a value or delete the line. Unknown keys are
ignored, so a typo in a name is silently ignored too.

The Kodi add-on and the phone app also default to
`https://movienight.example.com`; each lets you type another address.

## Required

| Setting | Default | What it does |
|---|---|---|
| `SECRET_KEY` | (empty) | Encrypts each person's Jellyfin token and signs the phone's sign-in page. Generate once (`python3 -c "import secrets; print(secrets.token_urlsafe(48))"`) and keep it: a new key makes every stored Jellyfin link unreadable, so everyone has to connect Jellyfin again |
| `POSTGRES_PASSWORD` | (empty) | Used by `docker-compose.yml` for the Postgres container and to build `DATABASE_URL` |
| `TMDB_API_KEY` | (empty) | Titles, discovery, search, people, images |
| `JELLYFIN_BASE_URL` | `https://jellyfin.example.com` | Where the server reaches Jellyfin |
| `JELLYFIN_PUBLIC_URL` | `https://jellyfin.example.com` | The Jellyfin address people's browsers and players use (Play links, "Open in Jellyfin") |
| `JELLYFIN_API_KEY` | (empty) | Jellyfin Dashboard, API Keys. Reads the library for everyone |
| `JELLYFIN_USER_ID` | (empty) | A Jellyfin user who sees every library. Without it there is no library sync and no Jellyfin critic score. The whole library is read as this user; if that read fails, comes back incomplete or lists nothing (a 404, an empty answer, or the user no longer sees the libraries), nothing is removed and admins see why at the top of Profile ([limits](limits.md#watches-finished-it-and-progress)) |
| `PUBLIC_URL` | `https://movienight.example.com` | Movie Night's own address: links in emails, a player's link back (`{done}`), the Jellyfin plugin catalog |
| `OWNER_USERNAME` | `admin` | The sign-in username of the install's owner, who is always an admin |
| `TIMEZONE` | `America/New_York` | Job times, Home's morning/noon/night rows, digest periods, a watch date without a time |

## Sign-in and people

| Setting | Default | What it does |
|---|---|---|
| `OIDC_ISSUER` | (empty) | The app's own sign-in (2026-10-16: the forward-auth proxy path is gone). The provider's issuer URL (e.g. `https://sso.example.com/application/o/movienight/`). With `OIDC_CLIENT_ID`, Movie Night does the login itself: it redirects to the provider, reads the ID token and keeps its own signed session ([Sign-in](sign-in.md#oidc-authentik-keycloak-pocket-id)) |
| `OIDC_CLIENT_ID` | (empty) | The client id the provider issued for Movie Night |
| `OIDC_CLIENT_SECRET` | (empty) | The client secret (leave empty for a public client using PKCE) |
| `OIDC_SCOPES` | `openid email profile` | Scopes to request |
| `OIDC_REDIRECT_URL` | (empty) | Overrides the callback URL; default `{PUBLIC_URL}/api/v1/auth/oidc/callback`, which must be registered on the provider |
| `OIDC_BUTTON_LABEL` | `Sign in` | Label for the sign-in button |
| `OIDC_USERNAME_CLAIM` | (empty) | Claim used as the Movie Night username; empty: `preferred_username`, then the email's local part, then the subject |
| `PASSWORD_SIGNIN` | `false` | Browser sign-in with a username and password at `/signin` (with the OIDC button too when OIDC is set up). Accounts are made with `python -m app.cli user add NAME`; no sign-up page ([Sign-in](sign-in.md)) |
| `PASSWORD_MIN_LENGTH` | `10` | Shortest password accepted when one is set or changed |
| `OIDC_SESSION_DAYS` | `30` | How long a Movie Night OIDC session lasts before signing in again |
| `AUTH_DEV_ALLOWED` | `false` | Local development and tests only: a caller signs in as the user in `X-Dev-User` (or `AUTH_DEV_USER` without the header). Never on a server others can reach |
| `AUTH_DEV_USER` | (empty) | Local development and tests only: act as this username with no session. Never on a server others can reach |
| `ADMIN_USERS` | (empty) | More admins, comma separated. Admins can rescan the library, sync every MDBList list and remove catalog lists |
| `USER_ALIASES` | (empty) | `other=main,other2=main2`: one person with two sign-in accounts shares one Movie Night |
| `APP_DEV_REDIRECTS` | `false` | `true` also accepts Expo Go's `exp://` sign-in address (development only) |
| `DEVICE_IDLE_DAYS` | `90` | A phone's or TV's sign-in runs out after this many days unused (a security measure: the sign-in provider never sees device tokens). Each device's date shows in Profile → Signed-in devices ([limits](limits.md#technical-limits-not-usage-caps)) |
| `DEVICE_MAX_DAYS` | `365` | ... and this many days after it signed in, however much it is used |
| `PAIR_STARTS_PER_ADDRESS` | `10` | New TV (Kodi) codes one address may ask for in 10 minutes (an IPv6 /64 counts as one); then 429 "Too many sign-ins from here". A flood guard on the one sign-in the proxy lets through |
| `PAIR_CODES_WAITING` | `200` | TV codes waiting at once for the whole install; then 429 "Too many sign-ins are waiting" |
| `TEXT_MAX_CHARS` | `5000` | How long a comment (review), a list description, a player's link or the Jellyfin password typed to connect may be; longer is refused in plain words, and the phone's comment box counts down to it |
| `SEARCH_MAX_CHARS` | `200` | How long a search (or a plot described for the AI's best guess) may be |
| `NAME_MAX_CHARS` | `300` | How long a name a person types may be: a list's name, an MDBList list id or link, the list explorer's search, a player's name, a Jellyfin username, a Discover sort |
| `REPORTED_MAX_CHARS` | `500` | How long what a player, the Kodi add-on or the Jellyfin plugin reports may be: a title, a series or episode name, the app and device names, the sender's reference, a provider id |
| `BATCH_MAX_ITEMS` | `1000` | How many items one call may carry: watches to remove, episodes to mark, Home rows, seasons in a request, posters to fill. The website and the phone split bigger calls to fit (they read it from `/api/v1/me`) |

## Optional services

| Setting | Default | What it does |
|---|---|---|
| `SEERR_BASE_URL`, `SEERR_API_KEY` | (empty) | Requests through Seerr (Jellyseerr/Overseerr). Both empty: no Request button |
| `DRY_RUN` | `false` | `true`: Request buttons only pretend; nothing reaches Seerr |
| `MDBLIST_API_KEY` | (empty) | MDBList: list explorer, followed lists, outside scores, search fallback. Movie Night puts no allowance of its own on it; when MDBList refuses (its own limit for the key) the kept scores and lists go on showing ([limits](limits.md#mdblist-and-outside-scores)) |
| `IMDB_RATINGS_ENABLED` | `true` | Loads IMDb's free ratings file every night |
| `LITELLM_BASE_URL` | `http://litellm:4000/v1` | Any OpenAI-compatible endpoint |
| `LITELLM_API_KEY` | (empty) | The endpoint's key. Also needed by the AI search |
| `LITELLM_MODEL` | `your-model` | The model for "Picked for you" rows and the digest wording. Both this and the key must be set. These calls have **no daily allowance** ([limits](limits.md#recommendations-and-the-ai-rows)) |
| `LITELLM_MAX_TOKENS` | `8000` | Output tokens a reply may use (reasoning models need room) |
| `AI_ROWS_CACHE_TTL_HOURS` | `24` | "Picked for you" rows are rebuilt once this old |
| `AI_REFRESH_AFTER_WATCHES` | `3` | ... or after this many new watches |
| `AI_PROMPT_TITLES` | `60` | "Picked for you" reads a person's this many latest films and best film ratings (to fit the model's context window; the first row says what was read) |
| `AI_PROMPT_COMMENTS` | `20` | ... and this many of their latest comments |
| `AI_PROMPT_COMMENT_CHARS` | `300` | ... the first this many characters of each (a longer one ends with "…", and the row says how many were shortened) |
| `SEARCH_AI_MODEL` | (empty) | The model for the AI's best guess in search, e.g. `ds-deepseek-v4.1-flash`. Empty: off. No cap on how often it is asked ([limits](limits.md#search-and-the-ais-best-guess)) |
| `SEARCH_AI_MAX_TOKENS` | `1500` | Output tokens the search AI's answer may use (a reasoning model thinks within them). When it runs out before answering, "Best guess" says so and the log says to raise this |
| `SMTP_HOST` | (empty) | Mail server for the digests. Empty: no emails at all |
| `SMTP_PORT` | `587` | `465` uses SSL from the start; anything else plain SMTP, upgraded with STARTTLS |
| `SMTP_STARTTLS` | `true` | STARTTLS on ports other than 465 |
| `SMTP_USER`, `SMTP_PASSWORD` | (empty) | Mail server login, if it needs one |
| `SMTP_FROM` | `Movie Night <movienight@example.com>` | The sender |
| `DIGEST_SEND_TRIES` | `3` | How many times the schedule (and its hourly catch-up) tries one person's digest of one period when the mail server clearly fails (no connection, a refusal with an error code); then it is given up on for that period, with an ERROR in the log ("Send it to me now" still works). A send whose fate is unknown (the connection broke after the email was handed over, or the app stopped midway) counts as sent and is never repeated ([limits](limits.md#background-jobs)) |
| `JELLYFIN_WEBHOOK_SECRET` | (empty) | The shared secret for the Jellyfin Webhook plugin **and** the Movie Night plugin for Jellyfin (sent as `X-MovieNight-Token`). Empty: both are refused with 503 |

## Search

| Setting | Default | What it does |
|---|---|---|
| `MEILI_BASE_URL` | `http://meilisearch:7700` | The Meilisearch container |
| `MEILI_MASTER_KEY` | (empty) | Read by `docker-compose.yml` and given to the Meilisearch container |
| `MEILI_API_KEY` | (empty) | What Movie Night sends to Meilisearch. Movie Night creates and configures its indexes, so it needs the master key (or an admin key): the simplest is the same value as `MEILI_MASTER_KEY` |
| `MEILI_EMBEDDER_MODEL` | `BAAI/bge-base-en-v1.5` | The embedding model Meilisearch runs for search by meaning (won the 2026-10-02 benchmark). Changing it re-embeds everything |
| `MEILI_SEMANTIC_RATIO` | `0.8` | 0 = keywords only, 1 = meaning only |
| `SEARCH_MAX_HITS` | `1000` | The most matches one search can page through in a section (Meilisearch's `pagination.maxTotalHits`, set on both indexes at each library sync and catalog build). A technical limit ([limits](limits.md#technical-limits-not-usage-caps)): a section that stops there says so |
| `SEARCH_MAX_PAGES` | `25` | The last page of one search ("Show more" stops there, and the answer says so: "That's as far as one search goes (page 25); add words to narrow it down."). Page N holds every match of pages 1 to N, so it reads TMDB's search pages 1 to N for each wording: a deep page not in the 10-minute cache costs up to 2 x N TMDB calls (25: at most 50). At most 500 (TMDB's last). A technical limit ([limits](limits.md#search-and-the-ais-best-guess)) |
| `CATALOG_ENABLED` | `true` | Search also covers every TMDB title with enough votes (the "catalog" index, rebuilt every Sunday) |
| `CATALOG_MOVIE_MIN_VOTES`, `CATALOG_TV_MIN_VOTES` | `5` | Votes a title needs to be in the catalog (5: about 200,000 titles) |
| `HIDE_ADULT_TITLES` | `false` | `true` leaves out what TMDB marks adult, in search, the catalog, Home, Discover and smart lists |
| `TMDB_LANGUAGE` | `en-US` | TMDB's language for titles and plots |
| `WATCH_REGION` | `US` | The country for "where to watch", streaming-service rows and age ratings |

## Behaviour

| Setting | Default | What it does |
|---|---|---|
| `RUN_JOBS` | `true` | `false` turns off every background job (one-off scripts, tests) |
| `DATABASE_URL` | `postgresql+psycopg://movienight:movienight@postgres:5432/movienight` (in the code) | Postgres address. Under `docker compose` it is always the one compose builds from `POSTGRES_PASSWORD` (its `environment:` beats `.env`), so a value in `.env` has no effect. Outside compose, set it yourself: a missing one silently takes the code's default |
| `MOVIENIGHT_NO_ENV_FILE` | (unset) | Tests set it so the real `.env` is never read |

Old names that nothing reads any more, safe to delete from an old `.env`:
`SESSION_SECRET`, `TRAKT_EXPORT_DIR`, `SEERR_API_USER`,
`MOVIENIGHT_DISABLE_SCHEDULER` (use `RUN_JOBS=false`),
`SEARCH_AI_DAILY_LIMIT` (the AI search has no daily allowance any more),
`MDBLIST_DAILY_BUDGET` (nor has MDBList).

## Docker

These are read by `docker-compose.yml` itself (from `.env` in the same
folder), not by the app.

| Setting | Default | What it does |
|---|---|---|
| `LOG_MAX_SIZE` | `10m` | Each container's log file is rotated at this size (Docker's `json-file` `max-size`) |
| `LOG_MAX_FILES` | `5` | ... and at most this many files are kept, the oldest dropped first. With the defaults a container's log is at most 50 MB; it all goes when the container is replaced. What the app writes there: [limits](limits.md#data-kept-and-data-deleted) |

## What turns each feature on

| Feature | Needs | Without it |
|---|---|---|
| Library, search index, Jellyfin critic scores | `JELLYFIN_API_KEY`, `JELLYFIN_USER_ID` | No library rows, no "In your library" |
| Plays from Jellyfin into history | each person connects Jellyfin in Profile | Their plays don't come in |
| Instant plays (Webhook plugin) and progress (Movie Night plugin) | `JELLYFIN_WEBHOOK_SECRET` | 503; plays still arrive within 5 minutes |
| Requests | `SEERR_BASE_URL` + `SEERR_API_KEY`, or `DRY_RUN=true` | No Request button |
| MDBList lists, outside scores, search fallback | `MDBLIST_API_KEY` | Lists page shows what was copied before (or nothing) |
| "Picked for you" rows, the digests' wording | `LITELLM_API_KEY` + `LITELLM_MODEL` | No AI rows; digests use plain wording |
| AI best guess in search | `SEARCH_AI_MODEL` + `LITELLM_API_KEY` | No "Best guess" section |
| Digest emails | `SMTP_HOST` | No emails |
| Expo Go sign-in | `APP_DEV_REDIRECTS=true` | Only the real app (`movienight://`) can sign in |
| Admin buttons (library rescan, sync every MDBList list, removing a catalog list) | `OWNER_USERNAME`, `ADMIN_USERS` | Hidden |
