# Configuration

Every install-wide setting comes from the environment, which
`docker compose` fills from `.env` (`env_file` in the compose file).
`.env.example` is the template to copy; the server reads the settings in
`server/internal/config`. Each person's own settings (Jellyfin account,
players, Home rows, emails, time zone) are in the database and changed in
the app, not here.

After changing `.env`: `docker compose up -d` (the containers are recreated
with the new values).

A setting that names a place, a key or the owner has no default: a missing
one stops startup with a message naming it, and every problem is listed at
once. A half-configured service (`SEERR_BASE_URL` without `SEERR_API_KEY`,
`OIDC_ISSUER` without `OIDC_CLIENT_ID`, ...) is an error too, not "off". A
number or a true/false setting left empty takes its default; one that isn't
a number or a true/false (`true`/`false`, `1`/`0`, `yes`/`no`, `on`/`off`)
stops startup. Unknown names are ignored, so check the spelling of a
setting that seems to have no effect.

The Kodi add-on and the phone app ask for your address; nothing in them
needs to change for your server.

## Required

| Setting | Default | What it does |
|---|---|---|
| `SECRET_KEY` | required | Encrypts each person's Jellyfin token and keys the signatures of the sign-in forms. Generate once (`openssl rand -base64 48`) and keep it: a new key makes every stored Jellyfin link unreadable, so everyone has to connect Jellyfin again |
| `POSTGRES_PASSWORD` | required | Used by the compose file for the Postgres container and to build `DATABASE_URL` |
| `TMDB_API_KEY` | required | Titles, discovery, search, people, images |
| `JELLYFIN_BASE_URL` | required | Where the server reaches Jellyfin (can be an internal address) |
| `JELLYFIN_PUBLIC_URL` | required | The Jellyfin address people's browsers and players use (Play links, "Open in Jellyfin") |
| `JELLYFIN_API_KEY` | required | Jellyfin Dashboard, API Keys. Reads the library for everyone |
| `JELLYFIN_USER_ID` | required | A Jellyfin user who sees every library. The whole library is read as this user; if that read fails, comes back incomplete or lists nothing (a 404, an empty answer, or the user no longer sees the libraries), nothing is removed and admins see why at the top of Profile ([limits](limits.md#watches-finished-it-and-progress)) |
| `PUBLIC_URL` | required | Movie Night's own address, e.g. `https://movies.example.com`: links in emails, a player's link back (`{done}`), the Jellyfin plugin catalog, the OIDC callback. An `https` address also marks the cookies Secure |
| `OWNER_USERNAME` | required | The sign-in username of the install's owner, who is always an admin |
| `TIMEZONE` | required | An IANA zone name (e.g. `Europe/London`): the install's clock. The worker's schedule runs on it (Monday 09:00, 02:30, ...). Each person's own dates follow their own zone, the one their browser or phone reports or one they picked in Profile → Time zone (history's days, "this year" in Profile, Home's morning/noon/night rows and daily shuffle, "today" for aired episodes, a watch date given without a time at 20:00 that day, their digest weeks, months and years, due at 09:00 on their own Monday); `TIMEZONE` is the zone of someone who has neither reported nor picked one (a new account before its first page load, someone who only uses Kodi). An unknown name stops startup |
| `TRUSTED_PROXY` | required | Your reverse proxy's address(es) as the app sees them: IPs or CIDRs, comma separated, or `none` when nothing sits in front. Only a request from one of these has its `X-Forwarded-For` believed, which is what the per-address sign-in lockout and the TV pairing limit count by. With a proxy on the host in front of `127.0.0.1:8137`, it is the gateway of the compose network: `docker network inspect movienight_default --format '{{(index .IPAM.Config 0).Gateway}}'`. With a proxy container on that network, its address. Trusting the wrong address makes every visitor look like the proxy, so one person's wrong passwords would lock out everyone |
| `MEILI_MASTER_KEY` | (empty), set one | Given to the Meilisearch container by the compose file. Set a long random one (`openssl rand -base64 32`): without it Meilisearch answers anything on the compose network |
| `MEILI_API_KEY` | (empty) | What Movie Night sends to Meilisearch. Movie Night creates and configures its indexes, so it needs the master key (or an admin key): the simplest is the same value as `MEILI_MASTER_KEY` |

## Sign-in and people

| Setting | Default | What it does |
|---|---|---|
| `OIDC_ISSUER` | (empty) | The provider's issuer URL (e.g. `https://sso.example.com/application/o/movienight/`). With `OIDC_CLIENT_ID`, Movie Night does the login itself: it redirects to the provider, reads the ID token and keeps its own session ([Sign-in](sign-in.md)). One without the other stops startup |
| `OIDC_CLIENT_ID` | (empty) | The client id the provider issued for Movie Night |
| `OIDC_CLIENT_SECRET` | (empty) | The client secret (leave empty for a public client using PKCE) |
| `OIDC_SCOPES` | `openid email profile` | Scopes to request |
| `OIDC_REDIRECT_URL` | (empty) | Overrides the callback URL; default `{PUBLIC_URL}/api/v1/auth/oidc/callback`, which must be registered on the provider |
| `OIDC_BUTTON_LABEL` | `Sign in` | Label for the sign-in button |
| `OIDC_USERNAME_CLAIM` | (empty) | Claim used as the Movie Night username; empty: `preferred_username`, then the email's local part, then the subject |
| `PASSWORD_SIGNIN` | `false` | Sign-in with a username and password: the website's `/signin` form (with the OIDC button too when OIDC is set up) and the phone app's fields (`POST /api/v1/auth/password`; the app reads `GET /api/v1/auth/methods` and shows only the browser sign-in while this is off). Accounts are made and passwords set with `admin user add NAME` and `admin user password NAME`; no sign-up page ([Sign-in](sign-in.md)) |
| `PASSWORD_MIN_LENGTH` | `10` | Shortest password accepted when one is set or changed |
| `OIDC_SESSION_DAYS` | `30` | How long a browser stays signed in (password and OIDC sign-ins alike). Sessions are kept on the server, so signing out ends one at once |
| `AUTH_DEV_ALLOWED` | `false` | Local development and tests only: a caller signs in as the user in `X-Dev-User` (or `AUTH_DEV_USER` without the header). Never on a server others can reach |
| `AUTH_DEV_USER` | (empty) | Local development and tests only: act as this username with no session. Never on a server others can reach |
| `ADMIN_USERS` | (empty) | More admins, comma separated. Admins can rescan the library, sync every MDBList list and remove catalog lists |
| `USER_ALIASES` | (empty) | `other=main,other2=main2`: one person with two sign-in accounts shares one Movie Night |
| `APP_DEV_REDIRECTS` | `false` | `true` also accepts Expo Go's `exp://` sign-in address (development only) |
| `APPLE_APP_ID` | (empty) | Your iPhone app build's Apple app id, `TEAMID.bundle.id` (e.g. `ABCDE12345.com.example.movienight`): served at `/.well-known/apple-app-site-association` so iOS offers the site's saved passwords on the app's sign-in fields. Needs the app built with `webcredentials:<your host>` in `ios.associatedDomains`. Empty: that address is a 404 and the app signs in without autofill |
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
| `DRY_RUN` | `false` | `true` changes nothing outside the install: no Seerr requests or Seerr users, no Jellyfin played marks or sign-outs, and no email (a send succeeds without sending). For a test install or a copy of a live database; reads still go out |
| `MDBLIST_API_KEY` | (empty) | MDBList: list explorer, followed lists, outside scores, search fallback. Movie Night puts no allowance of its own on it; when MDBList refuses (its own limit for the key) the kept scores and lists go on showing ([limits](limits.md#mdblist-and-outside-scores)) |
| `IMDB_RATINGS_ENABLED` | `true` | Loads IMDb's free ratings file every night |
| `LITELLM_BASE_URL` | (empty) | Any OpenAI-compatible endpoint, e.g. `http://litellm:4000/v1`. Required when `LITELLM_API_KEY` is set |
| `LITELLM_API_KEY` | (empty) | The endpoint's key. Also needed by the AI search |
| `LITELLM_MODEL` | (empty) | The model for "Picked for you" rows and the digest wording. Both this and the key must be set. These calls have no daily allowance ([limits](limits.md#recommendations-and-the-ai-rows)) |
| `LITELLM_MAX_TOKENS` | `8000` | Output tokens a reply may use (reasoning models need room) |
| `AI_ROWS_CACHE_TTL_HOURS` | `24` | "Picked for you" rows are rebuilt once this old |
| `AI_REFRESH_AFTER_WATCHES` | `3` | ... or after this many new watches |
| `AI_PROMPT_TITLES` | `60` | "Picked for you" reads a person's this many latest films and best film ratings (to fit the model's context window; the first row says what was read) |
| `AI_PROMPT_COMMENTS` | `20` | ... and this many of their latest comments |
| `AI_PROMPT_COMMENT_CHARS` | `300` | ... the first this many characters of each (a longer one ends with "…", and the row says how many were shortened) |
| `SEARCH_AI_MODEL` | (empty) | The model for the AI's best guess in search. Empty: off. No cap on how often it is asked ([limits](limits.md#search-and-the-ais-best-guess)) |
| `SEARCH_AI_MAX_TOKENS` | `1500` | Output tokens the search AI's answer may use (a reasoning model thinks within them). When it runs out before answering, "Best guess" says so and the log says to raise this |
| `MAIL_SENDER` | `smtp` | How the digests go out: `smtp` (the `SMTP_*` settings) or `smtp2go` (SMTP2GO's HTTP API). Anything else stops startup |
| `SMTP2GO_API_KEY_FILE` | (empty) | With `MAIL_SENDER=smtp2go` (then required): a file holding the SMTP2GO API key, read at start-up; one that is missing, empty or unreadable stops startup. The containers run as uid 1000 and see the `movienight-data` volume read-only at `/data`, so a key file there must be readable by uid 1000 |
| `SMTP_HOST` | (empty) | With `MAIL_SENDER=smtp`: the mail server for the digests. Empty: no emails at all |
| `SMTP_PORT` | `587` | `465` uses SSL from the start; anything else plain SMTP, upgraded with STARTTLS |
| `SMTP_STARTTLS` | `true` | STARTTLS on ports other than 465 |
| `SMTP_USER`, `SMTP_PASSWORD` | (empty) | Mail server login, if it needs one |
| `SMTP_FROM` | `Movie Night <movienight@example.com>` | The sender, for either `MAIL_SENDER` |
| `DIGEST_SEND_TRIES` | `3` | How many times the schedule (and its hourly catch-up) tries one person's digest of one period when the mail server clearly fails (no connection, a refusal with an error code); then it is given up on for that period, with an ERROR in the log ("Send it to me now" still works). A send whose fate is unknown (the connection broke after the email was handed over, or the app stopped midway) counts as sent and is never repeated ([limits](limits.md#background-jobs)) |
| `JELLYFIN_WEBHOOK_SECRET` | (empty) | The shared secret for the Jellyfin Webhook plugin and the Movie Night plugin for Jellyfin (sent as `X-MovieNight-Token`). Empty: both are refused with 503 |

## Error reports

Crashes and errors can go to a Sentry-compatible collector such as
GlitchTip or Sentry. Each part is off while its setting is empty. Make one
project per part in the collector and copy each project's DSN.

The website and the phone app never get a DSN. They send their reports to
Movie Night (`POST /api/v1/errors/web` and `/api/v1/errors/app`), which
forwards them with the DSN set here. One address may send 20 reports in
10 minutes, and everyone together 300 an hour per part. Reports over that
are dropped.

A report holds the error message, its stack trace, the browser or OS and
the app version, and the page's path without its query string. It never
holds a name, a username, an email address, a token, a cookie or a request
body. The collector sees Movie Night's address, never the visitor's.

| Setting | Default | What it does |
|---|---|---|
| `ERROR_REPORTS_SERVER_DSN` | (empty) | The api's and the worker's own errors: everything they log at ERROR, panics included |
| `ERROR_REPORTS_WEB_DSN` | (empty) | The website's errors. While it is set, the website loads the reporter (`/static/vendor/sentry.min.js`) |
| `ERROR_REPORTS_APP_DSN` | (empty) | The phone app's errors. The app asks `GET /api/v1/auth/methods` and reports only while this is set |
| `ERROR_REPORTS_ENVIRONMENT` | `production` | The environment every report is filed under, the forwarded ones too. Set without any DSN, it stops startup |

## Search

| Setting | Default | What it does |
|---|---|---|
| `MEILI_BASE_URL` | required | The Meilisearch container: `http://meilisearch:7700` with the compose file |
| `MEILI_EMBEDDER_MODEL` | `BAAI/bge-base-en-v1.5` | The embedding model Meilisearch runs for search by meaning. Changing it re-embeds everything |
| `MEILI_SEMANTIC_RATIO` | `0.8` | 0 = keywords only, 1 = meaning only |
| `SEARCH_MAX_HITS` | `1000` | The most matches one search can page through in a section (Meilisearch's `pagination.maxTotalHits`, set on both indexes at each library sync and catalog build). A technical limit ([limits](limits.md#technical-limits-not-usage-caps)): a section that stops there says so |
| `SEARCH_MAX_PAGES` | `25` | The last page of one search ("Show more" stops there, and the answer says so: "That's as far as one search goes (page 25); add words to narrow it down."). Page N holds every match of pages 1 to N, so it reads TMDB's search pages 1 to N for each wording: a deep page not in the 10-minute cache costs up to 2 x N TMDB calls (25: at most 50). At most 500 (TMDB's last). A technical limit ([limits](limits.md#search-and-the-ais-best-guess)) |
| `CATALOG_ENABLED` | `true` | Search also covers every TMDB title with enough votes (the "catalog" index, rebuilt every Sunday) |
| `CATALOG_MOVIE_MIN_VOTES`, `CATALOG_TV_MIN_VOTES` | `5` | Votes a title needs to be in the catalog (5: about 200,000 titles) |
| `HIDE_ADULT_TITLES` | `false` | `true` leaves out what TMDB marks adult, in search, the catalog, Home, Discover and smart lists |
| `TMDB_LANGUAGE` | `en-US` | TMDB's language for titles and plots |
| `WATCH_REGION` | `US` | The country for "where to watch", streaming-service rows and age ratings |

## Server and Docker

| Setting | Default | What it does |
|---|---|---|
| `RUN_JOBS` | `true` | Read by the worker only (the API never runs jobs): `false` runs no schedule; the queued work (webhook plays syncs, Jellyfin marks, AI rows) still runs |
| `DATABASE_URL` | set in the compose file | `postgresql://user:password@host:5432/movienight`, built from `POSTGRES_PASSWORD`. The compose file's value beats `.env`. The Python version's `postgresql+psycopg://` form is refused with a message |
| `VALKEY_URL` | set in the compose file | The shared Valkey (`redis://valkey:6379/0`): caches, the sign-in lockout, the TV pairing limit, the OIDC sign-in's state and the work queues the API hands the worker. Every API replica and worker must use the same one. Nothing in it needs a backup |
| `WEB_DIR` | set in the image (`/app/web`) | The website's files (`web/`) |
| `KODI_ADDON_DIR`, `JELLYFIN_PLUGIN_DIR` | set in the image | The Kodi add-on's and the Jellyfin plugin's folders, served at `/kodi/` and `/jellyfin/`. Unset: that download answers 503 naming the setting |
| `AI_NETWORK_NAME` | `movienight-ai` | Read by the compose file: the external Docker network api and worker join to reach an AI container. Create it once (`docker network create movienight-ai`), even without AI |
| `LOG_MAX_SIZE` | `10m` | Read by the compose file: each container's log file is rotated at this size (Docker's `json-file` `max-size`) |
| `LOG_MAX_FILES` | `5` | ... and at most this many files are kept, the oldest dropped first. With the defaults a container's log is at most 50 MB; it all goes when the container is replaced. What the app writes there: [limits](limits.md#data-kept-and-data-deleted) |

The server reads only its environment, never a file of its own.

Names an `.env` from the Python version may still have, read by nothing
now and safe to delete: `SESSION_SECRET`, `TRAKT_EXPORT_DIR`,
`SEERR_API_USER`, `MOVIENIGHT_DISABLE_SCHEDULER`, `MOVIENIGHT_NO_ENV_FILE`,
`SEARCH_AI_DAILY_LIMIT`, `MDBLIST_DAILY_BUDGET`.

## What turns each feature on

| Feature | Needs | Without it |
|---|---|---|
| Plays from Jellyfin into history | each person connects Jellyfin in Profile | Their plays don't come in |
| Instant plays (Webhook plugin) and progress (Movie Night plugin) | `JELLYFIN_WEBHOOK_SECRET` | 503; plays still arrive within 5 minutes |
| Requests | `SEERR_BASE_URL` + `SEERR_API_KEY`, or `DRY_RUN=true` | No Request button |
| MDBList lists, outside scores, search fallback | `MDBLIST_API_KEY` | Lists page shows what was copied before (or nothing) |
| "Picked for you" rows, the digests' wording | `LITELLM_API_KEY` + `LITELLM_MODEL` | No AI rows; digests use plain wording |
| AI best guess in search | `SEARCH_AI_MODEL` + `LITELLM_API_KEY` | No "Best guess" section |
| Digest emails | `SMTP_HOST`, or `SMTP2GO_API_KEY_FILE` with `MAIL_SENDER=smtp2go` | No emails |
| Password autofill in the iPhone app | `APPLE_APP_ID` and an app build naming your host | The app signs in without autofill |
| Expo Go sign-in | `APP_DEV_REDIRECTS=true` | Only the real app (`movienight://`) can sign in |
| Error reports | `ERROR_REPORTS_SERVER_DSN`, `ERROR_REPORTS_WEB_DSN`, `ERROR_REPORTS_APP_DSN` | Errors go only to the log |
| Admin buttons (library rescan, sync every MDBList list, removing a catalog list) | `OWNER_USERNAME`, `ADMIN_USERS` | Hidden |
