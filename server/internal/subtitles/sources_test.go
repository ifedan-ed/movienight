package subtitles

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/testbase"
)

// fake is one source's site: a handler, and every request it was sent.
type fake struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *fake {
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.URL.Path)
	}
	return out
}

func (f *fake) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type obj = map[string]any

var testWeb = NewWeb(http.DefaultClient, "Movie Night v2.0")

func film(t *testing.T) Query {
	return Query{Media: "movie", TMDBID: 603, IMDbID: "tt0133093", Title: "The Matrix", Year: 1999, Language: mustLang(t, "en")}
}

func episode(t *testing.T) Query {
	return Query{Media: "tv", TMDBID: 1399, IMDbID: "tt0944947", Title: "Game of Thrones", Year: 2011, Season: 1, Episode: 2,
		Language: mustLang(t, "en")}
}

const srt = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"

// jwt is a token whose payload expires at exp (OpenSubtitles' tokens are
// JWTs; the signature is never checked here).
func jwt(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"
}

func TestOpenSubtitlesSearchAndDownload(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	var logins int
	var mu sync.Mutex
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			mu.Lock()
			logins++
			mu.Unlock()
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["username"] != "me" || b["password"] != "pw" || r.Header.Get("Api-Key") != "key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			reply(w, obj{"token": jwt(time.Now().Add(24 * time.Hour)), "base_url": "api.opensubtitles.com", "status": 200,
				"user": obj{"allowed_downloads": 20}})
		case "/subtitles":
			reply(w, obj{"total_count": 3, "data": []obj{
				{"id": "1", "attributes": obj{"language": "en", "download_count": 12, "ratings": 7.5, "hearing_impaired": true,
					"release": "The.Matrix.1999.1080p", "moviehash_match": true, "files": []obj{{"file_id": 77, "file_name": "a.srt"}}}},
				{"id": "2", "attributes": obj{"language": "en", "ai_translated": true, "release": "AI one",
					"files": []obj{{"file_id": 78, "file_name": "b.srt"}}}},
				{"id": "3", "attributes": obj{"language": "en", "foreign_parts_only": true, "files": []obj{{"file_id": 79}}}},
			}})
		case "/download":
			var b map[string]int
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["file_id"] == 99 {
				w.WriteHeader(http.StatusNotAcceptable)
				reply(w, obj{"requests": 20, "remaining": 0, "message": "You have downloaded your allowed 20 subtitles for 24h",
					"reset_time_utc": time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)})
				return
			}
			reply(w, obj{"link": "http://" + r.Host + "/file/77", "remaining": 5, "reset_time_utc": ""})
		case "/file/77":
			_, _ = io.WriteString(w, srt)
		default:
			http.NotFound(w, r)
		}
	})
	o := NewOpenSubtitles(testWeb, f.URL, "key", "me", "pw", kv)
	q := film(t)
	q.Hash = "8e245d9679d31e12"
	rs, err := o.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || !rs[0].HashMatch || !rs[0].HearingImpaired || rs[0].Downloads != 12 || rs[0].Ref != "77" || !rs[1].Machine {
		t.Fatalf("%+v", rs)
	}
	// Sorted, lower case, ids as bare numbers: no redirect.
	if got := f.last().URL.RawQuery; got != "imdb_id=133093&languages=en&moviehash=8e245d9679d31e12" {
		t.Fatal(got)
	}
	if h := f.last().Header; h.Get("Api-Key") != "key" || h.Get("User-Agent") != "Movie Night v2.0" ||
		!strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		t.Fatal(h)
	}
	// An episode is asked for by the series' id.
	if _, err := o.Search(ctx, episode(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.last().URL.RawQuery; got != "episode_number=2&languages=en&parent_imdb_id=944947&season_number=1" {
		t.Fatal(got)
	}
	body, err := o.Download(ctx, rs[0])
	if err != nil || string(body) != srt {
		t.Fatal(string(body), err)
	}
	if logins != 1 {
		t.Fatalf("logged in %d times; the token is kept in Valkey", logins)
	}
	if used, _ := o.Quota(ctx); used {
		t.Fatal("quota marked used with 5 left")
	}

	// The quota: a 406 says until when, and then no download is tried.
	_, err = o.Download(ctx, Result{Source: "opensubtitles", ID: "99", Ref: "99"})
	var p *Problem
	if !errors.As(err, &p) || !p.Quota || p.Until.IsZero() {
		t.Fatalf("%v", err)
	}
	if used, until := o.Quota(ctx); !used || until.Before(time.Now().Add(2*time.Hour)) {
		t.Fatal(used, until)
	}
	before := len(f.paths())
	if _, err := o.Download(ctx, rs[0]); !errors.As(err, &p) || !p.Quota {
		t.Fatal(err)
	}
	if len(f.paths()) != before {
		t.Fatal("a download was tried with the quota used up")
	}
}

func TestOpenSubtitlesRefusedLoginIsNotRetried(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply(w, obj{"data": []obj{}})
	})
	o := NewOpenSubtitles(testWeb, f.URL, "key", "me", "wrong", kv)
	// Searching still works without the login.
	if rs, err := o.Search(ctx, film(t)); err != nil || len(rs) != 0 {
		t.Fatal(rs, err)
	}
	if _, err := o.Download(ctx, Result{Ref: "1"}); err == nil || !strings.Contains(err.Error(), "username or password") {
		t.Fatal(err)
	}
	logins := 0
	for _, p := range f.paths() {
		if p == "POST /login" {
			logins++
		}
	}
	if logins != 1 {
		t.Fatalf("%d logins: a refused one must not be tried again", logins)
	}
	if tokenLife(jwt(time.Now().Add(time.Hour)), time.Now()) > time.Hour || tokenLife("not-a-jwt", time.Now()) != 12*time.Hour {
		t.Fatal("token life")
	}
}

