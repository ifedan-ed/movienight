package server_test

import (
	"net/http"
	"os"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

// TestServeForBrowserTests serves the app with the fake upstreams on
// E2E_PORT until killed, for the Playwright flows (e2e/flows.mjs). Only
// with E2E_SERVE=1: it never ends on its own, so it is not part of a test
// run (it is not skipped either: without the variable it does nothing and
// says so).
func TestServeForBrowserTests(t *testing.T) {
	if os.Getenv("E2E_SERVE") != "1" {
		t.Log("E2E_SERVE is not 1: not serving")
		return
	}
	env := testbase.BaseEnv(t)
	env["PUBLIC_URL"] = "http://127.0.0.1:" + os.Getenv("E2E_PORT")
	s, f := testenv.StartFaked(t, env)
	f.Jellyfin.QCReady = true
	// The in-browser player's clip, made by e2e/run.sh with ffmpeg.
	f.Jellyfin.MediaDir = os.Getenv("E2E_MEDIA")
	s.SeedLibrary(t)
	t.Log("serving on", env["PUBLIC_URL"])
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("E2E_PORT"), s.App.Handler); err != nil {
		t.Fatal(err)
	}
}
