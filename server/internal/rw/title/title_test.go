package title_test

// Behaviour tests of a title's page and everything done on it, ported from
// the Python's tests/test_titles.py, test_outside_services.py (scores,
// Seerr) and test_no_caps.py (the title parts), against a real Postgres and
// Valkey with the outside services faked over HTTP.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

type testenvClient = testenv.Client

type env struct {
	s          *testenv.Server
	f          *testenv.Fakes
	alice, bob *testenv.Client
}

func start(t *testing.T) *env {
	t.Helper()
	return startWith(t, testbase.BaseEnv(t))
}

// startWith is start with the given settings (DRY_RUN=false, say, for a
// test of what reaches Jellyfin: the fakes' default DRY_RUN sends nothing).
func startWith(t *testing.T, base testbase.Env) *env {
	t.Helper()
	s, f := testenv.StartFaked(t, base)
	return &env{s: s, f: f, alice: s.Client("alice", true), bob: s.Client("bob", true)}
}

// call sends a request and decodes the JSON answer, failing on any status
// but want.
func call(t *testing.T, c *testenv.Client, want int, method, path string, body ...string) map[string]any {
	t.Helper()
	var b any
	if len(body) > 0 {
		b = body[0]
	}
	r := c.Do(t, method, path, b)
	if r.StatusCode != want {
		t.Fatalf("%s %s: %d (want %d) %s", method, path, r.StatusCode, want, r.Body)
	}
	if r.Body == "" {
		return nil
	}
	return r.Map(t)
}

func ok(t *testing.T, c *testenv.Client, method, path string, body ...string) map[string]any {
	t.Helper()
	return call(t, c, http.StatusOK, method, path, body...)
}

func TestTheTitlePage(t *testing.T) {
	e := start(t)
	d := ok(t, e.alice, "GET", "/api/v1/title/movie/603")
	if d["title"] != "Movie 603" || d["certification"] != "R" || !strings.HasSuffix(d["trailer"].(string), "v=abc") {
		t.Fatal(d)
	}
	if fmt.Sprint(d["directors"]) != "[Dee Rector]" || d["director_label"] != "Directed by" {
		t.Fatal(d["directors"])
	}
	stream := d["providers"].(map[string]any)["stream"].([]any)
	if stream[0].(map[string]any)["name"] != "Netflix" {
		t.Fatal(stream)
	}
	c := d["community"].(map[string]any)
	if c["average"] != nil || c["count"] != 0.0 || c["mine"] != nil || len(c["others"].([]any)) != 0 {
		t.Fatal(c)
	}
	// Lists are arrays, never null, even when empty.
	for _, k := range []string{"seasons", "watches", "lists", "writers", "studios", "networks", "languages"} {
		if _, isList := d[k].([]any); !isList {
			t.Errorf("%s: %v", k, d[k])
		}
	}
	show := ok(t, e.alice, "GET", "/api/v1/title/tv/1399")
	if show["director_label"] != "Created by" || fmt.Sprint(show["directors"]) != "[Cree Ator]" || len(show["seasons"].([]any)) != 3 {
		t.Fatal(show)
	}
}

func TestAnUnknownTitleIs404AndTMDBTroubleIs502(t *testing.T) {
	e := start(t)
	call(t, e.alice, 404, "GET", "/api/v1/title/movie/9999999")
	e.f.TMDB.Fail("/movie/604", 500)
	r := call(t, e.alice, 502, "GET", "/api/v1/title/movie/604")
	if r["detail"] != "Couldn't reach TMDB for this title" {
		t.Fatal(r)
	}
	call(t, e.alice, 422, "GET", "/api/v1/title/book/1")
}

