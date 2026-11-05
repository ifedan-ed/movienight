#!/usr/bin/env bash
# The plugin end to end: a throwaway Jellyfin 12.1 with the built plugin and two
# one-minute test videos (a film, TMDB 603, and an episode, TMDB 1399 S01E05),
# and a throwaway Movie Night (fake TMDB/Seerr, its own Postgres). driver.py
# plays through Jellyfin's own API and checks Movie Night's database after each
# step. Everything listens on this host only (Postgres and Jellyfin on
# 127.0.0.1, Movie Night on the Docker bridge address so Jellyfin can reach it)
# and is removed at the end.
#
#   jellyfin-plugin/e2e/run.sh        (build the plugin first: see ../README.md)
set -euo pipefail
cd "$(dirname "$0")/../.."
REPO="$PWD"; HERE="$REPO/jellyfin-plugin/e2e"
WORK="$(mktemp -d)"; mkdir -p "$WORK/config" "$WORK/cache" "$WORK/plugin" "$WORK/media/movies/Movie 603 (2000)" "$WORK/media/tv/Show 1399/Season 01"
export HOOK_SECRET=$(openssl rand -hex 24) JF_PASSWORD=$(openssl rand -hex 12) PG_CONTAINER=mn-plugtest-pg-$$
PGPW=$(openssl rand -hex 16)
BRIDGE=$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}')
export MN_URL="http://$BRIDGE:8141"
MNPID=""
cleanup() {
  [ -n "$MNPID" ] && kill "$MNPID" 2>/dev/null || true
  docker rm -f jf-plugtest "$PG_CONTAINER" >/dev/null 2>&1 || true
  docker run --rm -v "$WORK:/w" alpine rm -rf /w/config /w/cache >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

cp jellyfin-plugin/out/Jellyfin.Plugin.MovieNight.dll jellyfin-plugin/meta.json "$WORK/plugin/"
FF="docker run --rm -v $WORK/media:/m --entrypoint /usr/lib/jellyfin-ffmpeg/ffmpeg jellyfin/jellyfin:12.1 -loglevel error -f lavfi -i testsrc=duration=60:size=320x240:rate=10 -f lavfi -i sine=duration=60 -shortest"
$FF "/m/movies/Movie 603 (2000)/Movie 603 (2000).mkv"
$FF "/m/tv/Show 1399/Season 01/Show 1399 - S01E05 - Episode 5.mkv"
echo '<?xml version="1.0" encoding="utf-8"?><movie><title>Movie 603</title><year>2000</year><tmdbid>603</tmdbid><uniqueid type="tmdb" default="true">603</uniqueid></movie>' > "$WORK/media/movies/Movie 603 (2000)/movie.nfo"
echo '<?xml version="1.0" encoding="utf-8"?><tvshow><title>Show 1399</title><year>2011</year><tmdbid>1399</tmdbid><uniqueid type="tmdb" default="true">1399</uniqueid></tvshow>' > "$WORK/media/tv/Show 1399/tvshow.nfo"

docker run -d --rm --name "$PG_CONTAINER" -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_DB=movienight -p 127.0.0.1:55438:5432 pgvector/pgvector:pg17 >/dev/null
for _ in $(seq 1 90); do [ "$(docker logs "$PG_CONTAINER" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break; sleep 1; done
MN_HOST="$BRIDGE" TEST_DATABASE_URL="postgresql+psycopg://movienight:$PGPW@127.0.0.1:55438/movienight" .venv/bin/python "$HERE/server.py" > "$WORK/movienight.log" 2>&1 &
MNPID=$!
for _ in $(seq 1 60); do curl -sf "$MN_URL/healthz" >/dev/null && break; sleep 1; done
docker run -d --name jf-plugtest -p 127.0.0.1:18096:8096 -v "$WORK/config:/config" -v "$WORK/cache:/cache" -v "$WORK/media:/media:ro" \
  -v "$WORK/plugin:/config/plugins/Movie Night_1.0.0.0" jellyfin/jellyfin:12.1 >/dev/null
set +e
timeout 600 python3 "$HERE/driver.py"; RC=$?
set -e
[ "$RC" -ne 0 ] && { echo "== events Movie Night received:"; grep "PLUGIN EVENT" "$WORK/movienight.log" | cut -c1-200 | head -20; }
exit "$RC"
