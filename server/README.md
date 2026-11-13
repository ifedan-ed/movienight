# Movie Night server (Go)

The Go rewrite of Movie Night's server. It serves the JSON API under
`/api/v1`, the sign-in pages, the website (`web/`) and the add-on
downloads, runs the background work in its own process, and replaces the
Python app (`app/`). The API keeps the Python's contract: the same paths,
operation ids, schema names and JSON fields as `docs/openapi.json` (the
phone app's types come from it), the same `{"detail": "..."}` error shape,
and the same `X-Requested-With` rule for writes from a browser.

## Commands

One image holds every command:

| Command | What it does |
| --- | --- |
| `api` | Serves on :8000: the JSON API, the website, sign-in, the add-on downloads. Any number of replicas: everything shared lives in Postgres and Valkey. Refuses to start on a schema it wasn't built for. Never runs background work. |
| `api -openapi` | Prints the OpenAPI description of every route (`docs/openapi.json`). |
| `api -healthcheck` | Asks the API in the same container for `/healthz`; exit 0 when it answers 200 (the compose healthcheck: the image has no shell or curl). |
| `worker` | The scheduled jobs and the queued work the API asks for. One or more (see "The worker"). |
| `worker -run plays,library` | Runs the named scheduled jobs once, in that order, and exits: each one's time and the peak memory are logged. No schedule, no queues, nothing recorded in `job_runs`. |
| `migrate` | Brings the schema up to date. Its own deploy step, before `api` and `worker`. |
| `admin` | Accounts and devices from the command line (`admin user add NAME`, `admin user password NAME`, `admin devices signout NAME`, ...). |

The image's entrypoint is `api`; the other commands are chosen with
`--entrypoint` (`api` refuses any stray argument, so a command given the
wrong way stops with a message instead of starting the API):

```sh
docker build -f server/Dockerfile -t movienight-go .        # from the repository root
docker run --env-file .env --entrypoint /usr/local/bin/migrate movienight-go
docker run --env-file .env movienight-go                    # api
docker run --env-file .env --entrypoint /usr/local/bin/worker movienight-go
docker run --env-file .env --entrypoint /usr/local/bin/worker movienight-go -run catalog
```

## What the API does

| Area | Routes | Package |
| --- | --- | --- |
| Sign-in | OIDC login and callback, the phone app's token swap, `/auth/methods` and `/auth/password` (the app's username and password, PASSWORD_SIGNIN: same token, same lockout as `/signin`), signed-in devices, TV pairing | `internal/api`, `internal/web` |
| Home | `/home`, `/hero`, `/rows/{key}`, `/browse`, `/discover`, `/home/config` (+ reset), `/genres`, `/providers` | `internal/rw/home` |
| Title page | `/title/{media}/{id}` and its `/status`, `/scores`, `/related`, `/request`, `/watchlist`, `/watched`, `/unwatch`, `/rating`, `/favorite`, `/lists`, `/season/{n}`, `/next`, `/episodes`, `/episode/{s}/{e}/favorite`, `/episode/{s}/{e}/rating` | `internal/rw/title` |
| Search | `/search`, `/search/ai`, `/person/{id}`, `/titles/fill`, `/img/jf/{item}` | `internal/rw/search` |
| Lists | `/lists` (+ `/smart`), `/lists/{id}` (+ items, copy, sync), `/collection/{kind}`, `/mdblist/lists`, `/mdblist/lists/{id}`, `/mdblist/follow` | `internal/rw/lists` |
| Profile | `/me`, `/me/time-zone`, `/history`, `/requests`, `/rate-prompt` (+ dismiss), `/export` | `internal/rw/profile` |
| Account | `/me/jellyfin` (status, Quick Connect, password, unlink), `/me/players`, `/me/scrobble-token` | `internal/rw/profile` |
| Play | `/title/{media}/{id}/play` (GET: the Play sheet; POST: the player's link), `/play/open`, `/play/{id}` (+ `/back`, `/finish`) | `internal/rw/devices`, `internal/playback` |
| Scrobbles | `/scrobble` (Kodi and other players), `/scrobble/jellyfin` (Webhook plugin), `/scrobble/jellyfin/events` (Movie Night plugin) | `internal/rw/devices`, `internal/scrobble` |
| Manual syncs | `/sync/plays`, `/sync/{library,lists}` (admin) | `internal/rw/devices` |
| Digest emails | `/me/emails`, `/me/digests/{kind}`, `/me/digests/{id}/html`, `/me/digests/{id}/send` | `internal/rw/digests`, `internal/digest` |
| Downloads | `/kodi/` (the add-on zip), `/jellyfin/manifest.json` (the plugin repository), without sign-in. Zips are built once at start with fixed dates, so every replica serves identical bytes. | `internal/addons` |
| Website | `/`, every app page, `/static/...`, `/sw.js`, `/healthz`, `/.well-known/apple-app-site-association` (names the iPhone app for password autofill) | `internal/web` |

Routes no client calls were dropped: `GET /auth/config`,
`GET /auth/oidc/config`, `POST /auth/oidc/logout`, `GET /me/digests`,
`GET /ratings`, `POST /history/remove` and the `POST` alias of an
episode's rating (`PUT` stays).

### How it is built

- `internal/api` is sign-in and the shared plumbing (errors, the
  description). Every other route is in `internal/rw/<area>`, all on one
  `rw.Deps`: the settings, the database, Valkey, the outside-service
  clients and the services below, built once (`rw.NewDeps`).
  `internal/rw/routes` registers every area from one call in `server.go`.
  The worker builds the same services from the same packages.
- One implementation of each shared piece, used by the API and the worker
  alike:
  - `internal/titles`, the title cache (TMDB metadata in `titles`). Titles
    passing through a page are written only when that changes something,
    in one statement sorted by key (no deadlocks); `Ensure` fetches missing
    ones, at most 8 at a time; `RefreshStale` is the nightly refresh, paged,
    with no cap.
  - `internal/settings`: per-person and install settings (JSON values);
    `MergeApp` merges into a JSON record in one statement.
  - `internal/status`: what Movie Night did by itself (library and plays
    syncs, the catalog, MDBList refusing), written by the syncs, shown in
    Profile to admins and to the person.
  - `internal/players`: built-in and custom players, link checks and
    filling, for `/me/players` and the Play sheet.
  - `internal/history`: every watch, however it is recorded (marked by
    hand, "Finished it?"), merged per title and the person's day (see
    "Time zones").
  - `internal/lists`: lists, MDBList follow and sync (the "Sync now" button
    and the nightly job) and the MDBList copy (saved as pages pass, and
    refreshed nightly).
  - `internal/spelling`: the search vocabulary. The catalog build counts
    words with it (`AddWords`, `Keep`) and search reads the result
    (`catalog_vocab`), so both use one definition of a word.