func TestATitlePageListsEveryCredit(t *testing.T) {
	e := start(t)
	e.f.TMDB.Override("/movie/603", func(w http.ResponseWriter, _ *http.Request) {
		var cast, crew, studios, langs, services []fakes.JSON
		for n := 1; n <= 40; n++ {
			cast = append(cast, fakes.JSON{"id": n, "name": fmt.Sprintf("Actor %d", n)})
		}
		for n := 0; n < 5; n++ {
			crew = append(crew, fakes.JSON{"job": "Director", "name": fmt.Sprintf("Director %d", n)})
			langs = append(langs, fakes.JSON{"english_name": fmt.Sprintf("Language %d", n)})
		}
		for n := 0; n < 6; n++ {
			crew = append(crew, fakes.JSON{"department": "Writing", "name": fmt.Sprintf("Writer %d", n)})
		}
		for n := 0; n < 7; n++ {
			studios = append(studios, fakes.JSON{"name": fmt.Sprintf("Studio %d", n)})
		}
		for n := 0; n < 9; n++ {
			services = append(services, fakes.JSON{"provider_id": n, "provider_name": fmt.Sprintf("Service %d", n)})
		}
		m := fakes.Movie(603)
		m["credits"] = fakes.JSON{"cast": cast, "crew": crew}
		m["production_companies"], m["spoken_languages"] = studios, langs
		m["watch/providers"] = fakes.JSON{"results": fakes.JSON{"US": fakes.JSON{"flatrate": services}}}
		fakes.Reply(w, m)
	})
	d := ok(t, e.alice, "GET", "/api/v1/title/movie/603")
	for k, n := range map[string]int{"cast": 40, "directors": 5, "writers": 6, "studios": 7, "languages": 5} {
		if got := len(d[k].([]any)); got != n {
			t.Errorf("%s: %d", k, got)
		}
	}
	if n := len(d["providers"].(map[string]any)["stream"].([]any)); n != 9 {
		t.Error("services", n)
	}
}

func TestWatchlistFavoriteAndRatingArePerPerson(t *testing.T) {
	e := start(t)
	if ok(t, e.alice, "POST", "/api/v1/title/movie/603/watchlist")["watchlist"] != true {
		t.Fatal("watchlist")
	}
	if ok(t, e.alice, "POST", "/api/v1/title/movie/603/favorite")["favorite"] != true {
		t.Fatal("favorite")
	}
	r := e.alice.Do(t, "POST", "/api/v1/title/movie/603/rating", `{"rating": 9, "review": "Holds up."}`)
	if r.Body != `{"ok":true,"rating":9,"review":"Holds up."}` {
		t.Fatal(r.Body)
	}
	st := ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	if st["watchlist"] != true || st["favorite"] != true || st["rating"] != 9.0 {
		t.Fatal(st)
	}
	stb := ok(t, e.bob, "GET", "/api/v1/title/movie/603/status")
	if stb["watchlist"] != false || stb["favorite"] != false || stb["rating"] != nil {
		t.Fatal(stb)
	}
	ok(t, e.alice, "DELETE", "/api/v1/title/movie/603/watchlist")
	ok(t, e.alice, "DELETE", "/api/v1/title/movie/603/favorite")
	if r := ok(t, e.alice, "DELETE", "/api/v1/title/movie/603/rating"); r["rating"] != nil {
		t.Fatal(r)
	}
	st = ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	if st["watchlist"] != false || st["favorite"] != false || st["rating"] != nil {
		t.Fatal(st)
	}
}

func TestMarksAtOnceNeverFail(t *testing.T) {
	// Two devices (or a phone's outbox replaying) adding and removing the
	// same mark at once: each is one statement, so none fails.
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603/status") // the person exists before the burst
	var wg sync.WaitGroup
	codes := make([]int, 30)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths := []string{"/watchlist", "/favorite"}
			method := "POST"
			if i%3 == 1 {
				method = "DELETE"
			}
			codes[i] = e.alice.Do(t, method, "/api/v1/title/movie/603"+paths[i%2], nil).StatusCode
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("call %d: %d", i, c)
		}
	}
}

func TestEveryonesRatingsAndCommentsShow(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "POST", "/api/v1/title/movie/603/rating", `{"rating": 9, "review": "Holds up."}`)
	ok(t, e.bob, "POST", "/api/v1/title/movie/603/rating", `{"rating": 6}`)
	c := ok(t, e.bob, "GET", "/api/v1/title/movie/603")["community"].(map[string]any)
	if c["average"] != 7.5 || c["count"] != 2.0 {
		t.Fatal(c)
	}
	mine := c["mine"].(map[string]any)
	other := c["others"].([]any)[0].(map[string]any)
	if mine["rating"] != 6.0 || mine["mine"] != true || other["name"] != "alice" || other["review"] != "Holds up." {
		t.Fatal(c)
	}
}

