#!/usr/bin/env bash
# Browser tests (WebKit = iPhone, Chromium = Android, Chromium desktop)
# against the Go server with fake upstreams: a throwaway Postgres and Valkey
# (as server/scripts/test.sh starts them, removed afterwards), and the app
# served by server/internal/server/e2e_serve_test.go (E2E_SERVE=1) inside the
# pinned Go image. Needs Docker, ffmpeg (the player's test clip is made here,
# in a temporary folder removed afterwards) and Playwright with its browsers.
#
#   e2e/run.sh                       ports 55435/55436 and 8139
#   E2E_PG_PORT=.. E2E_VALKEY_PORT=.. E2E_PORT=.. e2e/run.sh
set -euo pipefail
cd "$(dirname "$0")/.."
PG_PORT="${E2E_PG_PORT:-55435}"
KV_PORT="${E2E_VALKEY_PORT:-55436}"
export E2E_PORT="${E2E_PORT:-8139}"
PG="movienight-e2e-pg-$$"
KV="movienight-e2e-kv-$$"
APP="movienight-e2e-app-$$"
MEDIA="$(mktemp -d)"
cleanup() {
  # The server runs in its own named container: killing the docker client
  # alone would leave it serving.
  docker rm -f -v "$PG" "$KV" "$APP" >/dev/null 2>&1 || true
  rm -rf "$MEDIA"
}
trap cleanup EXIT

# The in-browser player's clip: 60 s of a test picture with two audio tracks
# (440 Hz English, 880 Hz German), the same as HLS with either track, two
# SRT subtitles turned into WebVTT as Jellyfin serves them, and an ASS one.
# A few hundred KB, never committed.
make_clip() {
  local d="$1"
  ffmpeg -v error -f lavfi -i testsrc2=size=320x180:rate=25:duration=60 \
    -f lavfi -i sine=frequency=440:duration=60 -f lavfi -i sine=frequency=880:duration=60 \
    -map 0:v -map 1:a -map 2:a -c:v libx264 -preset veryfast -profile:v main -pix_fmt yuv420p -g 50 \
    -c:a aac -b:a 48k -ac 2 -metadata:s:a:0 language=eng -metadata:s:a:1 language=ger \
    -b:v 150k -movflags +faststart "$d/clip.mp4"
  for a in 1 2; do
    mkdir -p "$d/hls-$a"
    ffmpeg -v error -i "$d/clip.mp4" -map 0:v -map "0:a:$((a - 1))" -c copy -f hls -hls_time 4 \
      -hls_playlist_type vod -hls_segment_filename "$d/hls-$a/%d.ts" "$d/hls-$a/main.m3u8"
  done
  printf '1\n00:00:01,000 --> 00:00:03,000\nHello there\n\n2\n00:00:04,000 --> 00:00:30,000\nA <i>long</i> line\n\n3\n00:00:31,000 --> 00:00:59,000\nLater line\n' > "$d/subs-3.srt"
  printf '1\n00:00:01,000 --> 00:00:59,000\nBonjour\n' > "$d/subs-4.srt"
  for n in 3 4; do ffmpeg -v error -i "$d/subs-$n.srt" "$d/subs-$n.vtt"; done
  cat > "$d/subs-5.ass" <<'ASS'
[Script Info]
ScriptType: v4.00+
PlayResX: 320
PlayResY: 180

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Liberation Sans,18,&H0000FFFF,&H000000FF,&H00000000,&H00000000,1,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:59.00,Default,,0,0,0,,{\an8}Styled line
ASS
}
make_clip "$MEDIA"

docker run -d --rm --name "$PG" --tmpfs /var/lib/postgresql/data:rw \
  -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD=test -e POSTGRES_DB=movienight \
  -p "127.0.0.1:${PG_PORT}:5432" pgvector/pgvector:pg17 >/dev/null
docker run -d --rm --name "$KV" --tmpfs /data:rw -p "127.0.0.1:${KV_PORT}:6379" \
  valkey/valkey:9.1.2-alpine valkey-server --save "" --appendonly no >/dev/null
for _ in $(seq 1 90); do
  [ "$(docker logs "$PG" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done

# The same image and mounts as server/scripts/go.sh.
docker run -d --rm --name "$APP" --network host \
  -v /root/go/pkg/mod:/go/pkg/mod -v /root/.cache/movienight-go-build:/root/.cache/go-build \
  -v "$(pwd)":/repo -v "$MEDIA":/media:ro -w /repo/server -e GOMAXPROCS=4 -e E2E_SERVE=1 -e E2E_PORT -e E2E_MEDIA=/media \
  -e TEST_DATABASE_URL="postgresql://movienight:test@127.0.0.1:${PG_PORT}/movienight" \
  -e TEST_VALKEY_URL="redis://127.0.0.1:${KV_PORT}/0" \
  golang:1.27.3-trixie go test -count=1 -run TestServeForBrowserTests -timeout 60m ./internal/server/ >/dev/null
# The first build can take a few minutes.
for _ in $(seq 1 600); do curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null && break; sleep 1; done
if ! curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null; then
  docker logs "$APP" 2>&1 | tail -30
  echo "the e2e server did not start"
  exit 1
fi

# Playwright from e2e/node_modules (cd e2e && npm install && npx playwright
# install webkit chromium), or a copy installed elsewhere: set PLAYWRIGHT_DIR
# to that node_modules directory. Nothing is guessed.
export NODE_PATH="${NODE_PATH:-${PLAYWRIGHT_DIR:-$(pwd)/e2e/node_modules}}"
node e2e/flows.mjs "http://127.0.0.1:${E2E_PORT}"
