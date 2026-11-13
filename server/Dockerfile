# The Go server's image: api, worker, migrate and admin from one build.
# Build from the repository root (it needs web/, the website it serves):
#   docker build -f server/Dockerfile -t movienight-go .
# Run: `api` by default; `migrate` as its own deploy step first; `worker`
# as its own service; `admin user add NAME` for accounts.
# The Jellyfin plugin (jellyfin-plugin/), served from /jellyfin/ for
# Jellyfin's plugin catalog; the same build as the Python image's.
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS jellyfin-plugin
WORKDIR /src
COPY jellyfin-plugin/Jellyfin.Plugin.MovieNight ./Jellyfin.Plugin.MovieNight
RUN dotnet publish Jellyfin.Plugin.MovieNight -c Release -o /out

FROM golang:1.27.3-trixie AS build
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/admin

# Static binaries on distroless: no shell, no package manager, a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
COPY web /app/web
# The Kodi add-on and the Jellyfin plugin, served at /kodi/ and /jellyfin/.
COPY kodi/service.movienight /app/kodi/service.movienight
COPY jellyfin-plugin/meta.json jellyfin-plugin/movie-night.png /app/jellyfin-plugin/
COPY --from=jellyfin-plugin /out/Jellyfin.Plugin.MovieNight.dll /app/jellyfin-plugin/out/
# The image's own layout: the website's files and the add-ons are here.
ENV WEB_DIR=/app/web KODI_ADDON_DIR=/app/kodi/service.movienight JELLYFIN_PLUGIN_DIR=/app/jellyfin-plugin
EXPOSE 8000
USER nonroot
ENTRYPOINT ["/usr/local/bin/api"]