- "Because you watched" takes its seeds from `RecentSeeds`
  (`db/queries/lists_rows.sql`): the newest distinct titles watched; two
  watched at the same moment go in the order they were recorded (newest
  first), as in the Python.
- `internal/userstate` is a title's state on a card, loaded per page in one
  query. Seerr's status of every title is one Valkey hash shared by all
  replicas, pulled by one replica at a time every 90 s; a request made here
  updates its title at once, and a pull that brings no media never replaces
  the map.
- `internal/cache` keeps outside answers in Valkey with TTLs (shared by
  replicas, one call per key at a time, text keys hashed). A TMDB list
  keeps a copy for a day, served only when TMDB fails.
- `internal/episodes`: season pages, ticks (a season is one statement),
  hearts and ratings. Jellyfin's played marks for ticked episodes are
  queued for a worker (`jfsync.AskMarks`), never fired from the request.
- `internal/playback`: the Play sheet and "Finished it?". Asking and
  finishing are single conditional UPDATEs: a double tap records one watch.
- `internal/jfsync`: the library sync (only a complete read changes
  `library_items`) and each person's plays (one transaction per person,
  batched writes, folded into a watch marked here within 36 h), the webhook
  queue and the Jellyfin marks queue.
- `internal/airows`: "Picked for you". Home calls `Serve` (the cached rows;
  queues a rebuild when they are stale) and `Note`; a worker builds them
  through LiteLLM only.
- `internal/digest`, `internal/mail`: weekly, monthly and yearly emails
  over SMTP or SMTP2GO, each person's periods in their own zone; each send
  is claimed in Postgres first, so no email goes twice.
- `internal/imdb`, `internal/catalog`: IMDb ratings streamed into a new
  table and swapped in; the Meilisearch catalog of rated TMDB titles.

### Time zones

Each person has their own zone (`users.time_zone`, migration 0021): the
IANA name their browser or phone reports, or one they picked in Profile,
never guessed from their address. `/me` gives it with `time_zone_mode`,
`automatic` or `manual` (`users.time_zone_manual`, migration 0022).

- Automatic: the website (`web/static/js/api.js`, `reportTimeZone`) and
  the phone app (`mobile/src/lib/timeZone.ts`) read the device's zone
  (`Intl.DateTimeFormat().resolvedOptions().timeZone`) and, when it
  differs from `time_zone` in `/me`, send it with `PUT
  /api/v1/me/time-zone`: once per page load on the web, at start and on
  coming back to the front on the phone.