func TestEveryonesRatingsAreListedNoCap(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	e.s.Exec(t, `INSERT INTO users (username, display_name, is_admin, created_at)
		SELECT 'viewer' || n, 'Viewer ' || n, false, now() FROM generate_series(1, 60) n`)
	e.s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source)
		SELECT id, 'movie', 603, 7, now(), 'app' FROM users WHERE username LIKE 'viewer%'`)
	c := ok(t, e.alice, "GET", "/api/v1/title/movie/603")["community"].(map[string]any)
	if c["count"] != 60.0 || len(c["others"].([]any)) != 60 {
		t.Fatal(c["count"])
	}
}

func TestARatingWithoutACommentKeepsTheComment(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "POST", "/api/v1/title/movie/603/rating", `{"rating": 9, "review": "Holds up."}`)
	r := ok(t, e.alice, "POST", "/api/v1/title/movie/603/rating", `{"rating": 8}`)
	if r["rating"] != 8.0 || r["review"] != "Holds up." {
		t.Fatal(r)
	}
	// A blank comment clears it.
	if r := ok(t, e.alice, "POST", "/api/v1/title/movie/603/rating", `{"rating": 8, "review": "  "}`); r["review"] != nil {
		t.Fatal(r)
	}
}

func TestACommentTooLongIsRefusedInPlainWords(t *testing.T) {
	e := start(t)
	r := call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/rating", fmt.Sprintf(`{"rating": 8, "review": %q}`, strings.Repeat("x", 5001)))
	if r["detail"] != "Your comment is too long: at most 5,000 characters." {
		t.Fatal(r)
	}
	for _, bad := range []string{`{"rating": 0}`, `{"rating": 11}`, `{}`} {
		call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/rating", bad)
	}
}

func TestTheCommentLimitIsTheSetting(t *testing.T) {
	ev := testbase.BaseEnv(t)
	ev["TEXT_MAX_CHARS"] = "10"
	s, _ := testenv.StartFaked(t, ev)
	r := call(t, s.Client("alice", true), 422, "POST", "/api/v1/title/movie/603/rating", `{"rating": 8, "review": "eleven chars"}`)
	if r["detail"] != "Your comment is too long: at most 10 characters." {
		t.Fatal(r)
	}
}

func TestWatchedWithADateAndUnwatch(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "POST", "/api/v1/title/movie/603/watchlist")
	w := ok(t, e.alice, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "2026-09-30"}`)
	watches := w["watches"].([]any)
	first := watches[0].(map[string]any)
	// A bare date is that evening in the install's zone (America/New_York).
	if len(watches) != 1 || first["watched_at"] != "2026-09-30T20:00:00-04:00" || w["id"] == nil {
		t.Fatal(w)
	}
	if fmt.Sprint(first["sources"]) != "[manual]" || first["key"] != "movie:603" || first["title"] != "Movie 603" {
		t.Fatal(first)
	}
	// Watched: off the watchlist.
	if ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")["watchlist"] != false {
		t.Fatal("still on the watchlist")
	}
	future := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	if r := call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "`+future+`"}`); r["detail"] != "watched_at is in the future" {
		t.Fatal(r)
	}
	if r := call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "1899-12-31"}`); r["detail"] != "watched_at is too early" {
		t.Fatal(r)
	}
	if r := call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "last tuesday"}`); r["detail"] != "watched_at must be an ISO date" {
		t.Fatal(r)
	}
	left := ok(t, e.alice, "POST", "/api/v1/title/movie/603/unwatch", fmt.Sprintf(`{"ids": %v}`, first["ids"]))
	if len(left["watches"].([]any)) != 0 {
		t.Fatal(left)
	}
}

func TestTheDayOfAWatchIsTheInstallsDay(t *testing.T) {
	// A manual mark at 23:30 New York time and a Jellyfin play of the same
	// film reported at 03:00 UTC the next day are one evening's watch.
	e := start(t)
	ok(t, e.alice, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "2026-10-01T23:30:00-04:00"}`)
	e.s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT id, 'movie', 603, 'Movie 603', '2026-10-02T03:45:00Z', 'jellyfin' FROM users WHERE username = 'alice'`)
	w := ok(t, e.alice, "POST", "/api/v1/title/movie/603/unwatch", `{"ids": [999999]}`)["watches"].([]any)
	if len(w) != 1 {
		t.Fatalf("%v", w)
	}
	one := w[0].(map[string]any)
	// Newest first: the Jellyfin play came in after the manual mark.
	if len(one["ids"].([]any)) != 2 || fmt.Sprint(one["sources"]) != "[jellyfin manual]" || one["watched_at"] != "2026-10-01T23:45:00-04:00" {
		t.Fatal(one)
	}
}

func TestSomeoneElsesWatchCannotBeRemoved(t *testing.T) {
	e := start(t)
	w := ok(t, e.alice, "POST", "/api/v1/title/movie/603/watched", `{}`)
	ids := w["watches"].([]any)[0].(map[string]any)["ids"]
	ok(t, e.bob, "POST", "/api/v1/title/movie/603/unwatch", fmt.Sprintf(`{"ids": %v}`, ids))
	// Nor through another title of one's own.
	ok(t, e.alice, "POST", "/api/v1/title/movie/604/unwatch", fmt.Sprintf(`{"ids": %v}`, ids))
	if left := ok(t, e.alice, "POST", "/api/v1/title/movie/603/unwatch", `{"ids": [999999]}`); len(left["watches"].([]any)) != 1 {
		t.Fatal(left)
	}
	call(t, e.alice, 422, "POST", "/api/v1/title/movie/603/unwatch", `{"ids": []}`)
}

func TestARequestInDryRun(t *testing.T) {
	e := start(t)
	r := ok(t, e.alice, "POST", "/api/v1/title/movie/550/request")
	if r["state"].(map[string]any)["request"] != "dry_run" || r["already"] != false {
		t.Fatal(r)
	}
	var n int
	_ = e.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM requests WHERE tmdb_id = 550 AND dry_run`).Scan(&n)
	if n != 1 {
		t.Fatal("no request recorded", n)
	}
	// Seasons are taken for shows; their count is a setting.
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/request", `{"seasons": [1, 2]}`)
	call(t, e.alice, 422, "POST", "/api/v1/title/tv/1399/request", `{"seasons": [-1]}`)
}

