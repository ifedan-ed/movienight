# Movie Night for Jellyfin

A Jellyfin 12.1 plugin that sends every play to Movie Night as it happens:
start, progress, pause, stop, and the played and unplayed marks. Every app
that plays through Jellyfin raises these events. So Kodi with the Jellyfin
add-on, Swiftfin, Streamyfin and the web player all reach Movie Night's
history and its resume point.

Movie Night receives the events at `POST /api/v1/scrobble/jellyfin/events`.
The plugin sends the same `X-MovieNight-Token` secret as the Jellyfin
webhook, `JELLYFIN_WEBHOOK_SECRET`. While that secret is empty, Movie Night
answers 503 and nothing gets in.

## Plugin or Webhook?

The Webhook plugin only tells Movie Night that a play ended. Movie Night
then reads that person's plays from Jellyfin 5 seconds later. This plugin
sends each event with the title, so Movie Night also keeps where you
stopped and shows it on the title page. You can run either one or both. A
play is recorded once either way.

## Limits

- Progress is sent every Progress seconds per person and title. The default
  is 30, never more often than every 10 seconds, and the settings page
  allows up to 600. Start, pause, stop and the played and unplayed marks
  are always sent.
- Up to 1,000 events wait to be sent, in order. The setting is "Events kept
  while Movie Night doesn't answer". It takes any number from 1 and is read
  when Jellyfin starts. A lower number is saved as 1, and the page says so.
  Past the limit the oldest event is dropped, with a warning in Jellyfin's
  log each time. Plays still reach Movie Night through its own plays sync
  every 5 minutes.
- A failed send gets 4 attempts in all, 5, 20 and 80 seconds apart, each
  with a 20-second timeout. Then it is dropped with a warning in Jellyfin's
  log. A 4xx answer, such as a wrong token, is not retried.
- Movie Night counts a stop past 90% of the runtime as finished and clears
  the resume point. The rest are in
  [docs/limits.md](../docs/limits.md#jellyfin-plugin).

## Build

```sh
cd jellyfin-plugin
docker run --rm -v "$PWD":/src -w /src/Jellyfin.Plugin.MovieNight mcr.microsoft.com/dotnet/sdk:10.0 \
  dotnet publish -c Release -o /src/out
```

The server image builds it too (`server/Dockerfile`).

## Install

Movie Night serves its own plugin catalog:

1. In Jellyfin, Dashboard → Plugins → Repositories → add
   `https://movienight.example.com/jellyfin/manifest.json` and name it
   Movie Night.
2. Catalog → Movie Night → Install, then restart Jellyfin.
3. Dashboard → Plugins → Movie Night: enter the Movie Night address (for
   example `https://movienight.example.com`), the token, and how often to
   send progress.

The catalog (`server/internal/addons`) serves the plugin built from this
folder in Movie Night's image, its picture (`movie-night.png`) and the
details in `meta.json`. For a new version, raise the version in `meta.json`
and in the `.csproj`, then rebuild Movie Night's image. Jellyfin then offers
the update.

## Tests

The server's side, receiving and recording the plugin's events, is covered
by the Go tests (`server/internal/server/devicejobs_jellyfin_test.go`). The
plugin itself has no automated test.

## What Jellyfin decides

Two things are Jellyfin's, not the plugin's. Jellyfin estimates the
position between a player's reports. It also never resumes anything shorter
than its MinResumeDurationSeconds, 5 minutes by default, so stopping such a
video counts as played.
