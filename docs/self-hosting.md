# Self-hosting

This guide takes you from an empty server to Movie Night running at your own address, with accounts for your people. Plan on about 20 minutes.

## What you need

- A Linux server with **Docker** and the **Compose plugin** (`docker compose version`). 1 CPU and 2 GB of RAM are plenty for a household; Meilisearch likes a little more for a big library.
- A **domain name** pointing at the server, and a **reverse proxy** for TLS (Caddy, nginx, Traefik…).
- A free **TMDB API key**: <https://www.themoviedb.org/settings/api>.
- Optional: **Jellyfin** (library sync, plays, Play links), **Seerr** (requests), **MDBList** (lists and outside scores), an **OpenAI-compatible AI endpoint** (AI rows and search guesses) and **SMTP** (digest emails).

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
| `SECRET_KEY` | A long random string: `python3 -c "import secrets; print(secrets.token_urlsafe(48))"`. Keep it: it encrypts each person's Jellyfin token. |
| `POSTGRES_PASSWORD` | Another long random string. |
| `TMDB_API_KEY` | Your TMDB key. |
| `PUBLIC_URL` | Your address, e.g. `https://movies.example.com`. |
| `OWNER_USERNAME` | Your own username (you'll make it below). The owner is always an admin. |
| `TIMEZONE` | Your time zone, e.g. `Europe/London`. |
| `PASSWORD_SIGNIN` | `true` to sign in with a username and password (see [Sign-in](sign-in.md) for OIDC). |

Every other setting is in the [configuration reference](configuration.md).

## 2. Start it

The Compose file joins a Docker network for an optional AI container. Create it once, even if you don't use AI:

```sh
docker network create movienight-ai
docker compose up -d --build
curl -s http://127.0.0.1:8137/healthz     # {"status":"ok"}
```

The first build takes a few minutes (it also builds the Jellyfin plugin). Database migrations run on every start.

## 3. Put it behind your reverse proxy

Movie Night listens on `127.0.0.1:8137` only. With Caddy:

```caddy
movies.example.com {
    reverse_proxy 127.0.0.1:8137
}
```

With nginx, proxy `/` to `http://127.0.0.1:8137` and pass `Host` and `X-Forwarded-Proto`.

## 4. Make your accounts

```sh
docker compose exec web python -m app.cli user add yourname --name "Your Name"
docker compose exec web python -m app.cli user add alice --name "Alice" --email alice@example.com
```

Each command asks for the password twice. Open your address and sign in. People can change their own password under **Profile → Account**. More in [Sign-in](sign-in.md).

## 5. Connect Jellyfin (optional)

In `.env`, set `JELLYFIN_BASE_URL`, `JELLYFIN_PUBLIC_URL`, `JELLYFIN_API_KEY` (Jellyfin Dashboard → API Keys) and `JELLYFIN_USER_ID` (a user who sees every library), then `docker compose up -d`. The library syncs within a minute and then every night. Each person links their own Jellyfin account in **Profile → Jellyfin**. See [Jellyfin and players](jellyfin.md) for instant plays and the plugin.

## 6. Everything else (optional)

| Feature | Settings |
|---|---|
| Requests | `SEERR_BASE_URL`, `SEERR_API_KEY` |
| Lists and outside scores | `MDBLIST_API_KEY` |
| AI rows, digest wording, plot search guesses | `LITELLM_BASE_URL`, `LITELLM_API_KEY`, `LITELLM_MODEL`, `SEARCH_AI_MODEL` (any OpenAI-compatible endpoint) |
| Weekly digest and month-in-review emails | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM` |

After changing `.env`, run `docker compose up -d` to apply it.

## Next steps

- [Phone app and Kodi](devices.md)
- [Backups and upgrades](operations.md)
- [Limits and schedules](limits.md): what Movie Night does by itself, and when