func TestARequestForRealGoesToSeerrAsThePerson(t *testing.T) {
	ev := testbase.BaseEnv(t)
	ev["DRY_RUN"] = "false"
	s, f := testenv.StartFaked(t, ev)
	alice, owner := s.Client("alice", true), s.Client("owner", true)
	// Without a Jellyfin account (so no Seerr user of her own): refused, in words.
	if r := call(t, alice, 409, "POST", "/api/v1/title/movie/550/request"); !strings.Contains(r["detail"].(string), "Connect your Jellyfin account") {
		t.Fatal(r)
	}
	// The cards' shared Seerr map is warm (pulled once, before the request).
	ok(t, owner, "GET", "/api/v1/title/movie/550/status")
	if _, err := s.App.RW.State.Load(context.Background(), store.User{ID: 2}, []string{"movie:550"}); err != nil {
		t.Fatal(err)
	}
	// The install's admins may request as the API key's user.
	f.Seerr.SetInfo("movie:550", fakes.JSON{"status": 2, "requests": []fakes.JSON{{"createdAt": "2026-11-01T10:00:00.000Z"}}})
	r := ok(t, owner, "POST", "/api/v1/title/movie/550/request")
	st := r["state"].(map[string]any)
	if st["request"] != "pending" || st["requested_at"] != "2026-11-01T10:00:00.000Z" {
		t.Fatal(r)
	}
	if f.Seerr.Count("POST /api/v1/request") != 1 {
		t.Fatal(f.Seerr.Calls())
	}
	// Every card shows it at once (the shared map was updated).
	st2, err := s.App.RW.State.Load(context.Background(), store.User{ID: 2}, []string{"movie:550"})
	if err != nil || st2.Of("movie", 550).Request == nil {
		t.Fatal("the shared Seerr map does not have the new request", err)
	}
}

func TestSeerrStatusIsAskedOncePerWindowWhateverThePolling(t *testing.T) {
	e := start(t)
	e.f.Seerr.SetInfo("movie:603", fakes.JSON{"status": 3, "downloadStatus": []fakes.JSON{{"size": 100, "sizeLeft": 40}}})
	for i := 0; i < 10; i++ {
		c := e.alice
		if i%2 == 1 {
			c = e.bob
		}
		st := ok(t, c, "GET", "/api/v1/title/movie/603/status")
		if st["request"] != "processing" || st["progress"].(map[string]any)["percent"] != 60.0 {
			t.Fatal(st)
		}
	}
	if n := e.f.Seerr.Count("GET /api/v1/movie/603"); n != 1 {
		t.Fatalf("Seerr asked %d times for ten polls", n)
	}
}

func TestWhenSeerrStopsAnsweringTheButtonKeepsItsLastState(t *testing.T) {
	e := start(t)
	e.f.Seerr.SetInfo("movie:603", fakes.JSON{"status": 3})
	ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	e.f.Seerr.Fail("/api/v1/movie/603", 500)
	time.Sleep(5100 * time.Millisecond) // past the shared window
	if st := ok(t, e.alice, "GET", "/api/v1/title/movie/603/status"); st["request"] != "processing" {
		t.Fatal(st)
	}
}