func TestSubDL(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/subtitles" && r.URL.Query().Get("imdb_id") == "tt0000000":
			w.WriteHeader(http.StatusNotFound)
			reply(w, obj{"status": false, "error": "can't find movie or tv"})
		case r.URL.Path == "/api/v1/subtitles":
			reply(w, obj{"status": true, "subtitles": []obj{
				{"release_name": "GoT.S01E02.720p", "name": "x", "url": "/subtitle/1-2.zip", "season": 1, "episode": 2, "hi": true,
					"releases": []string{"GoT.S01E02.720p", "GoT.S01E02.1080p"}},
				{"release_name": "GoT.S01E05", "url": "/subtitle/5.zip", "season": 1, "episode": 5},
				{"release_name": "GoT.S01.Pack", "url": "/subtitle/pack.zip", "season": 1, "full_season": true},
			}})
		case r.URL.Path == "/files/subtitle/1-2.zip":
			_, _ = io.WriteString(w, srt)
		default:
			http.NotFound(w, r)
		}
	})
	s := NewSubDL(testWeb, f.URL+"/api/v1", f.URL+"/files", "k")
	rs, err := s.Search(ctx, episode(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != "/subtitle/1-2.zip" || !rs[0].HearingImpaired || rs[0].Release != "GoT.S01E02.720p\nGoT.S01E02.1080p" {
		t.Fatalf("%+v", rs)
	}
	q := f.last().URL.Query()
	if q.Get("api_key") != "k" || q.Get("languages") != "EN" || q.Get("type") != "tv" || q.Get("imdb_id") != "tt0944947" ||
		q.Get("season_number") != "1" || q.Get("episode_number") != "2" {
		t.Fatal(q)
	}
	body, err := s.Download(ctx, rs[0])
	if err != nil || string(body) != srt {
		t.Fatal(string(body), err)
	}
	none := film(t)
	none.IMDbID = "tt0000000"
	if rs, err := s.Search(ctx, none); err != nil || len(rs) != 0 {
		t.Fatal(rs, err)
	}
	pb := film(t)
	pb.Language = mustLang(t, "pt-br")
	_, _ = s.Search(ctx, pb)
	if got := f.last().URL.Query().Get("languages"); got != "BR_PT" {
		t.Fatal(got)
	}
}

func TestSubSource(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		// Bazarr's shape: the key as api_key on every call, never a header.
		if r.URL.Query().Get("api_key") != "k" || r.Header.Get("X-API-Key") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/movies/search":
			reply(w, obj{"data": []obj{{"movieId": 501, "title": "The Matrix", "releaseYear": 1999}}})
		case "/subtitles":
			reply(w, obj{"success": true, "data": []obj{
				{"subtitleId": 9, "language": "english", "releaseInfo": []string{"The.Matrix.1999.BluRay"}, "commentary": "SDH",
					"downloads": 40, "rating": 9},
				{"subtitleId": 10, "foreignParts": true},
			}})
		case "/subtitles/9/download":
			_, _ = io.WriteString(w, srt)
		default:
			http.NotFound(w, r)
		}
	})
	s := NewSubSource(testWeb, f.URL, "k")
	rs, err := s.Search(ctx, film(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].ID != "9" || !rs[0].HearingImpaired || rs[0].Downloads != 40 {
		t.Fatalf("%+v", rs)
	}
	if q := f.last().URL.Query(); q.Get("movieId") != "501" || q.Get("language") != "english" || q.Get("limit") != "100" {
		t.Fatal(q)
	}
	if body, err := s.Download(ctx, rs[0]); err != nil || string(body) != srt {
		t.Fatal(err)
	}
	bad := NewSubSource(testWeb, f.URL, "wrong")
	if _, err := bad.Search(ctx, film(t)); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatal(err)
	}
}

