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
| `media` | The media service, on :8100: scans library folders and plays their files with ffmpeg (see [The media service](#the-media-service)). It needs no database and runs next to the files. |
| `media -healthcheck` | Asks the media service in the same container for `/healthz`, as `api -healthcheck` does. |
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
docker run -e MEDIA_SECRET=... -e MEDIA_LIBRARIES=/media -v /srv/media:/media:ro \
  --entrypoint /usr/local/bin/media movienight-go
```

The image is Debian slim with a pinned ffmpeg (`server/Dockerfile`), which
only `media` runs.

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
| Account | `/me/jellyfin` (status, Quick Connect, password, unlink, plays without a date and hiding one), `/me/players`, `/me/scrobble-token` | `internal/rw/profile` |
| Play | `/title/{media}/{id}/play` (GET is the Play sheet, POST the player's link), `/play/open`, `/play/{id}` with `/back` and `/finish` | `internal/rw/devices`, `internal/playback` |
| Play here | `/title/{media}/{id}/watch` (a stream ticket, tracks, chapters, segments, `source`), `/watch/report` (start, progress, pause, stop) | `internal/rw/devices`, `internal/playback`, `internal/jfplay` |
| Subtitles online | `/subtitles/languages`, `/title/{media}/{id}/subtitles` (GET the file's stored tracks, POST to add a search result) and its `/search`, `/subtitles/{id}/file`, `/subtitles/{id}/delay` (PUT), `/subtitles/{id}` (DELETE, by the person who added it or an admin). Each route about a file takes the play's stream ticket | `internal/rw/subtitles`, `internal/subtitles` |
| Stream proxy | `/stream/{ticket}/...`: the file with Range, HLS playlists and segments, subtitles as WebVTT or ASS, and embedded fonts, from Jellyfin (its playlists rewritten to point back here, keys removed) or from the media service. Not in the API description: it serves bytes | `internal/stream` |
| Libraries (admins) | `/libraries` (list, add), `/libraries/{id}` (change, remove), `/libraries/{id}/scan`, `/libraries/{id}/problems`, `/libraries/files/{id}/match` | `internal/rw/libraries`, `internal/libraries` |
| Scrobbles | `/scrobble` for Kodi and other players, `/scrobble/jellyfin` for the Webhook plugin, `/scrobble/jellyfin/events` for the Movie Night plugin | `internal/rw/devices`, `internal/scrobble` |
| Manual syncs | `/sync/plays`, and `/sync/library` and `/sync/lists` for admins | `internal/rw/devices` |
| Digest emails | `/me/emails`, `/me/digests/{kind}`, `/me/digests/{id}/html`, `/me/digests/{id}/send` | `internal/rw/digests`, `internal/digest` |
| Downloads | `/kodi/` (the add-on zip) and `/jellyfin/manifest.json` (the plugin repository), without sign-in. The zips are built once at start with fixed dates, so every replica serves identical bytes. | `internal/addons` |
| Error reports | `/errors/web` and `/errors/app`: the website's and the app's Sentry envelopes, checked, limited and forwarded to the collector with the server's DSN. The server's own ERROR lines go there too | `internal/errreports` |
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
- `internal/errreports` sends errors to a Sentry-compatible collector. The
  api and the worker wrap their log, so every ERROR line, panics included,
  becomes a report, scrubbed of people and secrets. The relay takes the
  website's and the app's envelopes, checks their Origin or
  `X-Requested-With`, caps them at 200 KB, limits them in Valkey
  (`rl:errors-<web|app>-addr:*` and `rl:errors-<web|app>-all:*`), keeps only
  error events, strips users, headers and query strings, and forwards them
  with the configured DSN. It needs no database query, and the client has
  its answer before the forward starts.
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
  watch. It also runs Play here: a play session with the player
  `in-browser`, which is never asked "Finished it?" because a stop past
  90 % or into the credits records the watch through the same `Finish`.
- `internal/playback/library.go` is Play here from a library: the plan,
  the tracks from the probe (external subtitle files after the file's own
  streams), chapters named Intro, Credits, Recap or Preview as Skip
  segments, and the next episode the library holds. A title in a library
  plays from there; otherwise from Jellyfin when it is set up. Reports,
  resume and the watch at the end are the same code for both; a library
  play sends Jellyfin no playback reports, and its stop ends its ffmpeg at
  once. When Jellyfin has the same film or episode, the watch recorded at
  the end marks it played there too.
- `internal/jfplay` is Jellyfin's side of Play here: PlaybackInfo with a
  device profile made from what the browser says it decodes, chapters,
  media segments, the next episode, and the playback reports. It runs as
  the person's own Jellyfin token, or the install's key when they linked
  none.
- `internal/stream` is the media proxy. A ticket is HMAC-SHA256 over its
  claims (person, item, media source, Jellyfin play session, expiry six
  hours later), with a key derived from `SECRET_KEY`. The proxy checks only
  the ticket, so a segment costs no database or session read. It streams
  with `io.Copy` on its own keep-alive pool to Jellyfin, passes Range
  through, and moves the write deadline forward chunk by chunk, so the
  server's five-minute `WriteTimeout` never cuts a film.
- `internal/libraries` is the API's and the worker's side of the media
  libraries: the libraries, scans through the media service (a Valkey
  queue for Scan now, the `media-scan` job every 30 minutes), naming and
  TMDB matching (an id in the name, else TMDB's search with the year, each
  name asked once per scan), hand matches that stay over rescans, the
  library search documents (`mn-*` ids in the `library` index), and the
  lookups playback needs. Files are `media_files`; the view
  `library_titles` puts the matched, readable ones next to Jellyfin's
  `library_items`, and the library rows, the cards' in-library mark and the
  digests read the view.
- `internal/media/...` is the media service and what it shares with the
  API. `naming` reads a film or episode from a path, `sidecar` finds
  subtitle files and normalises languages, `probe` runs and reads ffprobe,
  `scan` walks folders incrementally with a bounded probe pool, `plan`
  decides direct, remux or transcode, `engine` runs ffmpeg, `mediasrv` is
  its HTTP side, `mediaclient` the API's client, and `protocol` the routes
  both use.
- `internal/subtitles` is the player's Search online. Each site is one
  `Source` (OpenSubtitles, SubDL, SubSource, Jimaku, Podnapisi, Gestdown)
  that knows only its own requests and answers. The service asks every
  source at once with an 8 s timeout each, ranks the merged answer (the
  file's moviehash match, then the release name closest to the file's
  name, then downloads and rating) and keeps it in the cache for an hour.
  The moviehash is two 64 KB Range reads through the stream proxy
  (`stream.Proxy.ReadRange`), from Jellyfin or from the media service for a
  library file, kept a day per file. Each result's download
  address stays in Valkey, so a client names a result by source and id
  and can never point a download elsewhere. A pick is unzipped, turned
  into UTF-8 (the language picks the charset of an old file), and stored
  in `online_subtitles` as WebVTT, or as ASS for JASSUB, under the file
  it was picked for: Jellyfin's media source, or `lib-<media_files.id>`
  for a library file. The delay is stored with it.
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
| `task:media-scan`, `task:media-scan:pending:<id>`, `task:media-scan:running:<id>` | Scan now and a library's add or change | The worker, which scans; the running key keeps two workers off one library |
| `media:scan:<id>` | The worker, while a scan runs (state, files seen, to read, read) | The Libraries page |
| `ai-rows:fail:<id>` | The worker, after a failed rebuild (back-off from 10 min, doubling to 6 h) | Home, which asks for nothing and says why |
| `seerr:media` (hash), `seerr:media:at` | `internal/userstate` | Every card |
| `c:subs:search:*`, `subs:pick:*`, `subs:file:*` | `internal/subtitles`: a search's answer (an hour), each result's download address (an hour), a file's name and moviehash (a day) | The next search, the pick that follows it |
| `subs:os:login`, `subs:os:quota`, `subs:os:refused` | OpenSubtitles: the shared login until its token expires, a used-up download quota until it resets, a refused login for 15 minutes | Every replica's searches and downloads |

## The media service

`media` runs next to the files: on this machine with
`docker-compose.media.yml` (`MEDIA_FOLDER` mounted read-only at `/media`),
or alone on the machine that holds them with
`docker-compose.media-host.yml`. Only the
api and the worker call it, at `MEDIA_URL`, with
`Authorization: Bearer <MEDIA_SECRET>`. It opens nothing outside
`MEDIA_LIBRARIES`, whatever it is asked. Its routes
(`internal/media/protocol`):

| Route | What it does |
| --- | --- |
| `POST /v1/scan` | Walks a library's folders and answers JSON lines as it goes: progress, each new or changed video with its probe and subtitle files, a folder it couldn't read, and at the end the known files that are gone. The worker sends what it has on record (size, time, subtitle signature), so an unchanged file costs one stat. An empty folder where files were, or one that can't be read, removes nothing |
| `GET /v1/file` | The file itself, with Range |
| `GET /v1/hls/main.m3u8`, `/v1/hls/init.mp4`, `/v1/hls/<n>.m4s` | HLS in fragmented MP4, 6-second segments, every segment listed from the start |
| `GET /v1/subtitles/<index>.<vtt or ass>` | An embedded subtitle as WebVTT, or ASS kept as ASS; with `?name=`, a subtitle file beside the video |
| `GET /v1/attachments/<index>` | An attached font |
| `POST /v1/stop?play=` | Ends a play's ffmpeg and removes its segments |
| `GET /v1/status` | Running jobs and transcodes, the encoder, and the media folders |

The browser never sees it. The api's stream proxy forwards a library
ticket's requests, adding the file, the plan and the play from the ticket,
which is HMAC-signed as for Jellyfin; the browser's query is dropped but
for an external subtitle's name, which the media service checks is one of
that video's own.

How a play is sent (`internal/media/plan`): the file as it is when the
browser plays its container, picture and default audio; a remux (the
picture copied into HLS, the sound copied or made AAC) when the browser
takes the picture's codec but not the container or the sound, or another
audio track is chosen; a transcode to H.264 otherwise, or to burn in a
picture subtitle. A transcode mixes the sound down to stereo AAC, or keeps
5.1 when the browser's output has six channels or more, tone-maps HDR,
scales 4K to 1080p, and uses x264 `veryfast` or the GPU (`MEDIA_HWACCEL`).

An HLS play is one ffmpeg per play and plan (`internal/media/engine`). It
starts at the first segment asked for and is restarted at a segment more
than four past what it has made, so a seek far ahead never waits for
everything before it. A transcode forces a keyframe every 6 seconds and
keeps the file's timestamps (`-copyts`), so runs started anywhere line up.
ffmpeg is paused (SIGSTOP) ten minutes past the last segment the player
asked for and carried on when the player is within six minutes. The
player reads two and a half minutes ahead of what it shows, so it never
waits on ffmpeg, and a seek within the next ten minutes finds its
segment made. A player reading segment after segment is never taken for
a seek, however far ahead of the picture it is. A play ends when the player
reports a stop, moves to another audio track, or asks for nothing for two
minutes; its folder goes with it. `MEDIA_MAX_TRANSCODES` caps transcodes;
Play here says "try again in a moment" when they are all taken. Extracted
subtitles (all of a file's text tracks in one read) and fonts are kept in
`MEDIA_CACHE_DIR` and removed after three days unused.

Intro and credits come from chapters named for them. Finding an intro by
matching the audio of a show's episodes, as Jellyfin's Intro Skipper does,
is a later step.

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
- The queues (webhook plays syncs, Jellyfin marks, AI rows, library scans)
  run on every worker. Exactly one worker takes each request.

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
digest markers, 0022 a time zone picked by hand, 0023 subtitles added
online, 0024 the media libraries.

## Testing

```sh
server/scripts/test.sh                      # every package
server/scripts/test.sh ./internal/rw/...    # go test arguments pass through
TEST_PG_PORT=55450 TEST_VALKEY_PORT=55451 server/scripts/test.sh   # other ports
```

The tests run in `golang:1.27.3-trixie` with ffmpeg added
(`scripts/dev.Dockerfile`, built by `go.sh` the first time). The media
tests make their own small clips with ffmpeg (`internal/media/mediatest`)
and run the real ffmpeg on them: direct play, remux, transcode, seeking,
the transcode limit, subtitles and fonts. Nothing binary is committed.

`test.sh` starts a throwaway Postgres (`pgvector/pgvector:pg17`, the
production image) and an in-memory Valkey. It runs the packages one at a
time with GOMAXPROCS=4 and removes both at the end.

The tests are behaviour tests over real HTTP against the real database.
`internal/testenv` builds the app exactly as `cmd/api` does.
`internal/testenv/fakes` stands in for TMDB, Seerr, Meilisearch, MDBList,
Jellyfin, the subtitle sites and the AI model over HTTP
(`testenv.StartFaked`). `internal/subtitles` tests each site's client
against a fake of its own answers. A service's own
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

All three play a title with Play here. `e2e/run.sh` makes a 60-second clip
with ffmpeg (two audio tracks, two SRT subtitles, an ASS one) and the fake
Jellyfin serves it as the file and as HLS (`fakes/jellyfin_play.go`). The
flows check direct play, Skip intro, the subtitle delay, Search online
against the fake subtitle sites (`fakes/subtitles.go`: the hash match first,
the picked subtitle shown at once, its delay kept on the server and back
after a reload, then removed), speed, switching audio onto HLS, JASSUB,
chapters, resume and the watch recorded at the end.
Linux WebKit's GStreamer stalls when a file is played again after a seek
made while paused, so the flows seek while playing and start a fresh play
after their paused seeks. Safari and Chromium do not do this.

```sh
PLAYWRIGHT_DIR=/path/to/node_modules e2e/run.sh
E2E_SUITE=libraries PLAYWRIGHT_DIR=/path/to/node_modules e2e/run.sh
```

The libraries suite (`e2e/libraries.mjs`) is a fresh install with no
Jellyfin and a real media service on clips run.sh makes: an admin adds the
libraries, the scan finds the files, and Chromium on a desktop and as a
phone plays them direct, remuxed and transcoded, switches audio, shows
subtitles, seeks, resumes and records the watch at the end.

`e2e/run.sh` starts Postgres and Valkey as `test.sh` does. It serves the
app with the fakes and a seeded library through
`internal/server/e2e_serve_test.go`, which does nothing unless
`E2E_SERVE=1`. Then it runs the flows and removes everything. The ports are
`E2E_PG_PORT`, `E2E_VALKEY_PORT` and `E2E_PORT`. The fake Jellyfin gives
each Quick Connect its own account, so the second browser links its own.