func TestTheLibrarysTitleState(t *testing.T) {
	e := start(t)
	e.s.SeedLibrary(t)
	st := ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	if st["in_library"] != true || !strings.HasSuffix(st["jellyfin_url"].(string), strings.Repeat("a", 32)) {
		t.Fatal(st)
	}
}

func TestRelatedTitlesAndTheCollection(t *testing.T) {
	e := start(t)
	e.f.TMDB.Override("/movie/603", func(w http.ResponseWriter, r *http.Request) {
		m := fakes.Movie(603)
		m["belongs_to_collection"] = fakes.JSON{"id": 2344, "name": "The Matrix Collection"}
		fakes.Reply(w, m)
	})
	ok(t, e.alice, "POST", "/api/v1/title/movie/604/watchlist")
	r := ok(t, e.alice, "GET", "/api/v1/title/movie/603/related")
	recs := r["recommendations"].([]any)
	if len(recs) != 5 || recs[0].(map[string]any)["key"] != "movie:604" || recs[0].(map[string]any)["watchlist"] != true {
		t.Fatal(recs)
	}
	coll := r["collection"].(map[string]any)
	if coll["name"] != "The Matrix Collection" || len(coll["items"].([]any)) != 2 {
		t.Fatal(coll)
	}
	// TMDB down for recommendations: the section is empty, the page still answers.
	e.f.TMDB.Fail("/tv/1399/recommendations", 500)
	if r := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/related"); len(r["recommendations"].([]any)) != 0 || r["collection"] != nil {
		t.Fatal(r)
	}
}

func TestTheTitlePageWritesTheTitleCacheOnlyWhenItChanges(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603")
	var at1, at2 time.Time
	_ = e.s.Pool.QueryRow(context.Background(), `SELECT fetched_at FROM titles WHERE key = 'movie:603' AND detailed`).Scan(&at1)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603")
	_ = e.s.Pool.QueryRow(context.Background(), `SELECT fetched_at FROM titles WHERE key = 'movie:603'`).Scan(&at2)
	if at1.IsZero() || !at1.Equal(at2) {
		t.Fatal(at1, at2)
	}
}

func TestTheResumePoint(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	e.s.Exec(t, `INSERT INTO watch_progress (user_id, key, media_type, tmdb_id, title, position_s, duration_s, state, client, updated_at)
		SELECT id, 'movie:603', 'movie', 603, 'Movie 603', 200000, 3000000, 'paused', $1, now() FROM users WHERE username = 'alice'`,
		strings.Repeat("C", 300))
	r := ok(t, e.alice, "GET", "/api/v1/title/movie/603")["resume"].(map[string]any)
	if r["position_s"] != 200000.0 || r["percent"] != 7.0 || r["client"] != strings.Repeat("C", 300) {
		t.Fatal(r)
	}
}

func TestTheListsATitleCanGoIn(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/movie/603/status")
	e.s.Exec(t, `INSERT INTO lists (id, name, kind, source, created_at, owner_id) SELECT 1, 'Mine', 'list', 'app', now(), id FROM users WHERE username = 'alice'`)
	e.s.Exec(t, `INSERT INTO lists (id, name, kind, source, created_at, owner_id) VALUES (2, 'Curated', 'list', 'copy', now(), NULL)`)
	e.s.Exec(t, `INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at) VALUES (1, 'movie', 603, 1, now())`)
	l := ok(t, e.alice, "GET", "/api/v1/title/movie/603/lists")["lists"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["in_list"] != true || l[0].(map[string]any)["name"] != "Mine" {
		t.Fatal(l)
	}
	if l := ok(t, e.bob, "GET", "/api/v1/title/movie/603/lists")["lists"].([]any); len(l) != 0 {
		t.Fatal(l)
	}
}

func TestTitleWatchesUseTheirIndex(t *testing.T) {
	e := start(t)
	tmdb := int32(603)
	plan := e.s.Explain(t, store.TitleWatchesSQL, int32(1), &tmdb, "movie", "America/New_York")
	// Which index the planner takes on a handful of rows is its choice; a
	// whole-table scan would be the defect.
	if strings.Contains(plan, "Seq Scan on history") || !strings.Contains(plan, "Index") {
		t.Fatal(plan)
	}
	plan = e.s.Explain(t, store.CommunitySQL, "movie", int32(603))
	if strings.Contains(plan, "Seq Scan on user_ratings") {
		t.Fatal(plan)
	}
}