func TestJimaku(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "jk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/entries/search":
			if r.URL.Query().Get("tmdb_id") != "tv:1399" {
				reply(w, []obj{})
				return
			}
			reply(w, []obj{{"id": 7, "name": "Anime", "flags": obj{"anime": true}}})
		case "/api/entries/7/files":
			url := "http://" + r.Host + "/dl/"
			reply(w, []obj{
				{"name": "Anime - 02.ja.srt", "url": url + "a", "size": 30000},
				{"name": "Anime - 02.whisperai.srt", "url": url + "b", "size": 30000},
				{"name": "broken.srt", "url": url + "c", "size": 12},
				{"name": "Anime - 02.7z", "url": url + "d", "size": 30000},
			})
		case "/dl/a":
			_, _ = io.WriteString(w, srt)
		}
	})
	j := NewJimaku(testWeb, f.URL+"/api", "jk")
	q := episode(t)
	if rs, err := j.Search(ctx, q); err != nil || rs != nil {
		t.Fatal("English is not asked of Jimaku", rs, err)
	}
	q.Language = mustLang(t, "ja")
	rs, err := j.Search(ctx, q)
	if err != nil || len(rs) != 2 || rs[0].Machine || !rs[1].Machine {
		t.Fatalf("%+v %v", rs, err)
	}
	if got := f.last().URL.Query().Get("episode"); got != "2" {
		t.Fatal(got)
	}
	if body, err := j.Download(ctx, rs[0]); err != nil || string(body) != srt {
		t.Fatal(err)
	}
}

func TestPodnapisi(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles/search/advanced":
			if r.Header.Get("Accept") != "application/json" {
				_, _ = io.WriteString(w, "<html>")
				return
			}
			reply(w, obj{"page": 1, "all_pages": 1, "data": []obj{
				{"id": "abc", "flags": []string{"hearing_impaired"}, "releases": []string{"The.Matrix.1999.DVDRip"},
					"movie": obj{"year": 1999}, "stats": obj{"downloads": 300}},
				{"id": "remake", "releases": []string{"The.Matrix.2031"}, "movie": obj{"year": 2031}},
			}})
		case "/subtitles/abc/download":
			if r.URL.Query().Get("container") == "zip" {
				_, _ = w.Write(zipOf(t, map[string]string{"m.srt": srt}))
			}
		}
	})
	p := NewPodnapisi(testWeb, f.URL+"/subtitles")
	rs, err := p.Search(ctx, film(t))
	if err != nil || len(rs) != 1 || rs[0].ID != "abc" || !rs[0].HearingImpaired || rs[0].Downloads != 300 {
		t.Fatalf("%+v %v", rs, err)
	}
	if q := f.last().URL.Query(); q.Get("keywords") != "The Matrix" || q.Get("year") != "1999" || q.Get("movie_type") != "movie" {
		t.Fatal(q)
	}
	data, err := p.Download(ctx, rs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, body, err := Prepare(data, mustLang(t, "en"), 0, 0); err != nil || !strings.Contains(body, "Hello") {
		t.Fatal(body, err)
	}
}

func TestGestdown(t *testing.T) {
	ctx := context.Background()
	locked := false
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/shows/search/"):
			reply(w, obj{"shows": []obj{{"id": "other", "tmdbId": 1}, {"id": "got", "tmdbId": 1399}}})
		case r.URL.Path == "/subtitles/get/got/1/2/en":
			if locked {
				w.WriteHeader(http.StatusLocked)
				return
			}
			reply(w, obj{"matchingSubtitles": []obj{
				{"subtitleId": "s1", "version": "KILLERS", "completed": true, "hearingImpaired": true,
					"downloadUri": "/subtitles/download/s1", "downloadCount": 99},
				{"subtitleId": "s2", "version": "WIP", "completed": false, "downloadUri": "/subtitles/download/s2"},
			}})
		case r.URL.Path == "/subtitles/download/s1":
			_, _ = io.WriteString(w, srt)
		default:
			http.NotFound(w, r)
		}
	})
	g := NewGestdown(testWeb, f.URL)
	if rs, err := g.Search(ctx, film(t)); err != nil || rs != nil {
		t.Fatal("films are not asked of Gestdown")
	}
	rs, err := g.Search(ctx, episode(t))
	if err != nil || len(rs) != 1 || rs[0].Release != "KILLERS" || rs[0].Downloads != 99 || !rs[0].HearingImpaired {
		t.Fatalf("%+v %v", rs, err)
	}
	if body, err := g.Download(ctx, rs[0]); err != nil || string(body) != srt {
		t.Fatal(err)
	}
	locked = true
	if _, err := g.Search(ctx, episode(t)); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatal(err)
	}
	if _, err := g.Download(ctx, Result{Ref: "http://elsewhere/x"}); err == nil {
		t.Fatal("a download outside Gestdown's paths")
	}
}

