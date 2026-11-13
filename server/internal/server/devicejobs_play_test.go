package server_test

// Playing in an outside player and "Finished it?" (tests/test_play_prompt.py
// and the play and players tests of tests/test_shows_play_sync.py).

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

const filmItem = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// readyToPlay puts film 603 in the library, as Jellyfin's item with its
// file and subtitle tracks.
func readyToPlay(t *testing.T, s *testenv.Server, u *up) {
	t.Helper()
	exec(t, s, `INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, synced_at) VALUES ($1, 'movie', 603, 'Movie 603', now())`, filmItem)
	u.set(func(u *up) {
		u.items[filmItem] = map[string]any{"Id": filmItem, "Type": "Movie", "Name": "Movie 603",
			"UserData": map[string]any{"PlaybackPositionTicks": 600_000_000},
			"MediaSources": []any{map[string]any{"Id": "ms1", "Container": "mkv", "DefaultSubtitleStreamIndex": 2,
				"MediaStreams": []any{
					map[string]any{"Type": "Video", "Index": 0},
					map[string]any{"Type": "Subtitle", "Index": 2, "Language": "eng", "DisplayTitle": "English", "IsTextSubtitleStream": true},
					map[string]any{"Type": "Subtitle", "Index": 3, "Language": "eng", "DisplayTitle": "English (file)", "IsExternal": true, "IsTextSubtitleStream": true},
					map[string]any{"Type": "Subtitle", "Index": 4, "Language": "ger", "IsTextSubtitleStream": true},
				}}}}
	})
}

type playOut struct {
	SessionID string  `json:"session_id"`
	URL       string  `json:"url"`
	WebURL    *string `json:"web_url"`
}

func play(t *testing.T, c *testenv.Client, body string) playOut {
	t.Helper()
	return decode[playOut](t, want(t, c.Do(t, "POST", "/api/v1/title/movie/603/play", body), 200))
}

func age(t *testing.T, s *testenv.Server, sid string, minutes int) {
	t.Helper()
	exec(t, s, "UPDATE play_sessions SET started_at = started_at - make_interval(mins => $2) WHERE id = $1", sid, minutes)
}

type sessionRow struct {
	Outcome  *string
	Finished bool
	Asked    bool
}

func row(t *testing.T, s *testenv.Server, sid string) sessionRow {
	t.Helper()
	var r sessionRow
	if err := s.Pool.QueryRow(t.Context(), "SELECT outcome, finished_at IS NOT NULL, asked_at IS NOT NULL FROM play_sessions WHERE id = $1", sid).
		Scan(&r.Outcome, &r.Finished, &r.Asked); err != nil {
		t.Fatal(err)
	}
	return r
}

func outcome(r sessionRow) string {
	if r.Outcome == nil {
		return ""
	}
	return *r.Outcome
}

type back struct {
	Ask     bool `json:"ask"`
	Session struct {
		ID string `json:"id"`
	} `json:"session"`
}

func openSession(t *testing.T, c *testenv.Client) string {
	t.Helper()
	r := want(t, c.Do(t, "GET", "/api/v1/play/open", nil), 200)
	got := decode[struct {
		Session *struct {
			ID     string  `json:"id"`
			Key    *string `json:"key"`
			Poster *string `json:"poster"`
		} `json:"session"`
	}](t, r)
	if got.Session == nil {
		return ""
	}
	if got.Session.Key == nil || *got.Session.Key != "movie:603" || got.Session.Poster == nil {
		t.Fatalf("%s", r.Body)
	}
	return got.Session.ID
}

