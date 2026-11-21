package server_test

// The player's "Search online" (internal/subtitles) over the real API: a
// play of the fake Jellyfin's file, the fake subtitle sites
// (testenv/fakes/subtitles.go), the moviehash read through the stream
// proxy, the stored tracks shared by the household, their delay, removing
// them, and the limits.

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"movienight/server/internal/subtitles"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

type subtitleResult struct {
	Source    string `json:"source"`
	ID        string `json:"id"`
	Release   string `json:"release"`
	HI        bool   `json:"hearing_impaired"`
	Machine   bool   `json:"machine_translated"`
	HashMatch bool   `json:"hash_match"`
	Available bool   `json:"available"`
}

type subtitleSearch struct {
	Results []subtitleResult `json:"results"`
	Hidden  int              `json:"hidden"`
	Notes   []string         `json:"notes"`
}

type subtitleTrack struct {
	ID        int64   `json:"id"`
	Label     string  `json:"label"`
	Kind      string  `json:"kind"`
	URL       string  `json:"url"`
	DelayS    float64 `json:"delay_s"`
	CanRemove bool    `json:"can_remove"`
}

// clipFile is the fake Jellyfin's file for these tests: 300 KB of
// patterned bytes, enough for a moviehash.
func clipFile(t *testing.T) (dir string, data []byte) {
	t.Helper()
	data = make([]byte, 300_000)
	for i := range data {
		data[i] = byte(i * 31 % 253)
	}
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, data
}

