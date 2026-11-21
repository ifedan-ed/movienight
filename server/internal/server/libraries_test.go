package server_test

// Movie Night's own media libraries on a fresh install with no Jellyfin:
// adding a library, scanning real files (made by ffmpeg) through a real
// media service, the titles in the library, and playing them direct,
// remuxed and transcoded through the stream proxy, with the watch
// recorded at the end.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"movienight/server/internal/media/mediatest"
	"movienight/server/internal/subtitles"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

// chromeCaps: no HEVC, no AC-3 (desktop Chrome on Linux).
const chromeCaps = `"caps":{"direct_containers":["mp4","webm"],"direct_video":["h264","vp9"],"direct_audio":["aac","mp3","opus"],"hls_video":["h264"],"hls_audio":["aac","mp3"]}`

type libFixture struct {
	s      *testenv.Server
	admin  *testenv.Client
	folder string
	films  string
	shows  string
	subs   *fakes.Subtitles
	fakes  *testenv.Fakes
}

// startLibraries makes three films and a show episode, a fresh install
// without Jellyfin, the media service on the folder, and the two
// libraries scanned.
func startLibraries(t *testing.T) *libFixture { t.Helper(); return startLibrariesWith(t, false) }

// startLibrariesWith keeps Jellyfin (the fake) set up when withJellyfin.
func startLibrariesWith(t *testing.T, withJellyfin bool) *libFixture {
	t.Helper()
	mediatest.FFmpeg(t)
	folder := t.TempDir()
	films, shows := filepath.Join(folder, "Films"), filepath.Join(folder, "Shows")
	mediatest.H264MP4(t, filepath.Join(films, "Plain Film (2001) [tmdbid-603]", "Plain Film (2001).mp4"))
	mediatest.H264MKV(t, filepath.Join(films, "Box Film (2002) [tmdbid-604]", "Box Film (2002).mkv"))
	hevc := filepath.Join(films, "Wide Film (2003) [tmdbid-605]", "Wide Film (2003).mkv")
	mediatest.HEVCWithTracks(t, hevc)
	mediatest.Sidecar(t, hevc, "de")
	mediatest.H264MKV(t, filepath.Join(shows, "Some Show (2011) [tmdbid-1399]", "Season 01", "Some Show - S01E01.mkv"))
	mediatest.H264MKV(t, filepath.Join(shows, "Some Show (2011) [tmdbid-1399]", "Season 01", "Some Show - S01E02.mkv"))
	// A film whose name matches nothing (the fake TMDB finds every search,
	// so this one is a file ffprobe can't read instead).
	if err := os.WriteFile(filepath.Join(films, "Broken.mkv"), []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := testbase.BaseEnv(t)
	if !withJellyfin {
		testenv.NoJellyfin(env)
	} else {
		env["DRY_RUN"] = "false" // Jellyfin's played marks are sent
	}
	testenv.StartMedia(t, env, folder)
	s, fk := testenv.StartFaked(t, env)
	admin := s.Client("owner", true)
	f := &libFixture{s: s, admin: admin, folder: folder, films: films, shows: shows, subs: fk.Subtitles, fakes: fk}
	for _, body := range []string{`{"name":"Films","kind":"movies","paths":["` + films + `"]}`,
		`{"name":"Shows","kind":"shows","paths":["` + shows + `"]}`} {
		want(t, admin.Do(t, "POST", "/api/v1/libraries", body), 201)
	}
	f.scan(t)
	return f
}

// scan runs both libraries' scans as the worker does.
func (f *libFixture) scan(t *testing.T) {
	t.Helper()
	for _, id := range []int32{1, 2} {
		if _, err := f.s.App.RW.Libraries.ScanLocked(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLibrariesScanAndMarkTitlesInTheLibrary(t *testing.T) {
	f := startLibraries(t)
	type lib struct {
		Name     string  `json:"name"`
		Files    int     `json:"files"`
		Titles   int     `json:"titles"`
		Problems int     `json:"problems"`
		Scanned  *string `json:"scanned_at"`
	}
	got := decode[struct {
		Enabled   bool     `json:"enabled"`
		Libraries []lib    `json:"libraries"`
		Folders   []string `json:"folders"`
	}](t, want(t, f.admin.Do(t, "GET", "/api/v1/libraries", nil), 200))
	if !got.Enabled || len(got.Libraries) != 2 || len(got.Folders) != 1 || got.Folders[0] != f.folder {
		t.Fatalf("%+v", got)
	}
	films, shows := got.Libraries[0], got.Libraries[1]
	if films.Files != 4 || films.Titles != 3 || films.Problems != 1 || films.Scanned == nil {
		t.Fatalf("films %+v", films)
	}
	if shows.Files != 2 || shows.Titles != 1 || shows.Problems != 0 {
		t.Fatalf("shows %+v", shows)
	}
	probs := decode[struct {
		Items []struct {
			FileID   int64  `json:"file_id"`
			Path     string `json:"path"`
			Readable bool   `json:"readable"`
		} `json:"items"`
	}](t, want(t, f.admin.Do(t, "GET", "/api/v1/libraries/1/problems", nil), 200))
	if len(probs.Items) != 1 || probs.Items[0].Path != "Broken.mkv" || probs.Items[0].Readable {
		t.Fatalf("problems %+v", probs)
	}

	// Members don't manage libraries.
	want(t, f.s.Client("alice", true).Do(t, "GET", "/api/v1/libraries", nil), 403)

	// The titles are in the library on cards and title pages, with no Jellyfin.
	st := f.admin.Do(t, "GET", "/api/v1/title/movie/605/status", nil).Map(t)
	if st["in_library"] != true || st["jellyfin_url"] != nil {
		t.Fatalf("status %v", st)
	}
	// Home's library row ("new in library").
	row := want(t, f.admin.Do(t, "GET", "/api/v1/rows/recent", nil), 200).Body
	for _, title := range []string{"Movie 603", "Movie 604", "Movie 605", "Show 1399"} {
		if !strings.Contains(row, title) {
			t.Errorf("%s missing from the library row: %s", title, row)
		}
	}
	// The season page marks the episodes the library holds.
	season := want(t, f.admin.Do(t, "GET", "/api/v1/title/tv/1399/season/1", nil), 200).Map(t)
	eps := season["episodes"].([]any)
	if eps[0].(map[string]any)["in_library"] != true || eps[1].(map[string]any)["in_library"] != true ||
		eps[2].(map[string]any)["in_library"] == true {
		t.Fatalf("episodes %v", eps[:3])
	}
	// /me says what this install has.
	feats := f.admin.Do(t, "GET", "/api/v1/me", nil).Map(t)["features"].(map[string]any)
	if feats["jellyfin"] != false || feats["libraries"] != true {
		t.Fatalf("features %v", feats)
	}
}

func TestLibraryRescanIsIncrementalAndRemovesDeletedFiles(t *testing.T) {
	f := startLibraries(t)
	// Nothing changed: nothing probed.
	res, err := f.s.App.RW.Libraries.ScanLocked(context.Background(), 1)
	if err != nil || res.Probed != 0 || res.Removed != 0 {
		t.Fatalf("an unchanged library: %+v %v", res, err)
	}
	if err := os.RemoveAll(filepath.Join(f.films, "Box Film (2002) [tmdbid-604]")); err != nil {
		t.Fatal(err)
	}
	res, err = f.s.App.RW.Libraries.ScanLocked(context.Background(), 1)
	if err != nil || res.Removed != 1 {
		t.Fatalf("after a delete: %+v %v", res, err)
	}
	if st := f.admin.Do(t, "GET", "/api/v1/title/movie/604/status", nil).Map(t); st["in_library"] == true {
		t.Fatal("a deleted film is still in the library")
	}
}

func TestLibraryMatchByHand(t *testing.T) {
	f := startLibraries(t)
	// A show whose name matched nothing: here, an episode put in a folder
	// with no title at all is matched by hand to the show.
	mediatest.H264MKV(t, filepath.Join(f.shows, "Mystery", "Mystery.S01E01.mkv"))
	f.scan(t)
	var id int64
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT id FROM media_files WHERE path LIKE '%Mystery.S01E01.mkv'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got := decode[struct{ Files int }](t, want(t, f.admin.Do(t, "PUT", "/api/v1/libraries/files/"+strconv.FormatInt(id, 10)+"/match", `{"tmdb_id":1400}`), 200))
	if got.Files != 1 {
		t.Fatalf("matched %d files", got.Files)
	}
	// A new episode of that show joins the hand match on the next scan.
	mediatest.H264MKV(t, filepath.Join(f.shows, "Mystery", "Mystery.S01E02.mkv"))
	f.scan(t)
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM media_files WHERE tmdb_id = 1400 AND matched_by = 'hand'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("hand-matched files: %d %v", n, err)
	}
	// TMDB's unknown ids are refused.
	want(t, f.admin.Do(t, "PUT", "/api/v1/libraries/files/"+strconv.FormatInt(id, 10)+"/match", `{"tmdb_id":9000001}`), 422)
}

// get fetches a proxy address, failing unless it answers 200 or 206.
func get(t *testing.T, c *testenv.Client, path string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", c.Base+path, nil)
	for k, v := range c.Headers {
		req.Header[k] = v
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
	}
	return resp, body
}

type libWatch struct {
	Ticket   string  `json:"ticket"`
	PlayID   string  `json:"play_id"`
	Mode     string  `json:"mode"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration_s"`
	Resume   float64 `json:"resume_s"`
	Title    string  `json:"title"`
	Source   string  `json:"source"`
	AudioIdx *int    `json:"audio_index"`
	Audio    []struct {
		Index    int    `json:"index"`
		Label    string `json:"label"`
		Channels int    `json:"channels"`
	} `json:"audio"`
	Subtitles []struct {
		Index    int     `json:"index"`
		Kind     string  `json:"kind"`
		External bool    `json:"external"`
		URL      *string `json:"url"`
	} `json:"subtitles"`
	Chapters []struct{ Name string } `json:"chapters"`
	Segments []struct {
		Kind  string  `json:"kind"`
		Start float64 `json:"start_s"`
	} `json:"segments"`
	Fonts []string `json:"fonts"`
	Next  *struct {
		Season, Episode int
	} `json:"next"`
}

func watchLib(t *testing.T, c *testenv.Client, path, extra string) libWatch {
	t.Helper()
	return decode[libWatch](t, want(t, c.Do(t, "POST", path, "{"+chromeCaps+extra+"}"), 200))
}

func TestLibraryPlaysDirectRemuxAndTranscode(t *testing.T) {
	f := startLibraries(t)
	alice := f.s.Client("alice", true)

	// An H.264/AAC MP4 plays as it is, with Range.
	w := watchLib(t, alice, "/api/v1/title/movie/603/watch", "")
	if w.Mode != "direct" || w.Source != "library" || !strings.HasSuffix(w.URL, "/stream.mp4") || w.Duration < 19 {
		t.Fatalf("direct %+v", w)
	}
	req, _ := http.NewRequest("GET", alice.Base+w.URL, nil)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := alice.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || len(body) != 100 || resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("range: %d %d %q", resp.StatusCode, len(body), resp.Header.Get("Content-Type"))
	}

	// H.264 in Matroska: a remux to HLS.
	w = watchLib(t, alice, "/api/v1/title/movie/604/watch", "")
	if w.Mode != "hls" || !strings.HasSuffix(w.URL, "/main.m3u8") {
		t.Fatalf("remux %+v", w)
	}
	_, list := get(t, alice, w.URL)
	if !strings.Contains(string(list), "hls/3.m4s") {
		t.Fatalf("playlist %s", list)
	}
	base := strings.TrimSuffix(w.URL, "main.m3u8")
	get(t, alice, base+"hls/init.mp4")
	_, seg := get(t, alice, base+"hls/0.m4s")
	if len(seg) < 1000 {
		t.Fatalf("segment of %d bytes", len(seg))
	}

	// HEVC with two audio tracks: transcoded for Chrome; chapters named
	// Intro and Credits become Skip segments; subtitles embedded and
	// external; the attached font.
	w = watchLib(t, alice, "/api/v1/title/movie/605/watch", "")
	if w.Mode != "hls" || len(w.Audio) != 2 || w.Audio[1].Label != "German · Surround · 5.1" || *w.AudioIdx != mediatest.HEVCAudioEN {
		t.Fatalf("transcode %+v", w)
	}
	if len(w.Chapters) != 3 || len(w.Segments) != 2 || w.Segments[0].Kind != "intro" || w.Segments[1].Kind != "credits" ||
		w.Segments[1].Start != 16 || len(w.Fonts) != 1 {
		t.Fatalf("chapters %+v segments %+v fonts %v", w.Chapters, w.Segments, w.Fonts)
	}
	if len(w.Subtitles) != 3 || w.Subtitles[0].Kind != "text" || w.Subtitles[1].Kind != "ass" || !w.Subtitles[2].External {
		t.Fatalf("subtitles %+v", w.Subtitles)
	}
	base = strings.TrimSuffix(w.URL, "main.m3u8")
	get(t, alice, base+"hls/init.mp4")
	get(t, alice, base+"hls/1.m4s")
	for _, sub := range w.Subtitles {
		_, b := get(t, alice, *sub.URL)
		if !strings.Contains(string(b), "line") {
			t.Fatalf("subtitle %d: %s", sub.Index, b)
		}
	}
	if _, b := get(t, alice, w.Fonts[0]); string(b) != "not really a font, but bytes all the same" {
		t.Fatalf("font %q", b)
	}

	// The German track: the same play on a fresh ticket.
	w2 := watchLib(t, alice, "/api/v1/title/movie/605/watch", `,"audio":2,"replace":"`+w.Ticket+`"`)
	if w2.PlayID != w.PlayID || w2.Ticket == w.Ticket || *w2.AudioIdx != 2 {
		t.Fatalf("audio switch %+v", w2)
	}
	get(t, alice, strings.TrimSuffix(w2.URL, "main.m3u8")+"hls/0.m4s")

	// Stopped half way: resumed there next time; stopped in the credits:
	// watched, and the resume point is gone.
	if reportWatch(t, alice, w2.Ticket, "stop", 8) {
		t.Fatal("a stop half way recorded a watch")
	}
	w3 := watchLib(t, alice, "/api/v1/title/movie/605/watch", "")
	if w3.Resume != 8 {
		t.Fatalf("resume at %v", w3.Resume)
	}
	reportWatch(t, alice, w3.Ticket, "start", 8)
	if !reportWatch(t, alice, w3.Ticket, "stop", 17) {
		t.Fatal("a stop in the credits recorded no watch")
	}
	if st := alice.Do(t, "GET", "/api/v1/title/movie/605/status", nil).Map(t); st["watched"] == nil || st["watched"].(float64) != 1 {
		t.Fatalf("status after the watch %v", st)
	}

	// An episode plays with the next one named.
	ep := watchLib(t, alice, "/api/v1/title/tv/1399/watch", `,"season":1,"episode":1`)
	if ep.Next == nil || ep.Next.Episode != 2 || !strings.Contains(ep.Title, "S01E01") {
		t.Fatalf("episode %+v", ep)
	}
	// The Play sheet sends a library-only title straight to Play here.
	info := alice.Do(t, "GET", "/api/v1/title/movie/603/play", nil).Map(t)
	if info["available"] != true || info["in_library"] != true || info["in_jellyfin"] != false {
		t.Fatalf("play info %v", info)
	}
}

func TestLibraryTicketReachesOnlyItsOwnFile(t *testing.T) {
	f := startLibraries(t)
	alice := f.s.Client("alice", true)
	w := watchLib(t, alice, "/api/v1/title/movie/605/watch", "")
	base := strings.TrimSuffix(w.URL, "main.m3u8")
	// An external subtitle name that is not one of the file's own.
	r := alice.Do(t, "GET", base+"subtitles/9.vtt?name=../../etc/passwd", nil)
	if r.StatusCode != 404 {
		t.Fatalf("a foreign name: %d %s", r.StatusCode, r.Body)
	}
	// A path the proxy doesn't serve.
	if r := alice.Do(t, "GET", base+"../stream.mkv", nil); r.StatusCode < 400 {
		t.Fatalf("an outside path: %d", r.StatusCode)
	}
	// A forged ticket.
	if r := alice.Do(t, "GET", "/api/v1/stream/abc.def/stream.mp4", nil); r.StatusCode != 403 {
		t.Fatalf("a forged ticket: %d", r.StatusCode)
	}
}

// Search online for a library play: the file's moviehash is read from the
// media service (two Range reads) and its name from the ticket, and a
// picked subtitle is stored for that library file alone.
func TestLibrarySearchOnline(t *testing.T) {
	f := startLibraries(t)
	alice := f.s.Client("alice", true)
	w := watchLib(t, alice, "/api/v1/title/movie/605/watch", "")
	found := decode[subtitleSearch](t, want(t, alice.Do(t, "GET",
		"/api/v1/title/movie/605/subtitles/search?ticket="+url.QueryEscape(w.Ticket)+"&language=en", nil), 200))
	if len(found.Results) == 0 || !found.Results[0].HashMatch {
		t.Fatalf("no hash match: %+v", found)
	}
	data, err := os.ReadFile(filepath.Join(f.films, "Wide Film (2003) [tmdbid-605]", "Wide Film (2003).mkv"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := subtitles.MovieHash(context.Background(), func(_ context.Context, off int64, n int) ([]byte, int64, error) {
		return data[off : off+int64(n)], int64(len(data)), nil
	})
	calls := strings.Join(f.subs.Calls(), " ")
	if !strings.Contains(calls, "moviehash="+hash) {
		t.Fatalf("hash %s not sent: %v", hash, calls)
	}
	body := `{"ticket":"` + w.Ticket + `","source":"` + found.Results[0].Source + `","id":"` + found.Results[0].ID + `"}`
	tr := decode[subtitleTrack](t, want(t, alice.Do(t, "POST", "/api/v1/title/movie/605/subtitles", body), 200))
	if file := want(t, alice.Do(t, "GET", tr.URL, nil), 200); !strings.HasPrefix(file.Body, "WEBVTT") {
		t.Fatalf("%q", file.Body)
	}
	var source string
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT media_source_id FROM online_subtitles WHERE id = $1`, tr.ID).Scan(&source); err != nil ||
		!strings.HasPrefix(source, "lib-") {
		t.Fatalf("stored under %q %v", source, err)
	}
	// Another play of the same file lists it.
	again := watchLib(t, f.s.Client("bob", true), "/api/v1/title/movie/605/watch", "")
	list := decode[struct {
		Tracks []subtitleTrack `json:"tracks"`
	}](t, want(t, f.s.Client("bob", true).Do(t, "GET", "/api/v1/title/movie/605/subtitles?ticket="+url.QueryEscape(again.Ticket), nil), 200))
	if len(list.Tracks) != 1 || list.Tracks[0].ID != tr.ID {
		t.Fatalf("%+v", list)
	}
}

// With Jellyfin set up too, a title in both plays from the library, and
// the watch recorded at the end marks Jellyfin's copy played as well.
func TestLibraryPlayMarksJellyfinsCopyPlayed(t *testing.T) {
	f := startLibrariesWith(t, true)
	f.s.SeedLibrary(t) // Jellyfin has movie 603 as item "a"×32
	alice := f.s.Client("alice", true)
	_, jf := link(t, f.s, "alice")
	w := watchLib(t, alice, "/api/v1/title/movie/603/watch", "")
	if w.Source != "library" {
		t.Fatalf("played from %s", w.Source)
	}
	reportWatch(t, alice, w.Ticket, "start", 1)
	if !reportWatch(t, alice, w.Ticket, "stop", 19.5) {
		t.Fatal("no watch recorded")
	}
	if n := f.fakes.Jellyfin.Count("POST /Users/" + jf + "/PlayedItems/" + strings.Repeat("a", 32)); n != 1 {
		t.Fatalf("Jellyfin marked played %d times: %v", n, f.fakes.Jellyfin.Calls())
	}
	// No playback report went to Jellyfin: the play was the library's.
	if n := f.fakes.Jellyfin.Count("POST /Sessions/Playing"); n != 0 {
		t.Fatalf("%d playback reports to Jellyfin", n)
	}
}