- Manual: Profile → Time zone (`web/static/js/views/timezone.js`,
  `mobile/src/components/TimeZoneSheet.tsx`) offers "Automatic (this
  device)" and a searchable list (`GET /api/v1/time-zones`: the IANA's
  `zone.tab`, embedded in `internal/users`, plus UTC, each with its offset
  now; only names both Go and Postgres know, read once per process) and
  saves with `PUT /api/v1/me/time-zone/choice` `{"mode": "manual" |
  "automatic", "time_zone": ...}`. Automatic carries the zone of the device
  it is chosen on, stored at once. While the mode is manual the apps send
  no reports, and the server refuses any that come (an older app, another
  browser) with 409 and changes nothing: the `UPDATE` itself has `NOT
  time_zone_manual`, so a report racing a choice cannot undo it.

Both writes check the name against Go's zone database (built into the
binary) and Postgres's (the `UPDATE` itself uses it with `AT TIME ZONE`);
anything else, `Local` included, is a 422 `{"detail": ...}`. Kodi and the
Jellyfin plugin report none.

`users.Location(user, install)` (also `rw.Deps.Zone`) is the one place a
person's zone is chosen: their reported or picked zone, else `TIMEZONE`,
the documented default for someone who has neither. It is used for
everything computed for a person: history's days and the times written
with their offset, a film's watch count on cards and title pages, "this
year" in Profile, a watch date typed without a time (20:00 that day),
whether an episode has aired ("today"), Home's morning/noon/night row sets
and the daily shuffle of "In your library, not watched yet", the times in
Profile's notices and the scores note, and the digests (below). The
install's `TIMEZONE` itself (`cfg.Location`) is used only for the
install's own work: the worker's schedule and the catalog's last year.
Not zone-dependent: the rate prompt looks back 21 x 24 hours, and the
export writes UTC times.