func TestSearchOnlineFindsStoresAndSharesSubtitles(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	dir, data := clipFile(t)
	f.Jellyfin.MediaDir = dir
	s.SeedLibrary(t)
	alice, bob, owner := s.Client("alice", true), s.Client("bob", true), s.Client("owner", true)

	langs := decode[struct {
		Enabled bool   `json:"enabled"`
		Default string `json:"default"`
	}](t, want(t, alice.Do(t, "GET", "/api/v1/subtitles/languages", nil), 200))
	if !langs.Enabled || langs.Default != "en" {
		t.Fatalf("%+v", langs)
	}

	w := startWatch(t, alice, "")
	q := "?ticket=" + url.QueryEscape(w.Ticket)
	search := func(c *testenv.Client, ticket, extra string) subtitleSearch {
		t.Helper()
		return decode[subtitleSearch](t, want(t, c.Do(t, "GET",
			"/api/v1/title/movie/603/subtitles/search?ticket="+url.QueryEscape(ticket)+"&language=en"+extra, nil), 200))
	}
	found := search(alice, w.Ticket, "")
	if len(found.Results) != 2 || found.Hidden != 1 || len(found.Notes) != 0 {
		t.Fatalf("%+v", found)
	}
	first := found.Results[0]
	if first.Source != "opensubtitles" || !first.HashMatch || first.Release != fakes.SubsRelease || !first.Available {
		t.Fatalf("not the hash match first: %+v", found.Results)
	}
	if found.Results[1].Source != "podnapisi" || !found.Results[1].HI {
		t.Fatalf("%+v", found.Results[1])
	}
	// The moviehash went to OpenSubtitles, read through the stream proxy
	// with two Range reads, never the whole file.
	hash, _ := subtitles.MovieHash(context.Background(), func(_ context.Context, off int64, n int) ([]byte, int64, error) {
		return data[off : off+int64(n)], int64(len(data)), nil
	})
	if f.Subtitles.Count("GET /opensubtitles/api/v1/subtitles") != 1 || !strings.Contains(strings.Join(f.Subtitles.Calls(), " "), "moviehash="+hash) {
		t.Fatalf("hash %s not sent: %v", hash, f.Subtitles.Calls())
	}
	// A second search is the cached answer.
	_ = search(alice, w.Ticket, "")
	if n := f.Subtitles.Count("GET /opensubtitles/api/v1/subtitles"); n != 1 {
		t.Fatalf("asked OpenSubtitles %d times", n)
	}
	if m := search(alice, w.Ticket, "&machine=true"); len(m.Results) != 3 || m.Hidden != 0 {
		t.Fatalf("%+v", m)
	}

	add := func(c *testenv.Client, ticket, source, id string, status int) *testenv.Response {
		t.Helper()
		body := `{"ticket":"` + ticket + `","source":"` + source + `","id":"` + id + `"}`
		return want(t, c.Do(t, "POST", "/api/v1/title/movie/603/subtitles", body), status)
	}
	tr := decode[subtitleTrack](t, add(alice, w.Ticket, "opensubtitles", first.ID, 200))
	if tr.Kind != "text" || tr.Label != "English · OpenSubtitles" || !tr.CanRemove {
		t.Fatalf("%+v", tr)
	}
	file := want(t, alice.Do(t, "GET", tr.URL, nil), 200)
	if !strings.HasPrefix(file.Body, "WEBVTT") || !strings.Contains(file.Body, "00:00:01.000 --> 00:00:03.000\n"+fakes.OnlineLine) ||
		!strings.HasPrefix(file.Header.Get("Content-Type"), "text/vtt") {
		t.Fatalf("%q %v", file.Body, file.Header)
	}
	// The same pick again is the same track, not another download.
	if again := decode[subtitleTrack](t, add(alice, w.Ticket, "opensubtitles", first.ID, 200)); again.ID != tr.ID {
		t.Fatal(again.ID, tr.ID)
	}
	if n := f.Subtitles.Count("POST /opensubtitles/api/v1/download"); n != 1 {
		t.Fatalf("%d downloads", n)
	}
	// Podnapisi's is a zip in Windows-1252.
	pn := decode[subtitleTrack](t, add(alice, w.Ticket, "podnapisi", found.Results[1].ID, 200))
	if body := want(t, alice.Do(t, "GET", pn.URL, nil), 200).Body; !strings.Contains(body, fakes.PodnapisiLine) {
		t.Fatalf("%q", body)
	}
	add(alice, w.Ticket, "opensubtitles", "no-such-result", http.StatusGone)

	// Bob plays the same file: he sees both, can change the delay for
	// everyone, but can't remove Alice's.
	bw := startWatch(t, bob, "")
	add(bob, w.Ticket, "opensubtitles", first.ID, http.StatusForbidden) // Alice's ticket
	list := decode[struct {
		Tracks []subtitleTrack `json:"tracks"`
	}](t, want(t, bob.Do(t, "GET", "/api/v1/title/movie/603/subtitles?ticket="+url.QueryEscape(bw.Ticket), nil), 200))
	if len(list.Tracks) != 2 || list.Tracks[0].ID != tr.ID || list.Tracks[0].CanRemove {
		t.Fatalf("%+v", list)
	}
	want(t, bob.Do(t, "PUT", "/api/v1/subtitles/"+id64(tr.ID)+"/delay", `{"delay_s":1.5}`), 200)
	want(t, bob.Do(t, "PUT", "/api/v1/subtitles/"+id64(tr.ID)+"/delay", `{"delay_s":700}`), 422)
	want(t, bob.Do(t, "DELETE", "/api/v1/subtitles/"+id64(tr.ID), nil), http.StatusForbidden)
	list = decode[struct {
		Tracks []subtitleTrack `json:"tracks"`
	}](t, want(t, alice.Do(t, "GET", "/api/v1/title/movie/603/subtitles"+q, nil), 200))
	if list.Tracks[0].DelayS != 1.5 {
		t.Fatalf("delay %v", list.Tracks[0].DelayS)
	}
	// An admin may remove anyone's.
	want(t, owner.Do(t, "DELETE", "/api/v1/subtitles/"+id64(pn.ID), nil), 200)
	want(t, alice.Do(t, "GET", pn.URL, nil), 404)
	want(t, alice.Do(t, "DELETE", "/api/v1/subtitles/"+id64(tr.ID), nil), 200)

	// The download quota, said plainly: the download refused, and the
	// next search's OpenSubtitles results marked unavailable.
	f.Subtitles.Override("POST /opensubtitles/api/v1/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = w.Write([]byte(`{"requests":20,"remaining":0,"message":"used","reset_time_utc":"2099-01-01T12:00:00Z"}`))
	})
	quota := add(alice, w.Ticket, "opensubtitles", first.ID, http.StatusTooManyRequests)
	if !strings.Contains(quota.Body, "OpenSubtitles: today's downloads are used up. They come back at ") {
		t.Fatal(quota.Body)
	}
	after := search(alice, w.Ticket, "")
	if after.Results[0].Available || len(after.Notes) != 1 || !strings.Contains(after.Notes[0], "used up") {
		t.Fatalf("%+v", after)
	}
}

func TestSubtitleSearchesAreLimitedPerPerson(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	dir, _ := clipFile(t)
	f.Jellyfin.MediaDir = dir
	s.SeedLibrary(t)
	alice := s.Client("alice", true)
	w := startWatch(t, alice, "")
	path := "/api/v1/title/movie/603/subtitles/search?language=en&ticket=" + url.QueryEscape(w.Ticket)
	for i := 0; i < subtitles.SearchesPerHour; i++ {
		want(t, alice.Do(t, "GET", path, nil), 200)
	}
	if r := want(t, alice.Do(t, "GET", path, nil), 429); !strings.Contains(r.Body, "Try again in an hour") {
		t.Fatal(r.Body)
	}
	// Another title in the path than the play's is refused.
	want(t, alice.Do(t, "GET", "/api/v1/title/movie/604/subtitles/search?language=en&ticket="+url.QueryEscape(w.Ticket), nil), 422)
	want(t, alice.Do(t, "GET", "/api/v1/title/movie/603/subtitles?ticket=forged", nil), 403)
	want(t, s.Client("", true).Do(t, "GET", "/api/v1/subtitles/languages", nil), 401)
}

func id64(n int64) string { return strconv.FormatInt(n, 10) }
