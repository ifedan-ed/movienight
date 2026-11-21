package server_test

// Movie Night's own player ("Play here"): starting a play, the stream proxy,
// switching audio, the reports to Jellyfin and to Movie Night, and the
// watch recorded at the end.

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"movienight/server/internal/jfplay"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

const caps = `"caps":{"direct_containers":["mp4"],"direct_video":["h264"],"direct_audio":["aac"],"hls_video":["h264"],"hls_audio":["aac"]}`

type watchOut struct {
	Ticket    string                `json:"ticket"`
	PlayID    string                `json:"play_id"`
	Mode      string                `json:"mode"`
	URL       string                `json:"url"`
	Duration  float64               `json:"duration_s"`
	Resume    float64               `json:"resume_s"`
	Title     string                `json:"title"`
	AudioIdx  *int                  `json:"audio_index"`
	Subtitle  *int                  `json:"subtitle_index"`
	Audio     []struct{ Index int } `json:"audio"`
	Subtitles []struct {
		Index int     `json:"index"`
		Kind  string  `json:"kind"`
		URL   *string `json:"url"`
	} `json:"subtitles"`
	Segments []struct {
		Kind  string  `json:"kind"`
		Start float64 `json:"start_s"`
	} `json:"segments"`
}

// readyToWatch is readyToPlay plus what PlaybackInfo answers: an MP4 the
// browser plays as it is, two audio tracks, a text and a picture subtitle,
// and Jellyfin's credits marker at 90 of 100 seconds.
func readyToWatch(t *testing.T, s *testenv.Server, u *up) {
	t.Helper()
	readyToPlay(t, s, u)
	u.set(func(u *up) {
		u.file = []byte(strings.Repeat("0123456789", 100))
		u.source = map[string]any{"Id": "ms1", "Container": "mp4", "RunTimeTicks": 100 * jfplay.TicksPerSecond,
			"SupportsDirectPlay": true, "DefaultAudioStreamIndex": 1,
			"MediaStreams": []any{
				map[string]any{"Type": "Video", "Index": 0, "Codec": "h264"},
				map[string]any{"Type": "Audio", "Index": 1, "Codec": "aac", "Language": "eng", "DisplayTitle": "English", "IsDefault": true},
				map[string]any{"Type": "Audio", "Index": 2, "Codec": "aac", "Language": "ger", "DisplayTitle": "Deutsch"},
				map[string]any{"Type": "Subtitle", "Index": 3, "Codec": "subrip", "Language": "eng", "IsTextSubtitleStream": true},
				map[string]any{"Type": "Subtitle", "Index": 4, "Codec": "PGSSUB", "Language": "eng"},
			}}
		u.segments = []map[string]any{{"Type": "Outro", "StartTicks": 90 * jfplay.TicksPerSecond, "EndTicks": 100 * jfplay.TicksPerSecond}}
	})
}

func startWatch(t *testing.T, c *testenv.Client, extra string) watchOut {
	t.Helper()
	return decode[watchOut](t, want(t, c.Do(t, "POST", "/api/v1/title/movie/603/watch", "{"+caps+extra+"}"), 200))
}

func reportWatch(t *testing.T, c *testenv.Client, ticket, event string, pos float64) bool {
	t.Helper()
	body := `{"ticket":"` + ticket + `","event":"` + event + `","position_s":` + strconv.FormatFloat(pos, 'f', -1, 64) + `}`
	return decode[struct {
		Watched bool `json:"watched"`
	}](t, want(t, c.Do(t, "POST", "/api/v1/watch/report", body), 200)).Watched
}