func TestPlayNeedsALinkThenBuildsThePlayerLink(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	alice := s.Client("alice", true)
	info := decode[map[string]any](t, want(t, alice.Do(t, "GET", "/api/v1/title/movie/603/play", nil), 200))
	if info["available"] != true || info["linked"] != false {
		t.Fatal(info)
	}
	r := want(t, alice.Do(t, "POST", "/api/v1/title/movie/603/play", `{"player":"vlc"}`), 409)
	if !strings.Contains(r.Body, "Connect your Jellyfin account first") {
		t.Fatal(r.Body)
	}
	_, jf := link(t, s, "alice")
	info = decode[map[string]any](t, alice.Do(t, "GET", "/api/v1/title/movie/603/play", nil))
	// The English file beats the embedded track; where the person stopped.
	if info["chosen_subtitle"] != float64(3) || info["resume_seconds"] != float64(60) || info["title"] != "Movie 603" ||
		len(info["subtitles"].([]any)) != 3 || info["default_player"] != "vlc" {
		t.Fatal(info)
	}
	out := play(t, alice, `{"player":"vlc","subtitle":3}`)
	if !strings.HasPrefix(out.URL, "vlc-x-callback://x-callback-url/stream?url=https%3A%2F%2Fjf.test%2FVideos%2F"+filmItem+"%2Fstream.mkv") ||
		!strings.Contains(out.URL, "Subtitles%2F3%2F0%2FStream.srt") || strings.Contains(out.URL, "x-success") ||
		!strings.Contains(out.URL, "api_key%3Duser-token") {
		t.Fatal(out.URL)
	}
	// No subtitle: the empty &sub= goes (it confuses some players); Infuse
	// gets the file's name.
	inf := play(t, alice, `{"player":"infuse"}`)
	if strings.Contains(inf.URL, "&sub=") || !strings.Contains(inf.URL, "&filename=Movie%20603.mkv") || !strings.Contains(inf.URL, "static%3Dtrue") {
		t.Fatal(inf.URL)
	}
	// A subtitle the file does not have is left out, not passed on.
	if odd := play(t, alice, `{"player":"vlc","subtitle":9}`); strings.Contains(odd.URL, "Subtitles") {
		t.Fatal(odd.URL)
	}
	// Starting another Play closed the first without a question.
	if r := row(t, s, out.SessionID); !r.Finished || outcome(r) != "not_finished" || r.Asked {
		t.Fatalf("%+v", r)
	}
	want(t, alice.Do(t, "POST", "/api/v1/title/movie/603/play", `{"player":"custom9"}`), 422)
	want(t, alice.Do(t, "POST", "/api/v1/title/movie/999/play", `{"player":"vlc"}`), 404)

	// "Finished it?" yes: the watch, off the watchlist, played in Jellyfin.
	last := play(t, alice, `{"player":"infuse"}`)
	exec(t, s, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source) SELECT id, 'movie', 603, now(), 'app' FROM users WHERE username = 'alice'`)
	age(t, s, last.SessionID, 180)
	if got := openSession(t, alice); got != last.SessionID {
		t.Fatalf("asked about %q", got)
	}
	want(t, alice.Do(t, "POST", "/api/v1/play/"+last.SessionID+"/finish", `{"outcome":"watched"}`), 200)
	if n := count(t, s, "SELECT count(*) FROM history WHERE source = 'player' AND tmdb_id = 603 AND title = 'Movie 603'"); n != 1 {
		t.Fatal(n)
	}
	if n := count(t, s, "SELECT count(*) FROM watchlist WHERE removed_at IS NULL"); n != 0 {
		t.Fatal("still on the watchlist")
	}
	u.set(func(u *up) {
		if len(u.marked) != 1 || u.marked[0] != jf+"/"+filmItem {
			t.Errorf("%v", u.marked)
		}
	})
}

func TestBackFromThePlayerEarlyClosesWithoutAsking(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, sid, 5)
	b := decode[back](t, want(t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/back", nil), 200))
	if b.Ask || b.Session.ID != sid {
		t.Fatalf("%+v", b)
	}
	if r := row(t, s, sid); outcome(r) != "not_finished" || r.Asked {
		t.Fatalf("%+v", r)
	}
	if openSession(t, alice) != "" {
		t.Fatal("asked after an early return")
	}
	// Someone else's Play is not found.
	want(t, s.Client("bob", true).Do(t, "POST", "/api/v1/play/"+sid+"/back", nil), 404)
	want(t, s.Client("bob", false).Do(t, "GET", "/api/v1/play/"+sid, nil), 404)
	want(t, alice.Do(t, "GET", "/api/v1/play/"+sid, nil), 200)
	want(t, alice.Do(t, "GET", "/api/v1/play/short", nil), 422)
}

func TestAskedOnceWhenTheFilmCouldHaveEnded(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, sid, 180)
	if b := decode[back](t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/back", nil)); !b.Ask {
		t.Fatal("not asked")
	}
	if b := decode[back](t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/back", nil)); b.Ask {
		t.Fatal("asked twice")
	}
	if openSession(t, alice) != "" {
		t.Fatal("asked again on return")
	}
	want(t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/finish", `{"outcome":"watched"}`), 200)
	if r := row(t, s, sid); outcome(r) != "watched" {
		t.Fatalf("%+v", r)
	}
}

func TestComingBackWaitsForTheFilmAndClosesNothing(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	if openSession(t, alice) != "" { // the player is still opening
		t.Fatal("asked at once")
	}
	age(t, s, sid, 20)
	if openSession(t, alice) != "" { // too soon for a 100-minute film (60 % of it)
		t.Fatal("asked too soon")
	}
	if row(t, s, sid).Finished {
		t.Fatal("a glance back mid-film closed it")
	}
	age(t, s, sid, 180)
	if openSession(t, alice) != sid {
		t.Fatal("not asked")
	}
	if openSession(t, alice) != "" {
		t.Fatal("asked twice")
	}
}

func TestAPlayerThatNeverOpenedIsNeverAskedAbout(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	want(t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/finish", `{"outcome":"dismissed"}`), 200)
	age(t, s, sid, 180)
	if openSession(t, alice) != "" {
		t.Fatal("asked about a dismissed Play")
	}
	// A later answer changes nothing: the Play was finished once.
	want(t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/finish", `{"outcome":"watched"}`), 200)
	if n := count(t, s, "SELECT count(*) FROM history"); n != 0 {
		t.Fatal(n)
	}
	want(t, alice.Do(t, "POST", "/api/v1/play/"+sid+"/finish", `{"outcome":"loved"}`), 422)
}

func TestTwoAnswersAtOnceRecordOneWatch(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, sid, 180)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want(t, s.Client("alice", true).Do(t, "POST", "/api/v1/play/"+sid+"/finish", `{"outcome":"watched"}`), 200)
		}()
	}
	wg.Wait()
	if n := count(t, s, "SELECT count(*) FROM history"); n != 1 {
		t.Fatalf("%d watches for one Play", n)
	}
	u.set(func(u *up) {
		if len(u.marked) != 1 {
			t.Errorf("marked played %d times", len(u.marked))
		}
	})
	// And two tabs coming back at once are asked once.
	sid2 := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, sid2, 180)
	var mu sync.Mutex
	asked := 0
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if openSession(t, s.Client("alice", false)) == sid2 {
				mu.Lock()
				asked++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
}

func TestANewPlayClosesTheOneBefore(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	first := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, first, 180)
	play(t, alice, `{"player":"infuse"}`)
	if r := row(t, s, first); !r.Finished || outcome(r) != "not_finished" || r.Asked {
		t.Fatalf("%+v", r)
	}
	if b := decode[back](t, alice.Do(t, "POST", "/api/v1/play/"+first+"/back", nil)); b.Ask {
		t.Fatal("asked about a closed Play")
	}
}

func TestFinishedItCanBeSwitchedOff(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	link(t, s, "alice")
	alice := s.Client("alice", true)
	if got := decode[map[string]any](t, alice.Do(t, "GET", "/api/v1/me/players", nil)); got["ask_finished"] != true {
		t.Fatal(got)
	}
	if got := decode[map[string]any](t, want(t, alice.Do(t, "PUT", "/api/v1/me/players", `{"ask_finished":false}`), 200)); got["ask_finished"] != false {
		t.Fatal(got)
	}
	sid := play(t, alice, `{"player":"infuse"}`).SessionID
	age(t, s, sid, 180)
	if openSession(t, alice) != "" {
		t.Fatal("asked while switched off")
	}
	if r := row(t, s, sid); outcome(r) != "dismissed" {
		t.Fatalf("%+v", r)
	}
}

func TestSwiftfinAndStreamyfinOpenTheTitleAndNobodyIsAsked(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	readyToPlay(t, s, u)
	alice := s.Client("alice", true)
	_, jf := link(t, s, "alice")
	r := play(t, alice, `{"player":"swiftfin"}`)
	if r.URL != fmt.Sprintf("swiftfin://%s/%s/item/%s", "5e1f"+strings.Repeat("0", 28), jf, filmItem) {
		t.Fatal(r.URL)
	}
	age(t, s, r.SessionID, 300)
	if openSession(t, alice) != "" { // Jellyfin records Swiftfin's plays itself
		t.Fatal("asked about a Swiftfin play")
	}
	if r := play(t, alice, `{"player":"streamyfin"}`); r.URL != "streamyfin://items/page?id="+filmItem {
		t.Fatal(r.URL)
	}
	// The Jellyfin app opens the website, with no Jellyfin token needed.
	exec(t, s, "UPDATE users SET jellyfin_token_enc = NULL WHERE username = 'alice'")
	if r := play(t, alice, `{"player":"jellyfin"}`); r.URL != "https://jf.test/web/#/details?id="+filmItem {
		t.Fatal(r.URL)
	}
}

func TestPlayerSettings(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	for _, bad := range []string{
		`{"custom":[{"name":"Evil","template":"javascript:alert(1)//{url}"}]}`,
		`{"custom":[{"name":"No video","template":"nplayer-{title}"}]}`,
		`{"custom":[{"name":"Odd","template":"x://{url}{nope}"}]}`,
		`{"custom":[{"name":"  ","template":"x://{url}"}]}`,
		`{"enabled":["VLC"]}`,
	} {
		want(t, alice.Do(t, "PUT", "/api/v1/me/players", bad), 422)
	}
	got := decode[struct {
		Players []struct {
			ID       string  `json:"id"`
			Enabled  bool    `json:"enabled"`
			Template *string `json:"template"`
		} `json:"players"`
		DefaultPlayer    string `json:"default_player"`
		SubtitleLanguage string `json:"subtitle_language"`
	}](t, want(t, alice.Do(t, "PUT", "/api/v1/me/players",
		`{"custom":[{"name":"nPlayer","template":"nplayer-{url_raw}"}],"enabled":["vlc","custom1"],"default_player":"custom1","subtitle_language":"GER"}`), 200))
	var on []string
	for _, p := range got.Players {
		if p.Enabled {
			on = append(on, p.ID)
		}
	}
	if strings.Join(on, ",") != "vlc,custom1" || got.DefaultPlayer != "custom1" || got.SubtitleLanguage != "ger" {
		t.Fatalf("%+v", got)
	}
	// A default no longer ticked falls back to the first ticked.
	got2 := decode[map[string]any](t, alice.Do(t, "PUT", "/api/v1/me/players", `{"custom":[{"name":"nPlayer","template":"nplayer-{url_raw}"}],"enabled":["outplayer"]}`))
	if got2["default_player"] != "outplayer" {
		t.Fatal(got2)
	}
}
