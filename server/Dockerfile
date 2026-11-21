# The Go server's image: api, worker, migrate, admin and media from one build.
# Build from the repository root (it needs web/, the website it serves):
#   docker build -f server/Dockerfile -t movienight-go .
# Run: `api` by default; `migrate` as its own deploy step first; `worker`
# and `media` (the media libraries' scans and playback, next to the files)
# as their own services; `admin user add NAME` for accounts.
# The Jellyfin plugin (jellyfin-plugin/), served from /jellyfin/ for
# Jellyfin's plugin catalog.
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS jellyfin-plugin
WORKDIR /src
COPY jellyfin-plugin/Jellyfin.Plugin.MovieNight ./Jellyfin.Plugin.MovieNight
RUN dotnet publish Jellyfin.Plugin.MovieNight -c Release -o /out

FROM golang:1.27.3-trixie AS build
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/admin ./cmd/media

# Static binaries on Debian slim with ffmpeg and ffprobe, which the media
# command runs. The version is pinned (and the same in
# server/scripts/dev.Dockerfile, where the tests run): Debian replaces a
# package with its next security release, so a pin that stops resolving is
# moved to the version `apt-cache policy ffmpeg` shows, in both files.
# Runs as a non-root user (compose sets 1000:1000).
FROM debian:trixie-slim
ARG FFMPEG_VERSION=7:7.1.5-0+deb13u1
RUN apt-get update \
 && apt-get install -y --no-install-recommends "ffmpeg=${FFMPEG_VERSION}" ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /cache && chown 1000:1000 /cache
# /cache: the media service's cache folder in compose. A new named volume
# takes this folder's owner, so the service (uid 1000) can write it.
COPY --from=build /out/ /usr/local/bin/
COPY web /app/web
# The Kodi add-on and the Jellyfin plugin, served at /kodi/ and /jellyfin/.
COPY kodi/service.movienight /app/kodi/service.movienight
COPY jellyfin-plugin/meta.json jellyfin-plugin/movie-night.png /app/jellyfin-plugin/
COPY --from=jellyfin-plugin /out/Jellyfin.Plugin.MovieNight.dll /app/jellyfin-plugin/out/
# The image's own layout: the website's files and the add-ons are here.
ENV WEB_DIR=/app/web KODI_ADDON_DIR=/app/kodi/service.movienight JELLYFIN_PLUGIN_DIR=/app/jellyfin-plugin
EXPOSE 8000
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/api"]