func TestWatchStreamsThroughTheProxyWithoutAnyKey(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToWatch(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)

	r := want(t, alice.Do(t, "POST", "/api/v1/title/movie/603/watch", "{"+caps+"}"), 200)
	if strings.Contains(r.Body, "user-token") || strings.Contains(r.Body, `"test"`) {
		t.Fatalf("a Jellyfin key reached the browser: %s", r.Body)
	}
	w := decode[watchOut](t, r)
	if w.Mode != "direct" || !strings.HasPrefix(w.URL, "/api/v1/stream/"+w.Ticket+"/stream.mp4") || w.Duration != 100 ||
		w.Resume != 60 || w.Title != "Movie 603" || w.AudioIdx == nil || *w.AudioIdx != 1 || len(w.Audio) != 2 {
		t.Fatalf("%+v", w)
	}
	if len(w.Subtitles) != 2 || w.Subtitles[0].Kind != "text" || w.Subtitles[0].URL == nil ||
		*w.Subtitles[0].URL != "/api/v1/stream/"+w.Ticket+"/subtitles/3.vtt" || w.Subtitles[1].Kind != "image" || w.Subtitles[1].URL != nil {
		t.Fatalf("subtitles %+v", w.Subtitles)
	}
	if len(w.Segments) != 1 || w.Segments[0].Kind != "credits" || w.Segments[0].Start != 90 {
		t.Fatalf("segments %+v", w.Segments)
	}
	// PlaybackInfo ran as alice (her token), with no subtitle burnt in.
	asked := u.infoAsked[0]
	if asked["SubtitleStreamIndex"] != float64(-1) || asked["DeviceProfile"] == nil {
		t.Fatalf("PlaybackInfo %v", asked)
	}

	// The file, a range of it, with no cookie at all: the ticket is enough.
	anon := s.Client("", false)
	got := anon.Do(t, "GET", w.URL, nil, "Range", "bytes=10-19")
	if got.StatusCode != http.StatusPartialContent || got.Body != "0123456789" || got.Header.Get("Content-Range") != "bytes 10-19/1000" {
		t.Fatalf("%d %q %v", got.StatusCode, got.Body, got.Header)
	}
	if a := u.mediaAuth[len(u.mediaAuth)-1]; !strings.Contains(a, `Token="test"`) {
		t.Fatalf("the proxy fetched with %q", a)
	}
	want(t, anon.Do(t, "GET", strings.Replace(w.URL, w.Ticket, w.Ticket[:len(w.Ticket)-2]+"xx", 1), nil), http.StatusForbidden)

	// Another audio track: the same play on Jellyfin's HLS, the old
	// transcode stopped, and still no key in the playlist.
	w2 := startWatch(t, alice, `,"audio":2,"replace":"`+w.Ticket+`"`)
	if w2.Mode != "hls" || w2.PlayID != w.PlayID || w2.Ticket == w.Ticket || w2.Resume != 0 ||
		!strings.HasPrefix(w2.URL, "/api/v1/stream/"+w2.Ticket+"/master.m3u8?") || strings.Contains(w2.URL, "ApiKey") ||
		!strings.Contains(w2.URL, "AudioStreamIndex=2") {
		t.Fatalf("%+v", w2)
	}
	if len(u.encodings) != 1 || !strings.Contains(u.encodings[0], "playSessionId=ps-1") {
		t.Fatalf("the old transcode was not stopped: %v", u.encodings)
	}
	pl := want(t, anon.Do(t, "GET", w2.URL, nil), 200)
	if strings.Contains(pl.Body, "user-token") || strings.Contains(strings.ToLower(pl.Body), "apikey") ||
		!strings.Contains(pl.Body, "main.m3u8?") || !strings.Contains(pl.Body, "PlaySessionId=ps-") {
		t.Fatalf("playlist %q", pl.Body)
	}

	// Burning in a text subtitle is refused; a track the file lacks too.
	want(t, alice.Do(t, "POST", "/api/v1/title/movie/603/watch", "{"+caps+`,"burn_subtitle":3}`), 422)
	want(t, alice.Do(t, "POST", "/api/v1/title/movie/603/watch", "{"+caps+`,"audio":9}`), 422)
	// A picture subtitle is burnt in: Jellyfin's HLS.
	if b := startWatch(t, alice, `,"burn_subtitle":4`); b.Mode != "hls" || b.Subtitle == nil || *b.Subtitle != 4 {
		t.Fatalf("burn-in %+v", b)
	}
	// Someone else's ticket cannot be continued.
	link(t, s, "bob")
	want(t, s.Client("bob", true).Do(t, "POST", "/api/v1/title/movie/603/watch", "{"+caps+`,"replace":"`+w.Ticket+`"}`), 403)
}

