# Self-hosting

This guide takes you from an empty server to Movie Night running at your own address, with accounts for your people. Plan on about 20 minutes.

## What you need

- A Linux server with Docker and the Compose plugin (`docker compose version`). 1 CPU and 2 GB of RAM are plenty for a household; Meilisearch likes a little more for a big library. The Movie Night server itself uses a few tens of megabytes.
- A domain name pointing at the server, and a reverse proxy for TLS (Caddy, nginx, Traefik...).
- A Jellyfin server. Movie Night is a companion to one Jellyfin server: it reads the library from it, and each person's plays.
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
| `JELLYFIN_BASE_URL` | Where Movie Night reaches Jellyfin (an internal address is fine). |
| `JELLYFIN_PUBLIC_URL` | The Jellyfin address people's browsers and players use. |
| `JELLYFIN_API_KEY` | Jellyfin Dashboard → API Keys. |
| `JELLYFIN_USER_ID` | The id of a Jellyfin user who sees every library. |
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
| `worker` | Runs the background jobs (Jellyfin syncs, IMDb ratings, the search catalog, digest emails) and the work the API hands it. |
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

## 5. Jellyfin

The library syncs as soon as the worker starts and then every 30 minutes. Each person links their own Jellyfin account in Profile → Jellyfin, and from then on their plays come into their history. See [Jellyfin and players](jellyfin.md) for instant plays and the plugin.

## 6. Everything else (optional)

| Feature | Settings |
|---|---|
| Requests | `SEERR_BASE_URL`, `SEERR_API_KEY` |
| Lists and outside scores | `MDBLIST_API_KEY` |
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
