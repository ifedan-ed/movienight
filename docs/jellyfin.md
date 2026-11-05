# Jellyfin and players

Jellyfin is optional. Without it, Movie Night is a watch diary you fill in by hand. With it, your library, plays and Play buttons come from your own server.

## The library

Set these in `.env` and run `docker compose up -d`:

| Setting | |
|---|---|
| `JELLYFIN_BASE_URL` | Where Movie Night reaches Jellyfin (can be an internal address) |
| `JELLYFIN_PUBLIC_URL` | The address people's browsers and players use |
| `JELLYFIN_API_KEY` | Jellyfin Dashboard → API Keys |
| `JELLYFIN_USER_ID` | A Jellyfin user who sees every library |

The library syncs 20 seconds after start and then every night; admins can also press **Profile → Rescan Jellyfin library**.

## Each person's plays

Everyone links their own Jellyfin account once in **Profile → Jellyfin** (Quick Connect or password; only an encrypted token is kept). From then on:

- their Jellyfin plays are added to their history every 5 minutes,
- ticking an episode in Movie Night marks it played in Jellyfin,
- **Play** opens the title in the player they choose.

### Instant plays (optional)

Set a shared secret, `JELLYFIN_WEBHOOK_SECRET`, then either:

- **The Movie Night plugin for Jellyfin** (plays *and* progress from any app that plays through Jellyfin): Dashboard → Plugins → Repositories → add `https://<your movie night>/jellyfin/manifest.json`, install **Movie Night**, restart Jellyfin, and enter your address and the secret in its settings.
- **The Webhook plugin**: add a Generic destination to `https://<your movie night>/api/v1/scrobble/jellyfin` with the header `X-MovieNight-Token: <secret>`, notification types *PlaybackStop* and *UserDataSaved*, and this template:

    ```json
    {"NotificationType":"{{NotificationType}}","UserId":"{{UserId}}","ItemId":"{{ItemId}}"}
    ```

## Players

**Play** can open VLC, Infuse, Outplayer, nPlayer, VidHub, SenPlayer, Swiftfin, Streamyfin or the Jellyfin website, with subtitles in the language each person picks (**Profile → Players and subtitles**). After a film or episode, Movie Night asks "Finished it?" and records the watch.

Any app that opens a video link can be added with its own link and these placeholders: `{url}` (URL-encoded), `{url_raw}`, `{url_noscheme}`, `{url_scheme}`, `{sub}`, `{sub_raw}`, `{title}`, `{filename}`, `{done}` (the link back to Movie Night), `{jellyfin_id}` and `{web}`. For example nPlayer: `nplayer-{url_raw}`.

## Requests (Seerr)

With `SEERR_BASE_URL` and `SEERR_API_KEY` set, title pages get a **Request** button. A person's first request creates their Seerr user from their linked Jellyfin account, and the request's progress shows on the title and under **Profile → Your requests**.