func TestWatchReportsAndRecordsTheEnd(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToWatch(t, s, u)
	uid, jf := link(t, s, "alice")
	alice := s.Client("alice", true)
	w := startWatch(t, alice, "")

	if reportWatch(t, alice, w.Ticket, "start", 60) {
		t.Fatal("a start recorded the watch")
	}
	reportWatch(t, alice, w.Ticket, "progress", 70.5)
	var pos int
	var state string
	if err := s.Pool.QueryRow(t.Context(), "SELECT position_s, state FROM watch_progress WHERE user_id = $1 AND key = 'movie:603'", uid).
		Scan(&pos, &state); err != nil || pos != 70 || state != "playing" {
		t.Fatalf("progress %d %q %v", pos, state, err)
	}
	reportWatch(t, alice, w.Ticket, "pause", 71)
	if err := s.Pool.QueryRow(t.Context(), "SELECT state FROM watch_progress WHERE user_id = $1", uid).Scan(&state); err != nil || state != "paused" {
		t.Fatalf("state %q %v", state, err)
	}
	// Jellyfin heard every report as alice, on the play's session.
	if len(u.reports) != 3 || u.reports[0].Path != "/Sessions/Playing" || u.reports[1].Path != "/Sessions/Playing/Progress" ||
		!strings.Contains(u.reports[0].Auth, `Token="user-token"`) || u.reports[1].Body["PositionTicks"] != float64(705_000_000) ||
		u.reports[2].Body["IsPaused"] != true || u.reports[0].Body["PlaySessionId"] != "ps-1" {
		t.Fatalf("reports %+v", u.reports)
	}
	// Nobody is asked "Finished it?" about a play here.
	exec(t, s, "UPDATE play_sessions SET started_at = now() - interval '3 hours'")
	if id := openSession(t, alice); id != "" {
		t.Fatalf("asked about an in-browser play: %s", id)
	}

	// Someone else cannot report on alice's play.
	link(t, s, "bob")
	want(t, s.Client("bob", true).Do(t, "POST", "/api/v1/watch/report", `{"ticket":"`+w.Ticket+`","event":"stop","position_s":99}`), 403)

	// Into the credits (90 s of 100): the watch is recorded, once.
	if !reportWatch(t, alice, w.Ticket, "stop", 91) {
		t.Fatal("a stop in the credits did not record the watch")
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE user_id = $1 AND tmdb_id = 603 AND source = 'player'", uid); n != 1 {
		t.Fatalf("%d watches", n)
	}
	if n := count(t, s, "SELECT count(*) FROM watch_progress WHERE user_id = $1", uid); n != 0 {
		t.Fatal("the position was kept after the end")
	}
	if len(u.marked) != 1 || u.marked[0] != jf+"/"+filmItem {
		t.Fatalf("marked %v", u.marked)
	}
	reportWatch(t, alice, w.Ticket, "stop", 95)
	if n := count(t, s, "SELECT count(*) FROM history WHERE user_id = $1", uid); n != 1 {
		t.Fatalf("a second stop recorded %d watches", n)
	}
}

func TestWatchStopEarlyKeepsThePlaceAndAskNothing(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToWatch(t, s, u)
	// No token: the install's key plays, and alice's Jellyfin resume point
	// is set by hand.
	uid := userID(t, s, "alice")
	exec(t, s, "UPDATE users SET jellyfin_user_id = $2 WHERE id = $1", uid, strings.Repeat("d", 32))
	alice := s.Client("alice", true)
	w := startWatch(t, alice, "")
	reportWatch(t, alice, w.Ticket, "start", 0)
	if reportWatch(t, alice, w.Ticket, "stop", 40) {
		t.Fatal("a stop at 40 % recorded the watch")
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE user_id = $1", uid); n != 0 {
		t.Fatal("recorded")
	}
	var state string
	var pos int
	if err := s.Pool.QueryRow(t.Context(), "SELECT state, position_s FROM watch_progress WHERE user_id = $1", uid).Scan(&state, &pos); err != nil ||
		state != "stopped" || pos != 40 {
		t.Fatalf("%q %d %v", state, pos, err)
	}
	if r := row(t, s, w.PlayID); outcome(r) != "not_finished" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(u.reports[0].Auth, `Token="test"`) {
		t.Fatalf("auth %q", u.reports[0].Auth)
	}
	if len(u.positions) != 1 || u.positions[0] != strings.Repeat("d", 32)+"/"+filmItem+"/4e+08" {
		t.Fatalf("positions %v", u.positions)
	}
	// The next play starts where she stopped: Jellyfin's resume point
	// first, Movie Night's own when Jellyfin has none.
	if w2 := startWatch(t, alice, ""); w2.Resume != 60 {
		t.Fatalf("resume %v", w2.Resume)
	}
	u.set(func(u *up) { u.items[filmItem]["UserData"] = map[string]any{"PlaybackPositionTicks": 0} })
	reportWatch(t, alice, startWatch(t, alice, "").Ticket, "stop", 40)
	if w3 := startWatch(t, alice, ""); w3.Resume != 40 {
		t.Fatalf("resume %v", w3.Resume)
	}
}
