# Self-hosting

This guide takes you from an empty server to Movie Night running at your own address, with accounts for your people. Plan on about 20 minutes.

## What you need

- A Linux server with Docker and the Compose plugin (`docker compose version`). 2 CPUs and 2 GB of RAM are plenty for a household; Meilisearch likes a little more for a big library. Converting a film the browser can't play takes about one CPU core per film playing at 1080p; most films play without it.
- A domain name pointing at the server, and a reverse proxy for TLS (Caddy, nginx, Traefik...).
- Your films and shows in a folder on the server, or on another machine (step 5). Jellyfin is optional: Movie Night plays the files itself.
- A free TMDB API key: <https://www.themoviedb.org/settings/api>.
- Optional: Seerr (requests), MDBList (lists and outside scores), an OpenAI-compatible AI endpoint (AI rows and search guesses) and SMTP (digest emails).

## 1. Get the code and settings

```sh
git clone https://github.com/ifedan-ed/movienight.git
cd movienight
cp .env.example .env
chmod 600 .env
```

Open `.env` and set at least:

| Setting | What to put |
|---|---|
| `SECRET_KEY` | A long random string: `openssl rand -base64 48`. Keep it: it encrypts each person's Jellyfin token. |
| `POSTGRES_PASSWORD` | Another long random string. |
| `MEILI_MASTER_KEY`, `MEILI_API_KEY` | One more random string, the same in both. |
| `TMDB_API_KEY` | Your TMDB key. |
| `MEDIA_FOLDER` | The folder that holds your films and shows, e.g. `/srv/media` (or the media service on another machine: step 5). |
| `MEDIA_SECRET` | One more random string: `openssl rand -base64 32`. |
| `PUBLIC_URL` | Your address, e.g. `https://movies.example.com`. |
| `OWNER_USERNAME` | Your own username (you'll make it below). The owner is always an admin. |
| `TIMEZONE` | Your time zone, e.g. `Europe/London`: the clock the background jobs run on. Each person's own dates follow the zone their browser or phone reports, or one they pick in Profile. |
| `TRUSTED_PROXY` | Your reverse proxy's address as Movie Night sees it (step 3). |
| `PASSWORD_SIGNIN` | `true` to sign in with a username and password (see [Sign-in](sign-in.md) for OIDC). |

Every other setting is in the [configuration reference](configuration.md). A required setting left empty stops the server with a message naming it, so a mistake shows at once in `docker compose logs api`.

## 2. Start it

The Compose file joins a Docker network for an optional AI container. Create it once, even if you don't use AI:

```sh
docker network create movienight-ai
docker compose up -d --build
docker compose ps                         # migrate exited 0, api healthy, worker running
curl -s http://127.0.0.1:8137/healthz     # {"status":"ok"}
```

The first build takes a few minutes (it also builds the Jellyfin plugin). The stack is:

| Container | What it does |
|---|---|
| `migrate` | Runs once on every start, before the others: brings the database schema up to date, then exits. |
| `api` | Serves the website, the API, sign-in and the add-on downloads on `127.0.0.1:8137`. |
| `worker` | Runs the background jobs (library scans, Jellyfin syncs, IMDb ratings, the search catalog, digest emails) and the work the API hands it. |
| `media` | Scans your folders and plays the files with ffmpeg. Only `api` talks to it. |
| `postgres` | Everyone's data, and the sign-in sessions. |
| `meilisearch` | The search indexes. |
| `valkey` | Shared caches, the sign-in lockout and the worker's queues. Nothing in it needs keeping. |

## 3. Put it behind your reverse proxy

Movie Night listens on `127.0.0.1:8137` only. With Caddy:

```caddy
movies.example.com {
    reverse_proxy 127.0.0.1:8137
}
```

With nginx, proxy `/` to `http://127.0.0.1:8137` and pass `Host`, `X-Forwarded-For` and `X-Forwarded-Proto`.

Movie Night counts wrong passwords and TV sign-in codes per visitor address, and believes the `X-Forwarded-For` header only from the address in `TRUSTED_PROXY`. A proxy on the host reaching `127.0.0.1:8137` arrives from the gateway of the Compose network:

```sh
docker network inspect movienight_default --format '{{(index .IPAM.Config 0).Gateway}}'
```

Put that address in `TRUSTED_PROXY` (for a proxy running as a container on that network, its own address; with nothing in front, `none`) and run `docker compose up -d`. Until it is set, `api` and `worker` stop at start with a message naming it; the network exists from the first `docker compose up`.

## 4. Make your accounts

```sh
docker compose exec api admin user add yourname --name "Your Name"
docker compose exec api admin user add alice --name "Alice" --email alice@example.com
```

Each command asks for the password twice. Open your address and sign in. People can change their own password under Profile → Account. More in [Sign-in](sign-in.md).

## 5. Media libraries

`MEDIA_FOLDER` is mounted read-only into the `media` container as `/media`. Sign in as the owner, open Profile → Libraries and add a library: a name, films or shows, and its folders as the container sees them, such as `/media/Films`. A library can have several folders. The scan starts at once, runs again every 30 minutes, and Scan now runs it whenever you like. Only new and changed files are read again; a file that is gone leaves the library.

Name files the way Radarr, Sonarr, Plex or Jellyfin do and they match on their own:

```
/media/Films/Inception (2010)/Inception (2010).mkv
/media/Films/Heat (1995) [tmdbid-949]/Heat (1995) - 1080p.mkv
/media/Shows/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01 - Pilot.mkv
```

A TMDB, IMDb or TVDB id in a folder or file name (`[tmdbid-949]`, `{imdb-tt0113277}`, `[tvdbid-81189]`) settles the match. Subtitle files beside a video are found by name, with the language in it: `Inception (2010).en.srt`, `Inception (2010).fr.forced.ass`. Trailers, samples and extras folders are left out. A file that can't be read, or whose name matches nothing, is listed under its library; Match puts it right with its TMDB id, and the fix stays over later scans.

Every film or episode plays in the browser with Play here, in the cheapest way the browser allows: the file as it is, the picture copied into HLS when only the container doesn't suit (an MKV, cheap), or converted to H.264 when the browser can't decode the picture (HEVC in Chrome, for example). `MEDIA_MAX_TRANSCODES` caps how many films convert at once (2).

### The media service on another machine

The media service can run on the machine that holds the files, with the rest of Movie Night elsewhere. Only Movie Night's api and worker talk to it, so it needs no database and no other settings.

On the machine with the files, copy `docker-compose.media-host.yml` from the repository and the server image (`docker save movienight-server:latest | ssh files docker load`, or build it there with `docker build -f server/Dockerfile -t movienight-server:latest .`). Next to the compose file, write a `.env` with three lines:

```sh
MEDIA_SECRET=...           # the same as on the Movie Night machine
MEDIA_FOLDER=/srv/media    # the folder with the films and shows
MEDIA_BIND=100.64.0.7      # this machine's private address, such as its Tailscale one
```

Then start it with `docker compose -f docker-compose.media-host.yml up -d`. It listens on port 8100 of that address only.

On the Movie Night machine, in `.env`:

- remove `:docker-compose.media.yml` from `COMPOSE_FILE` (or the whole line), so no local media service starts and `MEDIA_FOLDER` isn't needed;
- set `MEDIA_URL=http://100.64.0.7:8100` and the same `MEDIA_SECRET`;
- run `docker compose up -d`.

Keep port 8100 on a private network or a VPN such as Tailscale: the secret guards every call, but the traffic is plain HTTP. Library folders are written as the media machine's container sees them, under `/media`.

### Hardware encoding

Without a GPU, conversions run on the CPU with x264's `veryfast` preset, and 4K is converted to 1080p. With a GPU, pass its device into the `media` container and say which encoder to use:

| GPU | Setting | Device |
|---|---|---|
| Intel or AMD | `MEDIA_HWACCEL=vaapi` | `devices: ["/dev/dri:/dev/dri"]` |
| Intel Quick Sync | `MEDIA_HWACCEL=qsv` | `devices: ["/dev/dri:/dev/dri"]` |
| NVIDIA | `MEDIA_HWACCEL=nvenc` | the NVIDIA Container Toolkit, `gpus: all` |

The container's user must be allowed to use the device: add `group_add` with the group that owns `/dev/dri/renderD128` (often `render`). The media service tries one frame at start and stops with a message if the encoder doesn't work. Another render node goes in `MEDIA_HWACCEL_DEVICE`.

## 6. Jellyfin (optional)

Set `JELLYFIN_BASE_URL`, `JELLYFIN_PUBLIC_URL`, `JELLYFIN_API_KEY` and `JELLYFIN_USER_ID` (a user who sees every library) to add Jellyfin as a second source. Its library syncs as soon as the worker starts and then every 30 minutes. Each person links their own Jellyfin account in Profile → Jellyfin, and from then on their plays come into their history. A title in both plays from the media library. See [Jellyfin and players](jellyfin.md) for instant plays and the plugin.

## 7. Everything else (optional)

| Feature | Settings |
|---|---|
| Requests | `SEERR_BASE_URL`, `SEERR_API_KEY` |
| Lists and outside scores | `MDBLIST_API_KEY` |
| Subtitles online, beyond Podnapisi and Addic7ed | `OPENSUBTITLES_API_KEY` (with `OPENSUBTITLES_USERNAME` and `OPENSUBTITLES_PASSWORD` for downloads), `SUBDL_API_KEY`, `SUBSOURCE_API_KEY`, `JIMAKU_API_KEY` |
| AI rows, digest wording, plot search guesses | `LITELLM_BASE_URL`, `LITELLM_API_KEY`, `LITELLM_MODEL`, `SEARCH_AI_MODEL` (any OpenAI-compatible endpoint) |
| Weekly digest and month-in-review emails | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM` |
| Password autofill in your own iPhone app build | `APPLE_APP_ID` (see [Phone app and Kodi](devices.md)) |
| Crash and error reports to GlitchTip or Sentry | `ERROR_REPORTS_SERVER_DSN`, `ERROR_REPORTS_WEB_DSN`, `ERROR_REPORTS_APP_DSN` (see below) |

After changing `.env`, run `docker compose up -d` to apply it.

### Error reports

Make three projects in GlitchTip or Sentry, one each for the server, the
website and the phone app, and put their DSNs in the three settings. Leave
out any you don't want. The website and the app send their reports to
your Movie Night, which checks them, limits how many come in and forwards
them. They never see a DSN, so nobody can read one from the code and fill
your collector.

A report has the error message, its stack trace, the browser or OS, the
app version and the page's path without its query string. It has no
names, emails, tokens or request bodies, and the collector never sees a
visitor's IP address. `ERROR_REPORTS_ENVIRONMENT` changes the environment
reports are filed under (`production` by default).

## Next steps

- [Phone app and Kodi](devices.md)
- [Backups and upgrades](operations.md)
- [Limits and schedules](limits.md): what Movie Night does by itself, and when
