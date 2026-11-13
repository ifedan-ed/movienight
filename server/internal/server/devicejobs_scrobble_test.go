package server_test

// Scrobbles from Kodi and other players, and the personal scrobble token.

import (
	"strings"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

type scrobbleOut struct {
	Ok       bool    `json:"ok"`
	Recorded bool    `json:"recorded"`
	Detail   *string `json:"detail"`
}

func scrobble(t *testing.T, c *testenv.Client, body string, status int) scrobbleOut {
	t.Helper()
	return decode[scrobbleOut](t, want(t, c.Do(t, "POST", "/api/v1/scrobble", body), status))
}

func TestScrobbleTokenReportsAWatchOnceWithTMDBsTitle(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	tok := decode[struct{ Token string }](t, want(t, alice.Do(t, "POST", "/api/v1/me/scrobble-token", nil), 200)).Token
	if !strings.HasPrefix(tok, "mn_") {
		t.Fatal(tok)
	}
	kodi := bearer(s, tok, false)
	got := scrobble(t, kodi, `{"media_type":"movie","tmdb_id":603,"source":"kodi","ref":"p1"}`, 200)
	if !got.Ok || !got.Recorded || got.Detail == nil || *got.Detail != "Movie 603" {
		t.Fatalf("%+v", got)
	}
	again := scrobble(t, kodi, `{"media_type":"movie","tmdb_id":603,"source":"kodi","ref":"p1"}`, 200)
	if again.Recorded || again.Detail == nil || *again.Detail != "already recorded" {
		t.Fatalf("%+v", again)
	}
	want(t, bearer(s, "nope", false).Do(t, "POST", "/api/v1/scrobble", `{"media_type":"movie","tmdb_id":603}`), 401)
	// A new token turns the old one off; dropping it turns it off too.
	tok2 := decode[struct{ Token string }](t, alice.Do(t, "POST", "/api/v1/me/scrobble-token", nil)).Token
	want(t, kodi.Do(t, "POST", "/api/v1/scrobble", `{"media_type":"movie","tmdb_id":604}`), 401)
	want(t, bearer(s, tok2, false).Do(t, "POST", "/api/v1/scrobble", `{"media_type":"movie","tmdb_id":604}`), 200)
	want(t, alice.Do(t, "DELETE", "/api/v1/me/scrobble-token", nil), 200)
	want(t, bearer(s, tok2, false).Do(t, "POST", "/api/v1/scrobble", `{"media_type":"movie","tmdb_id":605}`), 401)
	if n := count(t, s, "SELECT count(*) FROM history WHERE source = 'kodi'"); n != 1 {
		t.Fatal(n)
	}
}

func TestKodiPairsAndReportsAWatch(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	token, _ := pair(t, s, "alice", "Kodi")
	tv := bearer(s, token, false)
	if got := scrobble(t, tv, `{"media_type":"movie","tmdb_id":603,"source":"kodi","ref":"kodi-1"}`, 200); !got.Recorded {
		t.Fatalf("%+v", got)
	}
	if got := scrobble(t, tv, `{"media_type":"movie","tmdb_id":603,"source":"kodi","ref":"kodi-1"}`, 200); got.Recorded {
		t.Fatalf("%+v", got)
	}
	// A TV signed out says so in words the TV shows as they are.
	devs := listDevices(t, s.Client("alice", false))
	want(t, s.Client("alice", true).Do(t, "DELETE", "/api/v1/me/devices/"+itoa(devs[0].ID), nil), 200)
	r := want(t, tv.Do(t, "POST", "/api/v1/scrobble", `{"media_type":"movie","tmdb_id":603}`), 401)
	if !strings.Contains(r.Body, "signed out of Movie Night") {
		t.Fatal(r.Body)
	}
}

func TestAnEpisodeFromATheTVDBScrapedLibrary(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	u.set(func(u *up) {
		u.find["121361"] = map[string]any{"movie_results": []any{}, "tv_results": []any{map[string]any{"id": 1399, "name": "Show 1399"}}}
	})
	token, _ := pair(t, s, "alice", "Kodi")
	got := scrobble(t, bearer(s, token, false), `{"media_type":"episode","tvdb_id":121361,"season":1,"episode":2,"title":"Some Show","source":"kodi","ref":"kodi-ep-1"}`, 200)
	if !got.Recorded || *got.Detail != "Show 1399" {
		t.Fatalf("%+v", got)
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE tmdb_id = 1399 AND media_type = 'episode' AND season = 1 AND episode = 2"); n != 1 {
		t.Fatal(n)
	}
}

func TestScrobbleFindsTitlesByIMDbAndKeepsTheKindApart(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	tv := bearer(s, newToken(t, s, "alice"), false)
	// In the title cache: found without asking TMDB.
	exec(t, s, `INSERT INTO titles (key, media_type, tmdb_id, title, imdb_id, detailed, fetched_at)
		VALUES ('movie:603', 'movie', 603, 'The Matrix', 'tt0133093', true, now())`)
	if got := scrobble(t, tv, `{"media_type":"movie","imdb_id":"tt0133093","ref":"a"}`, 200); *got.Detail != "The Matrix" {
		t.Fatalf("%+v", got)
	}
	// Not cached: TMDB's find. A film's IMDb id never names a show: an
	// episode sent with it and no title is refused, not filed under a film.
	u.set(func(u *up) {
		u.find["tt0000001"] = map[string]any{"movie_results": []any{map[string]any{"id": 11}}, "tv_results": []any{}}
	})
	if got := scrobble(t, tv, `{"media_type":"movie","imdb_id":"tt0000001","ref":"b"}`, 200); *got.Detail != "Movie 11" {
		t.Fatalf("%+v", got)
	}
	want(t, tv.Do(t, "POST", "/api/v1/scrobble", `{"media_type":"episode","imdb_id":"tt0000001","season":1,"episode":1}`), 422)
}

func TestScrobbleRefusesWhatItCannotRecord(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	tv := bearer(s, newToken(t, s, "alice"), false)
	for body, why := range map[string]string{
		`{"media_type":"movie"}`:                                                 "Send tmdb_id, imdb_id or tvdb_id",
		`{"media_type":"movie","tmdb_id":1,"watched_at":"x"}`:                    "watched_at must be ISO 8601",
		`{"media_type":"movie","tmdb_id":1,"watched_at":"2999-01-01T00:00:00Z"}`: "watched_at is in the future",
		`{"media_type":"show","tmdb_id":1}`:                                      "",
		`{"media_type":"movie","tmdb_id":0}`:                                     "",
		`{"media_type":"movie","imdb_id":"603"}`:                                 "",
	} {
		r := want(t, tv.Do(t, "POST", "/api/v1/scrobble", body), 422)
		if !strings.Contains(r.Body, why) {
			t.Errorf("%s: %s", body, r.Body)
		}
	}
	if n := count(t, s, "SELECT count(*) FROM history"); n != 0 {
		t.Fatal(n)
	}
}

func TestScrobbleWithATitleAloneAndTheMadeUpRef(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	tv := bearer(s, newToken(t, s, "alice"), false)
	body := `{"media_type":"movie","title":"A Home Video","watched_at":"2026-11-01T20:00:00+01:00"}`
	if got := scrobble(t, tv, body, 200); !got.Recorded || *got.Detail != "A Home Video" {
		t.Fatalf("%+v", got)
	}
	if got := scrobble(t, tv, body, 200); got.Recorded {
		t.Fatalf("a resend was recorded again: %+v", got)
	}
	// The made-up reference is the Python's, None and all, with the date in
	// the sender's own zone: a report the Python app recorded is still
	// "already recorded" here.
	var ref string
	_ = s.Pool.QueryRow(t.Context(), "SELECT source_ref FROM history").Scan(&ref)
	if ref != "movie:A Home Video:None:None:2026-11-01" {
		t.Fatal(ref)
	}
	// A long ref is kept as its hash, recorded once, never cut.
	long := strings.Repeat("r", 300)
	for i, wantNew := range []bool{true, false} {
		if got := scrobble(t, tv, `{"media_type":"movie","tmdb_id":5,"ref":"`+long+`"}`, 200); got.Recorded != wantNew {
			t.Fatalf("%d: %+v", i, got)
		}
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE source_ref LIKE 'sha256:%'"); n != 1 {
		t.Fatal(n)
	}
}

// newToken is the person's scrobble token.
func newToken(t *testing.T, s *testenv.Server, name string) string {
	t.Helper()
	return decode[struct{ Token string }](t, want(t, s.Client(name, true).Do(t, "POST", "/api/v1/me/scrobble-token", nil), 200)).Token
}
