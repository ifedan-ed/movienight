# Jellyfin and players

Movie Night is a companion to one Jellyfin server: your library, plays and Play buttons come from it. The four `JELLYFIN_*` settings below are required.

## The library

Set these in `.env` and run `docker compose up -d`:

| Setting | |
|---|---|
| `JELLYFIN_BASE_URL` | Where Movie Night reaches Jellyfin (can be an internal address) |
| `JELLYFIN_PUBLIC_URL` | The address people's browsers and players use |
| `JELLYFIN_API_KEY` | Jellyfin Dashboard → API Keys |
| `JELLYFIN_USER_ID` | A Jellyfin user who sees every library |

The library syncs when the worker starts and then every 30 minutes; admins can also press Profile → Rescan Jellyfin library. A read that fails or comes back incomplete changes nothing, and admins see why at the top of Profile.

## Each person's plays

Everyone links their own Jellyfin account once in Profile → Jellyfin (Quick Connect or password; only an encrypted token is kept). From then on:

- their Jellyfin plays are added to their history every 5 minutes. A play marked played in Jellyfin without a date can't go on the timeline, so Profile lists it by name, with "Add to history" (on a day they pick) and "Not watched". Either answer clears it for good,
- ticking an episode in Movie Night marks it played in Jellyfin,
- Play opens the title in the player they choose.

### Instant plays (optional)

Set a shared secret, `JELLYFIN_WEBHOOK_SECRET`, then either:

- The Movie Night plugin for Jellyfin (plays and progress from any app that plays through Jellyfin): Dashboard → Plugins → Repositories → add `https://<your movie night>/jellyfin/manifest.json`, install Movie Night, restart Jellyfin, and enter your address and the secret in its settings.
- The Webhook plugin: add a Generic destination to `https://<your movie night>/api/v1/scrobble/jellyfin` with the header `X-MovieNight-Token: <secret>`, notification types *PlaybackStop* and *UserDataSaved*, and this template:

    ```json
    {"NotificationType":"{{NotificationType}}","UserId":"{{UserId}}","ItemId":"{{ItemId}}"}
    ```

## Play here

Play here plays the film or episode in the browser. The browser tells Movie Night which codecs it can decode, and Jellyfin sends the file as it is when the browser can play it. Otherwise Jellyfin remuxes or transcodes it to HLS. Another audio track, or a picture subtitle (PGS, VobSub), restarts the stream from the same second with that track. Text subtitles are shown by the player and never burnt in.

Every byte goes through Movie Night, so browsers never see a Jellyfin key. Movie Night fetches the media with `JELLYFIN_API_KEY`, limited to the one item a signed stream link names. The link lasts six hours.

The player tells Jellyfin where it is, as the person's own Jellyfin user when they connected their account. A stop past 90 % of the runtime, or into the credits, records the watch and marks it played.

Skip intro and Skip credits appear when Jellyfin has media segments for the item. A segment plugin such as Intro Skipper makes them.

### Subtitles online

The subtitle menu's Search online asks every subtitle site the install has set up at once. The language starts as the one picked in Profile → Players and subtitles, else English. The subtitle made for this very file comes first, marked Match: OpenSubtitles knows it by the file's hash. Then come the ones whose release name is closest to the file's name, then the most downloaded. Machine-translated ones are left out until asked for.

A picked subtitle shows at once while the film plays on. It stays as a track of that file for everyone in the household. Its delay, set with [ and ] or in the menu, is kept with it, so every device gets the same. The person who added it, or an admin, can remove it.

Podnapisi and Addic7ed (through Gestdown, TV only) need no key. OpenSubtitles, SubDL, SubSource and Jimaku (anime, in Japanese) are used once their keys are set ([configuration](configuration.md#subtitles-online)). These sites hold subtitles their users uploaded, and each has its own terms. OpenSubtitles limits downloads a day. When they are used up, the player says until when.

On a phone, a tap shows or hides the controls. The keys: Space or K plays and pauses, ← and → jump 10 seconds, ↑ and ↓ change the volume, F is fullscreen, M mutes, C changes the subtitles, [ and ] move the subtitles half a second earlier or later, and < and > change the speed.

## Players

Play can open VLC, Infuse, Outplayer, nPlayer, VidHub, SenPlayer, Swiftfin, Streamyfin or the Jellyfin website, with subtitles in the language each person picks (Profile → Players and subtitles). After a film or episode, Movie Night asks "Finished it?" and records the watch.

Any app that opens a video link can be added with its own link and these placeholders: `{url}` (URL-encoded), `{url_raw}`, `{url_noscheme}`, `{url_scheme}`, `{sub}`, `{sub_raw}`, `{title}`, `{filename}`, `{done}` (the link back to Movie Night), `{jellyfin_id}` and `{web}`. For example nPlayer: `nplayer-{url_raw}`.

## Requests (Seerr)

With `SEERR_BASE_URL` and `SEERR_API_KEY` set, title pages get a Request button. A person's first request creates their Seerr user from their linked Jellyfin account, and the request's progress shows on the title and under Profile → Your requests.