Digests: a person's week is Monday to Sunday in their zone (the month and
year likewise), due at 09:00 on their Monday (09:30 on the 1st, 10:30 on 1
January). The scheduled runs (install time) and the hourly catch-up each
send whatever is due by then for each person, so someone in Auckland gets
theirs at the first hourly run after 09:00 their Monday. What was done is
kept per person and kind (`digest_done`, the period's first local date):
a person whose zone changes is not sent the same week again; with no row
yet, a period on or before the Python's install-wide marker
(`app_settings` `digest_done:<kind>`, now only read) or due before the
person joined counts as done. Each send is still claimed first, so two
workers never send one twice.

### Valkey keys shared between processes

| Key | Written by | Read by |
| --- | --- | --- |
| `c:jf:*` | cached Jellyfin answers (`c:jf:continue:<user id>`: Home's Up next, 60 s; a season's episodes; the server id) | forgotten by what makes them stale: `c:jf:continue:<id>` by ticks (after the worker's marks), "Finished it?", a plays sync that added plays, plugin events; every `c:jf:*` by a library sync |
| `task:jf-plays`, `task:jf-plays:pending:<id>` | the webhook and plugin (a person's plays, soon) | the worker |
| `task:jf-marks`, `task:jf-marks:items:<id>` | episode ticks (item id -> the latest played/unplayed) | the worker: marks them in Jellyfin |
| `task:ai-rows`, `task:ai-rows:pending:<id>` | Home, when "Picked for you" is missing or stale | the worker: rebuilds; Home says "being made" meanwhile |
| `ai-rows:fail:<id>` | the worker, after a failed rebuild (back-off 10 min doubling to 6 h) | Home: asks for nothing and says why |
| `seerr:media` (hash), `seerr:media:at` | `internal/userstate` | every card |

## The worker

Times are local to `TIMEZONE` (the install's clock; each person's digest
is due at the same local times in their own zone, see "Time zones").

| Job | When | Catch-up at start |
|-----|------|-------------------|
| `plays` | every 5 min | the missed slot |
| `library` | every 30 min | the missed slot |
| `imdb` | daily 02:30 | when none are loaded, or missed within 20 h |
| `mdblist` | daily 04:30: followed lists synced, then the top-lists copy refreshed (`internal/lists`) | missed within 12 h |
| `titles` | daily 05:00 | missed within 12 h |
| `catalog` | Sunday 03:00 | when never built, or missed within 12 h |
| `digest-week` / `-month` / `-year` | Monday 09:00 / 1st 09:30 / 1 January 10:30: everyone whose period is due by then in their zone | no; the hourly catch-up covers them |
| `digest-catch-up` | hourly at :04: everyone whose period is due in their zone and not done (people ahead of or behind `TIMEZONE`, missed runs, failed sends) | the missed slot |
| `cleanup` | hourly at :07 | expired sessions and codes, run records older than 30 days |

A job whose feature is off (no `MDBLIST_API_KEY`, email not set up,
`IMDB_RATINGS_ENABLED=false`, `CATALOG_ENABLED=false`) is not scheduled,
and the log says so.

How several workers share the work:

- The schedule runs on one leader, the holder of a Postgres advisory lock
  on a dedicated connection. The leader pings that connection, and losing
  it stops the schedule. A standby takes over within 5 s.
- Every run is claimed in `job_runs` (migration 0018) under (job, due
  time) before it starts, so no due run is made twice, even during a
  leader change. The table also records how each run ended.
- The queues (webhook plays syncs, Jellyfin marks, AI rows) run on every
  worker. Each request is popped by exactly one.

## Upgrading from the Python app

How to move an install from the Python version, and what behaves
differently, is on the docs site's Backups and upgrades page
(`docs/operations.md`).

## Settings

Every setting, the API's and the worker's, is described in one place:
`docs/configuration.md` (its "Go server" section lists what the Go server
needs that the Python did not: `TIMEZONE`, `VALKEY_URL`, `TRUSTED_PROXY`
and `WEB_DIR` required, `DATABASE_URL` as `postgresql://`, and
`KODI_ADDON_DIR`, `JELLYFIN_PLUGIN_DIR`). The names are the Python's, so
an existing `.env` keeps working.

`DRY_RUN=true` runs an install (a test install, a copy of the live
database checked before an upgrade) with nothing leaving it that changes
another service: no Seerr requests and no Seerr users made, no Jellyfin
played marks, no Jellyfin sign-outs (a copy holds the live install's
tokens), no email (a send succeeds without sending, so a digest is
recorded once). Reads of every service still go out, and so do the
Jellyfin sign-in calls a person makes to link their account. It is
enforced in the clients (`internal/clients`, `internal/mail`), not by each
caller. A required setting left out stops
startup with a message naming it; a half-configured service is an error,
not "off". The request-size limits are set on the published description
too, so validation and `/api/v1/openapi.json` say the same number.

## Developing

Every tool runs in the pinned Go image (`golang:1.27.3-trixie`), nothing
installed on the host:

```sh
server/scripts/go.sh build ./...
server/scripts/go.sh vet ./...
server/scripts/sqlc.sh            # after changing db/queries or adding a migration
```

SQL lives in `db/queries/*.sql` (sqlc generates `internal/store`); schema
changes are numbered files in `db/migrations` (0017 sessions, 0018 job
runs, 0019 and 0020 indexes, 0021 time zones and per-person digest
markers, 0022 a time zone picked by hand).

## Testing

```sh
server/scripts/test.sh                      # every package
server/scripts/test.sh ./internal/rw/...    # go test arguments pass through
TEST_PG_PORT=55450 TEST_VALKEY_PORT=55451 server/scripts/test.sh   # other ports
```

`test.sh` starts a throwaway Postgres (pgvector/pgvector:pg17, the
production image) and Valkey in memory, runs the packages one at a time
with GOMAXPROCS=4, and removes both. Tests are behaviour tests over real
HTTP against the real database: `internal/testenv` builds the app exactly
as `cmd/api` does, and `internal/testenv/fakes` stands in for TMDB, Seerr,
Meilisearch, MDBList, Jellyfin and the AI model over HTTP
(`testenv.StartFaked`); a service's own tests use `internal/testbase` (the
database, Valkey and settings, without the API). SMTP is a Go SMTP server.
Hot queries have EXPLAIN checks (`Server.Explain`) that fail when a table
would be scanned whole. The description test compares every route and
every nested schema with `docs/openapi.json`.

Browser tests (`e2e/flows.mjs`, Playwright: WebKit as an iPhone in
Pacific/Auckland, Chromium as an Android phone in Europe/Berlin, each
checking that its zone is reported once and that a zone picked in Profile
sticks until Automatic, and a Chromium desktop) run
against the Go server:

```sh
PLAYWRIGHT_DIR=/path/to/node_modules e2e/run.sh
```

`e2e/run.sh` starts Postgres and Valkey as `test.sh` does and serves the
app with the fakes and a seeded library through
`internal/server/e2e_serve_test.go` (`E2E_SERVE=1`; it does nothing
otherwise), then runs the flows and removes everything (ports:
`E2E_PG_PORT`, `E2E_VALKEY_PORT`, `E2E_PORT`). The fake Jellyfin gives
each Quick Connect its own account, so the second browser links its own.
Last run 2026-11-13:
every flow passes on WebKit, Chromium and desktop.
