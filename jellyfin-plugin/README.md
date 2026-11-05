# Movie Night for Jellyfin

A Jellyfin 12.1 plugin that sends every play to Movie Night as it happens: start,
progress (at most every *Progress seconds*), pause, stop, and the played and
unplayed marks. Every app that plays through Jellyfin raises these, so Kodi
(with the Jellyfin add-on), Swiftfin, Streamyfin and the web player all reach
Movie Night's history and its "resume" line without Trakt.

Movie Night receives them at `POST /api/v1/scrobble/jellyfin/events`, with the
same `X-MovieNight-Token` secret as the Jellyfin webhook (`JELLYFIN_WEBHOOK_SECRET`).
With that secret empty, Movie Night answers 503 and nothing gets in.

**Plugin or Webhook?** The Webhook plugin only tells Movie Night that a play
ended, and Movie Night then reads that person's plays from Jellyfin (5 s
later). This plugin sends each event with the title, so Movie Night also
keeps where you stopped ("resume" on the title page). Either one, or both,
can run; a play is recorded once either way.

## Limits

- **Progress** is sent every *Progress seconds* per person and title: 30 by
  default, never more often than every 10 s, and the settings page allows up
  to 600. Start, pause, stop and the played/unplayed marks are always sent.
- **Queue:** up to 1,000 events wait to be sent, in order (the setting
  *Events kept while Movie Night doesn't answer*, any number from 1, read
  when Jellyfin starts; a lower number is saved as 1 and the page says so);
  past that the oldest is dropped, and Jellyfin's log says so each time (a
  warning, never silent). Plays still reach Movie Night through its own
  plays sync every 5 minutes.
- **A failed send** gets 4 attempts in all, 5, 20 and 80 seconds apart, each
  with a 20 s timeout, and is then dropped (a warning in Jellyfin's log). An
  answer in the 4xx range (a wrong token, say) is not retried.
- On Movie Night's side a stop past 90% of the runtime counts as finished
  and clears the resume point. The rest are in Movie Night's
  [docs/limits.md](../docs/limits.md#jellyfin-plugin).

## Build

```sh
cd jellyfin-plugin
docker run --rm -v "$PWD":/src -w /src/Jellyfin.Plugin.MovieNight mcr.microsoft.com/dotnet/sdk:10.0 \
  dotnet publish -c Release -o /src/out
```

## Install

From Movie Night's own catalog, like any other plugin:

1. Dashboard → Plugins → Repositories → add `https://movienight.example.com/jellyfin/manifest.json`
   (name it Movie Night).
2. Catalog → Movie Night → Install, then restart Jellyfin.
3. Dashboard → Plugins → Movie Night: the Movie Night address (for example
   `https://movienight.example.com`), the token, and how often to send progress.

The catalog (`app/web/jellyfin_catalog.py`) serves the plugin built from this
folder in Movie Night's image, its picture (`movie-night.png`) and the details in
`meta.json`. A new version: raise the version in `meta.json` and in the `.csproj`
(Jellyfin offers the update), then rebuild Movie Night's image.

## Test

`jellyfin-plugin/e2e/run.sh` (after building) starts a throwaway Jellyfin 12.1
with the plugin and two one-minute test videos, and a throwaway Movie Night with
its own Postgres, plays through Jellyfin's own API and checks Movie Night's
database after each step: progress kept, pause, a finished film recorded once
and its progress cleared, an episode stopped halfway kept as progress and not a
watch, played and unplayed marks. Everything listens on this host only and is
removed at the end.

Two things Jellyfin decides, not the plugin: it estimates the position between
a player's reports, and it never resumes anything shorter than its
*MinResumeDurationSeconds* (5 minutes by default) — stopping one counts as
played. The test lowers that setting on its own Jellyfin so a stop halfway can
be tested with one-minute videos.
