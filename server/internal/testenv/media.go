package testenv

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"movienight/server/internal/media/engine"
	"movienight/server/internal/media/mediasrv"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/media/scan"
	"movienight/server/internal/testbase"
)

// MediaSecret is the test media service's shared secret.
const MediaSecret = "test-media-secret-0123"

// NoJellyfin takes Jellyfin's settings out of env: a fresh install with
// only Movie Night's own libraries.
func NoJellyfin(env testbase.Env) {
	for _, k := range []string{"JELLYFIN_BASE_URL", "JELLYFIN_PUBLIC_URL", "JELLYFIN_API_KEY", "JELLYFIN_USER_ID"} {
		delete(env, k)
	}
}

// StartMedia serves a real media service (cmd/media's handler, with real
// ffmpeg) reading folder, and points env's MEDIA_URL and MEDIA_SECRET at it.
func StartMedia(t testing.TB, env testbase.Env, folder string) *engine.Engine {
	t.Helper()
	// Quiet in tests; the browser tests' server (E2E_SERVE) shows the media
	// service's log, which is what explains a play that doesn't start.
	var out io.Writer = io.Discard
	if os.Getenv("E2E_SERVE") == "1" {
		out = os.Stderr
	}
	quiet := slog.New(slog.NewTextHandler(out, nil))
	prober := probe.Runner{}
	eng, err := engine.New(engine.Config{Prober: prober, CacheDir: t.TempDir(), MaxTranscodes: 2, HW: engine.HW{Kind: "none"}, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	srv := httptest.NewServer((&mediasrv.Server{Secret: MediaSecret, Allowed: []string{folder}, Engine: eng, Log: quiet,
		Scanner: &scan.Scanner{Prober: prober, Workers: 2, Allowed: []string{folder}}}).Handler())
	t.Cleanup(srv.Close)
	env["MEDIA_URL"], env["MEDIA_SECRET"] = srv.URL, MediaSecret
	return eng
}
