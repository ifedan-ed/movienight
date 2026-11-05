# The Jellyfin plugin, built from jellyfin-plugin/ for Movie Night's plugin
# catalog at /jellyfin/ (app/web/jellyfin_catalog.py).
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS jellyfin-plugin
WORKDIR /src
COPY jellyfin-plugin/Jellyfin.Plugin.MovieNight ./Jellyfin.Plugin.MovieNight
RUN dotnet publish Jellyfin.Plugin.MovieNight -c Release -o /out

FROM python:3.12-slim

WORKDIR /app

RUN useradd -u 1000 -m appuser
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY alembic.ini ./
COPY migrations ./migrations
COPY app ./app
COPY web ./web
COPY kodi ./kodi
COPY jellyfin-plugin/meta.json jellyfin-plugin/movie-night.png ./jellyfin-plugin/
COPY --from=jellyfin-plugin /out/Jellyfin.Plugin.MovieNight.dll ./jellyfin-plugin/out/
COPY scripts ./scripts

RUN mkdir -p /data && chown -R appuser:appuser /app /data
USER appuser

EXPOSE 8000
# One process only (no --workers): the scheduler, the in-memory caches and the
# Kodi pairing limit all assume a single process (README, "Updating").
# --no-access-log: uvicorn's access line carries every query string (search
# text, tokens); the app writes its own, without them (app/core/logs.py).
# --no-proxy-headers: the sign-in headers are believed only from the proxy's
# own address (app/core/security.py, from_proxy), so the connection's address
# must stay the real one; uvicorn would otherwise swap in X-Forwarded-For,
# which any caller can write, for any peer listed in FORWARDED_ALLOW_IPS.
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--no-access-log", "--no-proxy-headers"]
