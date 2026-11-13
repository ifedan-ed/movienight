# Movie Night server (Go)

This is Movie Night's server. It serves the JSON API under `/api/v1`, the
sign-in pages, the website (`web/`) and the add-on downloads. Background
work runs in a separate process, the worker. It replaced the earlier
Python app on 2026-11-13.

The API follows `docs/openapi.json`: its paths, operation ids, schema names
and JSON fields. The phone app's types are made from that file. Errors are
`{"detail": "..."}`. A write from a browser must carry
`X-Requested-With: movienight`.

## Commands

One image holds every command:

| Command | What it does |
| --- | --- |
| `api` | Serves the JSON API, the website, sign-in and the add-on downloads on :8000. It never runs background work, so any number of replicas can run: everything shared is in Postgres and Valkey. It refuses to start on a schema it wasn't built for. |
| `api -openapi` | Prints the OpenAPI description of every route (`docs/openapi.json`). |
| `api -healthcheck` | Asks the API in the same container for `/healthz` and exits 0 on a 200. The compose healthcheck uses it, since the image has no shell or curl. |
| `worker` | Runs the scheduled jobs and the work the API queues. One or more can run (see [The worker](#the-worker)). |
| `worker -run plays,library` | Runs the named jobs once, in that order, and exits. It logs each one's time and the peak memory, and records nothing in `job_runs`. |
| `migrate` | Brings the schema up to date. It runs as its own step before `api` and `worker`. |
| `admin` | Accounts and devices: `admin user add NAME`, `admin user password NAME`, `admin user nopassword NAME`, `admin user list`, `admin devices signout NAME`. |

The image's entrypoint is `api`. Choose another command with
`--entrypoint`. `api` refuses stray arguments, so a command given the wrong
way stops with a message instead of starting the API.

```sh
docker build -f server/Dockerfile -t movienight-go .        # from the repository root
docker run --env-file .env --entrypoint /usr/local/bin/migrate movienight-go
docker run --env-file .env movienight-go                    # api
docker run --env-file .env --entrypoint /usr/local/bin/worker movienight-go
docker run --env-file .env --entrypoint /usr/local/bin/worker movienight-go -run catalog
```

In a running stack, `docker compose exec api admin user list`
reaches `admin` the same way.

## What the API does

| Area | Routes | Package |
| --- | --- | --- |
| Sign-in | OIDC login and callback, the phone app's token swap, `/auth/methods` and `/auth/password` for password sign-in in the app, signed-in devices, TV pairing | `internal/api`, `internal/web` |
| Home | `/home`, `/hero`, `/rows/{key}`, `/browse`, `/discover`, `/home/config` and its reset, `/genres`, `/providers` | `internal/rw/home` |
| Title page | `/title/{media}/{id}` and its `/status`, `/scores`, `/related`, `/request`, `/watchlist`, `/watched`, `/unwatch`, `/rating`, `/favorite`, `/lists`, `/season/{n}`, `/next`, `/episodes`, `/episode/{s}/{e}/favorite`, `/episode/{s}/{e}/rating` | `internal/rw/title` |
| Search | `/search`, `/search/ai`, `/person/{id}`, `/titles/fill`, `/img/jf/{item}` | `internal/rw/search` |
| Lists | `/lists` and `/lists/smart`, `/lists/{id}` with its items, copy and sync, `/collection/{kind}`, `/mdblist/lists`, `/mdblist/lists/{id}`, `/mdblist/follow` | `internal/rw/lists` |
| Profile | `/me`, `/me/time-zone`, `/me/time-zone/choice`, `/time-zones`, `/history`, `/requests`, `/rate-prompt` and its dismiss, `/export` | `internal/rw/profile` |
| Account | `/me/jellyfin` (status, Quick Connect, password, unlink), `/me/players`, `/me/scrobble-token` | `internal/rw/profile` |
| Play | `/title/{media}/{id}/play` (GET is the Play sheet, POST the player's link), `/play/open`, `/play/{id}` with `/back` and `/finish` | `internal/rw/devices`, `internal/playback` |
| Scrobbles | `/scrobble` for Kodi and other players, `/scrobble/jellyfin` for the Webhook plugin, `/scrobble/jellyfin/events` for the Movie Night plugin | `internal/rw/devices`, `internal/scrobble` |
| Manual syncs | `/sync/plays`, and `/sync/library` and `/sync/lists` for admins | `internal/rw/devices` |
| Digest emails | `/me/emails`, `/me/digests/{kind}`, `/me/digests/{id}/html`, `/me/digests/{id}/send` | `internal/rw/digests`, `internal/digest` |
| Downloads | `/kodi/` (the add-on zip) and `/jellyfin/manifest.json` (the plugin repository), without sign-in. The zips are built once at start with fixed dates, so every replica serves identical bytes. | `internal/addons` |
| Website | `/`, every app page, `/static/...`, `/sw.js`, `/healthz`, and `/.well-known/apple-app-site-association`, which names the iPhone app for password autofill | `internal/web` |

## How it is built

`internal/api` holds sign-in and the shared plumbing: errors and the
description. Every other route is in `internal/rw/<area>`. They all share
one `rw.Deps`, built once by `rw.NewDeps`: the settings, the database,
Valkey, the outside-service clients and the services below.
`internal/rw/routes` registers every area from one call in `server.go`. The
worker builds the same services from the same packages.

Each shared piece has one implementation, used by the API and the worker
alike:

- `internal/titles` is the title cache (TMDB metadata in `titles`). A
  title passing through a page is written only when something changed, in
  one statement sorted by key, so there are no deadlocks. `Ensure` fetches
  missing titles, at most 8 at a time. `RefreshStale` is the nightly
  refresh, paged, with no cap.
- `internal/settings` holds per-person and install settings as JSON values.
  `MergeApp` merges into a JSON record in one statement.
- `internal/status` records what Movie Night did by itself: the library and
  plays syncs, the catalog, MDBList refusing. Profile shows it to admins and
  to the person.
- `internal/players` has the built-in and custom players, link checks and
  link filling, for `/me/players` and the Play sheet.
- `internal/history` records every watch, however it came in, merged per
  title and per day in the person's zone (see [Time zones](#time-zones)).
- `internal/lists` has lists, MDBList follow and sync (the Sync now button
  and the nightly job) and the MDBList copy, saved as pages pass and
  refreshed nightly.
- `internal/spelling` is the search vocabulary. The catalog build counts
  words with it (`AddWords`, `Keep`) and search reads the result
  (`catalog_vocab`), so both use one definition of a word.
- `internal/userstate` is a title's state on a card, loaded per page in one
  query. Seerr's status of every title is one Valkey hash shared by all
  replicas. One replica at a time pulls it every 90 s. A request made here
  updates its title at once, and a pull that brings no media never replaces
  the map.
- `internal/cache` keeps outside answers in Valkey with TTLs. Replicas share
  it, one call per key runs at a time, and text keys are hashed. A TMDB
  list keeps a copy for a day, served only when TMDB fails.
- `internal/episodes` has season pages, ticks (a season is one statement),
  hearts and ratings. Jellyfin's played marks for ticked episodes are
  queued for a worker (`jfsync.AskMarks`), never sent from the request.
- `internal/playback` has the Play sheet and "Finished it?". Asking and
  finishing are single conditional UPDATEs, so a double tap records one
  watch.
- `internal/jfsync` has the library sync, the plays sync, the webhook queue
  and the Jellyfin marks queue. Only a complete read changes
  `library_items`. Each person's plays are one transaction with batched
  writes, and a play within 36 h of a watch marked here is folded into it.
- `internal/airows` makes "Picked for you". Home calls `Serve`, which
  returns the cached rows and queues a rebuild when they are stale, and
  `Note`. A worker builds the rows, always through LiteLLM.
- `internal/digest` and `internal/mail` send the weekly, monthly and yearly
  emails over SMTP or SMTP2GO, each person's periods in their own zone.
  Each send is claimed in Postgres first, so no email goes twice.
- `internal/imdb` streams IMDb's ratings into a new table and swaps it in.
  `internal/catalog` builds the Meilisearch catalog of rated TMDB titles.

"Because you watched" takes its seeds from `RecentSeeds`
(`db/queries/lists_rows.sql`): the newest distinct titles watched. Two
watched at the same moment go newest-recorded first.

### Time zones

Each person has their own zone in `users.time_zone`. It is the IANA name
their browser or phone reports, or one they picked in Profile. It is never
guessed from their address. `/me` returns it with `time_zone_mode`,
`automatic` or `manual` (`users.time_zone_manual`).

- Automatic: the website (`reportTimeZone` in `web/static/js/api.js`) and
  the phone app (`mobile/src/lib/timeZone.ts`) read the device's zone. When
  it differs from `/me`, they send it with `PUT /api/v1/me/time-zone`. The
  website does this once per page load. The phone does it at start and when
  it comes back to the front.
- Manual: Profile → Time zone (`web/static/js/views/timezone.js`,
  `mobile/src/components/TimeZoneSheet.tsx`) offers "Automatic (this
  device)" and a searchable list. The list comes from
  `GET /api/v1/time-zones`: the IANA `zone.tab` embedded in
  `internal/users`, plus UTC, each with its current offset. Only names that
  both Go and Postgres know are listed. The choice is saved with
  `PUT /api/v1/me/time-zone/choice` and
  `{"mode": "manual" | "automatic", "time_zone": ...}`. Choosing Automatic
  stores the zone of the device it was chosen on.
- While the mode is manual the apps send no reports. The server refuses any
  that still come, from an older app or another browser, with 409. The
  `UPDATE` itself has `NOT time_zone_manual`, so a report racing a choice
  cannot undo it.

Both writes check the name against Go's zone database, which is built into
the binary, and against Postgres's. Anything else, `Local` included, is a
422. Kodi and the Jellyfin plugin report no zone.

`users.Location(user, install)` (also `rw.Deps.Zone`) is the one place a
person's zone is chosen: their own zone, or else `TIMEZONE`. It is used for
everything computed for a person:

- history's days, and times written with their offset;
- a film's watch count on cards and title pages, and "this year" in Profile;
- a watch date typed without a time, which becomes 20:00 that day;
- whether an episode has aired "today";
- Home's morning, noon and night row sets, and the daily shuffle of "In
  your library, not watched yet";
- the times in Profile's notices and the scores note;
- the digests.

The install's own `TIMEZONE` (`cfg.Location`) is used only for the
install's work: the worker's schedule and the catalog's last year. The rate
prompt looks back 21 × 24 hours and the export writes UTC, so neither
depends on a zone.

A person's digest week runs Monday to Sunday in their zone, and the month
and year likewise. It is due at 09:00 on their Monday, 09:30 on the 1st,
or 10:30 on 1 January. The scheduled runs and the hourly catch-up send
whatever is due by then, so someone in Auckland gets theirs at the first
hourly run after 09:00 their Monday. `digest_done` records what was sent
per person and kind, as the period's first local date, so a person whose
zone changes doesn't get the same week again. With no row yet, a period
counts as done if it is on or before the Python's install-wide marker
(`app_settings` `digest_done:<kind>`, now only read) or was due before the
person joined.

### Valkey keys shared between processes

| Key | Written by | Read by |
| --- | --- | --- |
| `c:jf:*` | Cached Jellyfin answers: `c:jf:continue:<user id>` (Home's Up next, 60 s), a season's episodes, the server id | Dropped by whatever makes them stale. `c:jf:continue:<id>` by ticks (after the worker's marks), "Finished it?", a plays sync that added plays and plugin events. Every `c:jf:*` by a library sync |
| `task:jf-plays`, `task:jf-plays:pending:<id>` | The webhook and the plugin, asking for a person's plays soon | The worker |
| `task:jf-marks`, `task:jf-marks:items:<id>` | Episode ticks: item id to the latest played or unplayed | The worker, which marks them in Jellyfin |
| `task:ai-rows`, `task:ai-rows:pending:<id>` | Home, when "Picked for you" is missing or stale | The worker, which rebuilds them. Home says "being made" meanwhile |
| `ai-rows:fail:<id>` | The worker, after a failed rebuild (back-off from 10 min, doubling to 6 h) | Home, which asks for nothing and says why |
| `seerr:media` (hash), `seerr:media:at` | `internal/userstate` | Every card |

## The worker

Every job, its time and its catch-up at start are in
[docs/limits.md](../docs/limits.md#background-jobs). The jobs are defined
in `cmd/worker/jobs.go`.

How several workers share the work:

- The schedule runs on one leader, the holder of a Postgres advisory lock
  on a dedicated connection. The leader pings that connection, and losing
  it stops the schedule. A standby takes over within 5 s.
- Every run is claimed in `job_runs` under its job and due time before it
  starts, so no due run is made twice, even during a leader change. The
  table also records how each run ended.
- The queues (webhook plays syncs, Jellyfin marks, AI rows) run on every
  worker. Exactly one worker takes each request.

## Performance

Measured against the Python app on a copy of the live database before the
switch, the same
machine for both, median of 3 after a warm-up. Over all 628 calls the Go
server's median was 6.9 ms against the Python's 30.4 ms, and the 90th
percentile 56 ms against 106 ms.

| | Go | Python |
| --- | --- | --- |
| Read endpoints | 2–20 ms | 10–120 ms |
| Home rows | 4–20 ms | 25–140 ms |
| Title pages | 10–20 ms | 40–60 ms |
| Search (Meilisearch and TMDB time included) | 100–300 ms | 170–460 ms |
| Play sheet (mostly Jellyfin's time) | 90–190 ms | 90–190 ms |
| Memory at rest | api 14 MB | web 116 MB |

Single worker runs (`worker -run`): plays 0.5 s and 21 MB; library 15.7 s
and 56 MB, waiting on Jellyfin; titles 0.4 s and 21 MB; the catalog 433 s
and 211 MB peak for 204,714 titles.

## Settings

Every setting is in [docs/configuration.md](../docs/configuration.md).
A required setting left out stops startup with a message naming it, and a
half-configured service is an error, not "off".

`DRY_RUN=true` is enforced in the clients (`internal/clients`,
`internal/mail`), not by each caller. The request-size limits are set on
the published description too, so validation and
`/api/v1/openapi.json` give the same numbers.

## Developing

Every tool runs in the pinned Go image (`golang:1.27.3-trixie`). Nothing is
installed on the host.

```sh
server/scripts/go.sh build ./...
server/scripts/go.sh vet ./...
server/scripts/sqlc.sh            # after changing db/queries or adding a migration
```

SQL lives in `db/queries/*.sql`, and sqlc generates `internal/store` from
it. Schema changes are numbered files in `db/migrations`: 0017 sessions,
0018 job runs, 0019 and 0020 indexes, 0021 time zones and per-person
digest markers, 0022 a time zone picked by hand.

## Testing

```sh
server/scripts/test.sh                      # every package
server/scripts/test.sh ./internal/rw/...    # go test arguments pass through
TEST_PG_PORT=55450 TEST_VALKEY_PORT=55451 server/scripts/test.sh   # other ports
```

`test.sh` starts a throwaway Postgres (`pgvector/pgvector:pg17`, the
production image) and an in-memory Valkey. It runs the packages one at a
time with GOMAXPROCS=4 and removes both at the end.

The tests are behaviour tests over real HTTP against the real database.
`internal/testenv` builds the app exactly as `cmd/api` does.
`internal/testenv/fakes` stands in for TMDB, Seerr, Meilisearch, MDBList,
Jellyfin and the AI model over HTTP (`testenv.StartFaked`). A service's own
tests use `internal/testbase`, which gives the database, Valkey and
settings without the API. SMTP is a Go SMTP server. Hot queries have
EXPLAIN checks (`Server.Explain`) that fail when a table would be scanned
whole. The description test compares every route and nested schema with
`docs/openapi.json`.

The browser tests (`e2e/flows.mjs`, Playwright) run against this server:

- WebKit as an iPhone in Pacific/Auckland;
- Chromium as an Android phone in Europe/Berlin;
- Chromium on a desktop.

The two phones each check that their zone is reported once, and that a
zone picked in Profile sticks until Automatic is chosen again.

```sh
PLAYWRIGHT_DIR=/path/to/node_modules e2e/run.sh
```

`e2e/run.sh` starts Postgres and Valkey as `test.sh` does. It serves the
app with the fakes and a seeded library through
`internal/server/e2e_serve_test.go`, which does nothing unless
`E2E_SERVE=1`. Then it runs the flows and removes everything. The ports are
`E2E_PG_PORT`, `E2E_VALKEY_PORT` and `E2E_PORT`. The fake Jellyfin gives
each Quick Connect its own account, so the second browser links its own.
