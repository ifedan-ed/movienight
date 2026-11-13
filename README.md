<p align="center"><img src="docs/images/icon.png" width="96" alt=""></p>

<h1 align="center">Movie Night</h1>

<p align="center">
A self-hosted film and TV tracker for you and the people on your Jellyfin server.<br>
History, ratings, lists, requests and what to watch next, on the web, iPhone, Android, Kodi and Jellyfin.
</p>

<p align="center">
<a href="https://ifedan-ed.github.io/movienight/">Documentation</a> ·
<a href="https://ifedan-ed.github.io/movienight/self-hosting/">Self-hosting guide</a> ·
<a href="docs/configuration.md">Configuration</a>
</p>

<p align="center">
<img src="docs/images/phone-01-home.jpg" width="24%" alt="Home">
<img src="docs/images/phone-02-discover.jpg" width="24%" alt="Discover">
<img src="docs/images/phone-04-search.jpg" width="24%" alt="Search">
</p>
<p align="center"><sub>The screenshots use a made-up demo catalog. Every title and picture in them is invented.</sub></p>

## What it does

- A watch diary for each person: films and episodes watched, ratings from 1 to 10 with a short comment, a watchlist, favourites, liked and rated episodes, and their own lists.
- Shows ticked off season by season, with what's up next in everything you follow.
- Home rows like a media center's: Up next, new on the server, your watchlist, trending, coming soon, "Because you watched", and AI-picked rows if you set up a model.
- Search by title, person or a described plot ("the one where a robot sings to sleeping passengers"). A model can add its best guess.
- Jellyfin: the library syncs, plays come into each person's history, and ticks are sent back.
- Play in VLC, Infuse, Outplayer, Swiftfin, Streamyfin or a player you add yourself, with subtitles. Afterwards Movie Night asks "Finished it?" and records the watch.
- Requests through Seerr, followed until the title is available.
- A website you can install as an app, an iPhone and Android app, a Kodi add-on and a Jellyfin plugin, all talking to your server.
- Sign-in with a username and password, any OIDC provider (Authentik, Keycloak, Pocket ID and others), or both. The phone app offers the same, with password autofill on iPhone. Sessions live on the server, so signing out ends them.
- Each person's own time zone, reported by their browser or phone or picked in Profile. Their days, "this year", Home's rows and their digest emails follow it.
- No ads, analytics or tracking. The data stays on your server, and everyone can download theirs.

Movie Night is a watch diary, not a media source. It does not host, stream, download or index films or TV. It keeps what your people enter, reads titles and artwork from [TMDB](https://www.themoviedb.org/), and reads play state from a Jellyfin server you control.

## Quick start

You need Docker with Compose, a Jellyfin server, a free [TMDB API key](https://www.themoviedb.org/settings/api), and a domain name with a TLS reverse proxy.

```sh
git clone https://github.com/ifedan-ed/movienight.git
cd movienight
cp .env.example .env && chmod 600 .env
# Fill in .env. The self-hosting guide lists what to set.
docker network create movienight-ai     # once; for an optional AI container
docker compose up -d --build
docker compose exec api admin user add alice --name "Alice"
```

Then point your reverse proxy at `127.0.0.1:8137` and open your address. The [self-hosting guide](https://ifedan-ed.github.io/movienight/self-hosting/) covers each step: the settings, the reverse proxy, sign-in, Jellyfin, the phone app, Kodi, backups and upgrades.

Coming from the Python version? Read [Upgrading from the Python version](https://ifedan-ed.github.io/movienight/operations/#upgrading-from-the-python-version) first. There is one new setting and one new container, and everyone signs in once more.

## How it fits together

<p align="center"><img src="docs/images/architecture.svg" width="100%" alt="Your people's devices reach Movie Night through your reverse proxy; Movie Night's api and worker use Postgres, Meilisearch and Valkey, TMDB and your Jellyfin, and optionally Seerr, MDBList, an AI endpoint and SMTP."></p>

The server is written in Go. One image holds four commands:

- `api` serves the website, the JSON API and the add-on downloads.
- `worker` runs the scheduled jobs (Jellyfin syncs, IMDb ratings, the search catalog, digest emails) and the work the API queues.
- `migrate` brings the database up to date before either starts.
- `admin` manages accounts.

Postgres holds everyone's data and the sign-in sessions. Meilisearch holds the search indexes. Valkey holds the shared caches and queues.

| Folder | What's in it |
|---|---|
| `server/` | The server: commands in `cmd/`, packages in `internal/`, SQL and migrations in `db/`. See [server/README.md](server/README.md) |
| `web/` | The website: plain JavaScript, no build step |
| `mobile/` | The iPhone and Android app (Expo, React Native). See [mobile/README.md](mobile/README.md) |
| `kodi/` | The Kodi add-on |
| `jellyfin-plugin/` | The Jellyfin plugin (C#), built into the server image and served from `/jellyfin/` |
| `docs/` | [Configuration](docs/configuration.md), [limits and schedules](docs/limits.md), the API description (`openapi.json`) and the documentation site |
| `e2e/` | Browser flows (Playwright) against the real server |

## Development

Every tool runs in Docker. Nothing else needs installing on the host.

```sh
server/scripts/go.sh build ./...
server/scripts/test.sh     # starts a throwaway Postgres and Valkey and runs every Go test
```

The tests are behaviour tests over real HTTP against a real database. TMDB, Jellyfin, Seerr, Meilisearch, MDBList and the AI are fakes. The website is plain JavaScript in `web/static/`: edit and reload. For the phone app, run `cd mobile && npm install && npx expo start`.

After an API change, regenerate the API description that the phone app's types come from:

```sh
docker build -f server/Dockerfile -t movienight-server .
docker run --rm --entrypoint /usr/local/bin/api movienight-server -openapi > docs/openapi.json
cd mobile && npm run types
```

## API

The website and the apps use `/api/v1` and nothing else. The description is at `/api/v1/openapi.json` and interactive docs are at `/api/v1/docs`. Both need a signed-in person.

Every write must carry the header `X-Requested-With: movienight`. The scrobble endpoints are the exception, because players call them with a token.

## License

[AGPL-3.0](LICENSE). Title data and images come from TMDB. This product uses the TMDB API but is not endorsed or certified by TMDB.