func TestASiteThatIsDownIsAProblemNotAPanic(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := NewPodnapisi(testWeb, f.URL).Search(context.Background(), film(t))
	var p *Problem
	if !errors.As(err, &p) || p.Error() != "Podnapisi is having trouble right now" {
		t.Fatal(err)
	}
}

func TestStatusesAreWordedByWhatTheyMean(t *testing.T) {
	cases := []struct {
		code int
		body string
		want string
	}{
		{401, ``, "refused the install's key"},
		{403, `{"status":false,"error":"invalid_api_key","message":"Invalid API key"}`, "refused the install's key"},
		{403, `{"error":"Forbidden"}`, "refused the install's key"},
		// A bare 403 is a firewall's page, not a verdict on the key.
		{403, `<html>Attention Required! | Cloudflare</html>`, "refused the request (HTTP 403)"},
		{429, `{"error":"rate_limit"}`, "is busy, try again in a minute"},
		{404, `{"error":"subtitle_not_found"}`, "has no such subtitle"},
		{502, ``, "is having trouble right now"},
		{418, ``, "answered HTTP 418"},
	}
	for _, c := range cases {
		err := failure("subdl", &errStatus{Code: c.code, Body: []byte(c.body)})
		var p *Problem
		if !errors.As(err, &p) || p.Msg != c.want || p.Status != c.code {
			t.Errorf("%d %s: %v", c.code, c.body, err)
		}
	}
	// The log gets the site's code and reason, never the rest of a body.
	p := asProblem("subdl", failure("subdl", &errStatus{Code: 429, Body: []byte(`{"error":"rate_limit","message":"Slow down","film":"Secret title"}`)}))
	if p.Detail != "rate_limit Slow down" {
		t.Fatal(p.Detail)
	}
	if p := asProblem("subdl", failure("subdl", &errStatus{Code: 403, Body: []byte("<html>")})); p.Detail != "not JSON, 6 bytes" {
		t.Fatal(p.Detail)
	}
}

func TestSubDLDownloadLimits(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	hits := map[string]int{}
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		if r.URL.RawQuery != "" {
			w.WriteHeader(http.StatusBadRequest) // the download carries no key or query
			return
		}
		switch r.URL.Path {
		case "/subtitle/daily.zip":
			w.WriteHeader(http.StatusTooManyRequests)
			reply(w, obj{"status": false, "error": "daily_limit"})
		case "/subtitle/busy-once.zip":
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				reply(w, obj{"status": false, "error": "service_busy"})
				return
			}
			_, _ = io.WriteString(w, srt)
		case "/subtitle/busy.zip":
			w.WriteHeader(http.StatusTooManyRequests)
			reply(w, obj{"status": false, "error": "rate_limit"})
		case "/subtitle/waf.zip":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<html>blocked</html>")
		}
	})
	s := NewSubDL(testWeb, f.URL, f.URL, "k")
	s.busyWait = 50 * time.Millisecond
	var p *Problem
	if _, err := s.Download(ctx, Result{Ref: "/subtitle/daily.zip"}); !errors.As(err, &p) || !p.Quota || p.Msg != "has no downloads left today" {
		t.Fatalf("daily: %v", err)
	}
	if hits["/subtitle/daily.zip"] != 1 {
		t.Fatal("a daily limit was tried again")
	}
	start := time.Now()
	if body, err := s.Download(ctx, Result{Ref: "/subtitle/busy-once.zip"}); err != nil || string(body) != srt {
		t.Fatalf("busy once: %v", err)
	}
	if time.Since(start) < s.busyWait {
		t.Fatal("didn't wait before trying again")
	}
	if _, err := s.Download(ctx, Result{Ref: "/subtitle/busy.zip"}); !errors.As(err, &p) || p.Msg != "is busy, try again in a minute" || hits["/subtitle/busy.zip"] != 2 {
		t.Fatalf("busy: %v, %d tries", err, hits["/subtitle/busy.zip"])
	}
	if _, err := s.Download(ctx, Result{Ref: "/subtitle/waf.zip"}); !errors.As(err, &p) || p.Msg != "refused the request (HTTP 403)" || p.Status != 403 {
		t.Fatalf("403: %v", err)
	}
}
