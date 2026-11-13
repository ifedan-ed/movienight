package home_test

// Home, its rows, "See all", Discover and "Picked for you".

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/rows"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

func get(t *testing.T, c *testenv.Client, path string) map[string]any {
	t.Helper()
	r := c.Do(t, http.MethodGet, path, nil)
	if r.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, r.StatusCode, r.Body)
	}
	return r.Map(t)
}

func userID(t *testing.T, s *testenv.Server, name string) int32 {
	t.Helper()
	var id int32
	if err := s.Pool.QueryRow(context.Background(), `SELECT id FROM users WHERE username = $1`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func items(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["items"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestTheHomeTabListsItsRowsInOrder(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	h := get(t, alice, "/api/v1/home")
	rows := h["rows"].([]any)
	first := rows[0].(map[string]any)
	if h["tab"] != "for_you" || first["key"] != "continue" || first["style"] != "landscape" || first["title"] != "Up next" {
		t.Fatal(first)
	}
	var keys []string
	for _, r := range rows {
		row := r.(map[string]any)
		keys = append(keys, row["key"].(string))
		if row["key"] == "because" && row["multi"] != true {
			t.Fatal(row)
		}
		if row["key"] == "trending" && row["title"] != "Trending now" {
			t.Fatal(row)
		}
		if row["key"] == "provider:8" && row["title"] != "Popular on Netflix" {
			t.Fatal(row)
		}
	}
	if len(keys) != 14 {
		t.Fatal(keys)
	}
	shows := get(t, alice, "/api/v1/home?tab=shows")["rows"].([]any)
	for _, r := range shows {
		if row := r.(map[string]any); row["key"] == "genre:10765" && row["title"] != "Sci-Fi & Fantasy" {
			t.Fatal(row)
		}
	}
	// Turning a row off, and an unknown key, through the Home rows screen.
	r := alice.Do(t, "PUT", "/api/v1/home/config?tab=movies", `{"rows": [{"key": "popular", "on": true}, {"key": "nonsense", "on": true}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.Body)
	}
	cfg := r.Map(t)["rows"].([]any)
	if cfg[0].(map[string]any)["key"] != "popular" || cfg[1].(map[string]any)["on"] != false {
		t.Fatal(cfg[:2])
	}
	if got := get(t, alice, "/api/v1/home?tab=movies")["rows"].([]any); len(got) != 1 {
		t.Fatal(got)
	}
	if r := alice.Do(t, "POST", "/api/v1/home/config/reset?tab=all", ""); r.StatusCode != 200 {
		t.Fatal(r.Body)
	}
	if got := get(t, alice, "/api/v1/home?tab=movies")["rows"].([]any); len(got) != 21 {
		t.Fatal(len(got))
	}
}

func TestGenresAndProviders(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	g := get(t, alice, "/api/v1/genres?media=tv")["genres"].([]any)
	if g[0].(map[string]any)["name"] != "Action & Adventure" || len(g) != 16 {
		t.Fatal(g)
	}
	p := get(t, alice, "/api/v1/providers")["providers"].([]any)
	if p[0].(map[string]any)["name"] != "Netflix" || len(p) != 8 {
		t.Fatal(p)
	}
}

func TestATMDBGridThatReachesTMDBsLastPageSaysSo(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	before := get(t, alice, "/api/v1/browse?key=popular&page=499")
	if before["more"] != true || before["note"] != nil || before["title"] != "Popular" {
		t.Fatal(before)
	}
	last := get(t, alice, "/api/v1/browse?key=popular&page=500")
	if len(items(last)) == 0 || last["more"] != false || !strings.HasPrefix(last["note"].(string), "TMDB lists only the first 10,000 titles") {
		t.Fatal(last)
	}
	d := get(t, alice, "/api/v1/discover?page=500")
	if d["more"] != false || !strings.Contains(d["note"].(string), "10,000") {
		t.Fatal(d)
	}
	if mine := get(t, alice, "/api/v1/browse?key=watchlist&page=500"); mine["note"] != nil {
		t.Fatal(mine)
	}
	if r := alice.Do(t, "GET", "/api/v1/browse?key=Bad-Key", nil); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
}

func TestATMDBPageThatDoesntComeSaysSoInsteadOfEndingTheGrid(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	s.SeedLibrary(t)
	if len(items(get(t, alice, "/api/v1/browse?key=popular&page=2"))) == 0 {
		t.Fatal("no page 2")
	}
	for _, p := range []string{"/discover/movie", "/discover/tv", "/trending/all/week", "/trending/movie/week", "/movie/603/recommendations"} {
		f.TMDB.Fail(p, 503)
	}
	for _, q := range []string{"key=popular&page=3", "key=trending&page=2", "key=upcoming", "key=recs:movie:603&page=2"} {
		r := alice.Do(t, "GET", "/api/v1/browse?"+q, nil)
		if r.StatusCode != 502 || r.Map(t)["detail"] != rows.TMDBDownText {
			t.Fatalf("%s: %d %s", q, r.StatusCode, r.Body)
		}
	}
	if r := alice.Do(t, "GET", "/api/v1/discover?page=4", nil); r.StatusCode != 502 || r.Map(t)["detail"] != rows.TMDBDownText {
		t.Fatal(r.Body)
	}
	if row := alice.Do(t, "GET", "/api/v1/rows/popular", nil); row.Body != `{"rows":[],"note":"`+rows.TMDBDownRow+`"}` {
		t.Fatal(row.Body)
	}
	// The banner goes on with the library alone.
	hero := get(t, alice, "/api/v1/hero")
	if len(items(hero)) != 2 || items(hero)[0]["hero_label"] != "New in your library" {
		t.Fatal(hero)
	}
	// A page TMDB sent before is still shown from the kept copy.
	if len(items(get(t, alice, "/api/v1/browse?key=popular&page=2"))) == 0 {
		t.Fatal("the kept copy is not served")
	}
}

func TestTheHeroShowsTheLibrarysNewestThenTrending(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	s.SeedLibrary(t)
	got := items(get(t, alice, "/api/v1/hero?tab=for_you"))
	if len(got) != 7 || got[0]["key"] != "tv:1399" || got[1]["key"] != "movie:603" || got[2]["hero_label"] != "Trending" {
		t.Fatal(got)
	}
	if got[0]["overview"] != "About show 1399." || len(got[0]["genres"].([]any)) == 0 || got[0]["in_library"] != true {
		t.Fatal(got[0])
	}
}

func TestPersonalRowsArePagedAndTheyAreThePersonsOwn(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice, bob := s.Client("alice", true), s.Client("bob", true)
	get(t, alice, "/api/v1/home")
	get(t, bob, "/api/v1/home")
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source)
		SELECT u.id, 'movie', 600 + n, now() - make_interval(mins => n), 'app' FROM users u, generate_series(1, 45) n WHERE u.username = 'alice'`)
	p1 := get(t, alice, "/api/v1/browse?key=watchlist")
	if len(items(p1)) != 40 || p1["more"] != true || items(p1)[0]["key"] != "movie:601" || items(p1)[0]["watchlist"] != true {
		t.Fatal(p1)
	}
	p2 := get(t, alice, "/api/v1/browse?key=watchlist&page=2")
	if len(items(p2)) != 5 || p2["more"] != false {
		t.Fatal(p2)
	}
	if b := get(t, bob, "/api/v1/browse?key=watchlist"); len(items(b)) != 0 {
		t.Fatal("bob sees alice's watchlist")
	}
	row := get(t, alice, "/api/v1/rows/watchlist")["rows"].([]any)[0].(map[string]any)
	if len(row["items"].([]any)) != rows.RowSize || row["more"].(map[string]any)["key"] != "watchlist" || row["title"] != "Plan to watch" {
		t.Fatal(row)
	}
}

func TestUnwatchedIsTheLibraryNotYetWatchedInADailyOrder(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	s.SeedLibrary(t)
	get(t, alice, "/api/v1/home")
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source)
		SELECT id, 'episode', 1399, 1, 1, 'Show 1399', now(), 'manual' FROM users WHERE username = 'alice'`)
	got := items(get(t, alice, "/api/v1/browse?key=unwatched"))
	if len(got) != 1 || got[0]["key"] != "movie:603" {
		t.Fatal(got)
	}
	plan := s.Explain(t, store.UnwatchedLibraryPageSQL, "", int32(1), "2026-11-12", int32(0), int32(40))
	if strings.Contains(plan, "Seq Scan on history") {
		t.Fatal(plan)
	}
}

func TestUpNextShowsEveryItemOnePerShow(t *testing.T) {
	// The old 20 in progress and 20 next up are gone: every one.
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/home")
	s.Exec(t, `UPDATE users SET jellyfin_user_id = $1 WHERE username = 'alice'`, strings.Repeat("f", 32))
	for n := 1; n <= 26; n++ {
		f.Jellyfin.Resume = append(f.Jellyfin.Resume, fakes.JSON{"Id": fmt.Sprintf("%032x", n), "Type": "Movie", "Name": fmt.Sprintf("Movie %d", n),
			"ProviderIds": fakes.JSON{"Tmdb": fmt.Sprint(n)}, "UserData": fakes.JSON{"PlayedPercentage": 40.4}, "RunTimeTicks": 72_000_000_000})
	}
	for n := 0; n < 30; n++ {
		f.Jellyfin.NextUp = append(f.Jellyfin.NextUp, fakes.JSON{"Id": fmt.Sprintf("%032x", 1000+n), "Type": "Episode", "Name": fmt.Sprintf("Episode %d", n),
			"SeriesId": fmt.Sprintf("%032x", 5000+n), "SeriesName": fmt.Sprintf("Show %d", n), "ParentIndexNumber": 1, "IndexNumber": 2})
	}
	// A second next-up episode of the first show is left out (one per show).
	f.Jellyfin.NextUp = append(f.Jellyfin.NextUp, fakes.JSON{"Id": fmt.Sprintf("%032x", 2000), "Type": "Episode", "SeriesId": fmt.Sprintf("%032x", 5000), "SeriesName": "Show 0"})
	row := get(t, alice, "/api/v1/rows/continue")["rows"].([]any)[0].(map[string]any)
	got := row["items"].([]any)
	if row["style"] != "landscape" || len(got) != 26+30 {
		t.Fatal(len(got), row["style"])
	}
	m := got[0].(map[string]any)
	if m["key"] != "movie:1" || m["progress"] != 40.0 || m["runtime_min"] != 120.0 || m["poster"] != "/p1.jpg" {
		t.Fatal(m)
	}
	ep := got[26].(map[string]any)
	if ep["title"] != "Show 0" || ep["subtitle"] != "S1 · E2 – Episode 0" || ep["key"] != nil {
		t.Fatal(ep)
	}
	// Movies tab: films only.
	if n := len(get(t, alice, "/api/v1/rows/continue?tab=movies")["rows"].([]any)[0].(map[string]any)["items"].([]any)); n != 26 {
		t.Fatal(n)
	}
}

func TestBecauseYouWatchedLooksPastALongBinge(t *testing.T) {
	// 700 episodes of one show, newest; 20 films before them. The Python's
	// read of the 600 newest rows found one title; all 15 seeds are found.
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/home")
	uid := userID(t, s, "alice")
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT $1, 'movie', 900 + n, 'Movie ' || (900 + n), '2025-01-01'::timestamptz + make_interval(hours => n), 'manual' FROM generate_series(0, 19) n`, uid)
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source)
		SELECT $1, 'episode', 1399, 1 + n / 10, n % 10 + 1, 'Show 1399', '2025-01-31'::timestamptz + make_interval(mins => n), 'manual' FROM generate_series(0, 699) n`, uid)
	// A film rated 5/10 or lower doesn't drive the row.
	s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source) VALUES ($1, 'movie', 919, 4, now(), 'app')`, uid)
	seeds, err := s.App.RW.Q.RecentSeeds(context.Background(), store.RecentSeedsParams{UserID: uid, Lim: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 15 || seeds[0].Media != "tv" || seeds[0].TmdbID != 1399 || seeds[1].TmdbID != 918 {
		t.Fatal(seeds)
	}
	got := get(t, alice, "/api/v1/rows/because")["rows"].([]any)
	// Most seeds' recommendations (the next five ids) were all watched:
	// their rows are left out; a row is never one of watched titles.
	if len(got) == 0 || len(got) > 5 {
		t.Fatal(len(got))
	}
	for _, x := range got {
		for _, c := range x.(map[string]any)["items"].([]any) {
			if c.(map[string]any)["watched"] != 0.0 {
				t.Fatal("a watched title in Because you watched")
			}
		}
	}
	r0 := got[0].(map[string]any)
	if r0["kicker"] != "Because you watched" || !strings.HasPrefix(r0["id"].(string), "because:") ||
		!strings.HasPrefix(r0["more"].(map[string]any)["key"].(string), "recs:") {
		t.Fatal(r0)
	}
}

// Two films marked watched for the same moment (a day marked by hand, both
// at midnight): the one recorded last leads, as the Python ordered them
// ("watched_at DESC, id DESC"), not the lower TMDB id.
func TestSeedsWatchedAtTheSameMomentGoNewestRecordedFirst(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/home")
	uid := userID(t, s, "alice")
	for _, id := range []int{13, 51540, 7} { // recorded in this order, 7 last
		s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
			VALUES ($1, 'movie', $2, 'M', '2026-10-06T00:00:00Z', 'manual')`, uid, id)
	}
	seeds, err := s.App.RW.Q.RecentSeeds(context.Background(), store.RecentSeedsParams{UserID: uid, Lim: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 3 || seeds[0].TmdbID != 7 || seeds[1].TmdbID != 51540 || seeds[2].TmdbID != 13 {
		t.Fatal(seeds)
	}
}

func aiEnv(t *testing.T) testbase.Env {
	env := testbase.BaseEnv(t)
	env["LITELLM_API_KEY"], env["LITELLM_MODEL"], env["LITELLM_BASE_URL"] = "k", "test-model", "http://litellm.test"
	return env
}

func TestPickedForYouAsksTheWorkerAndSaysItIsBeingMade(t *testing.T) {
	s, _ := testenv.StartFaked(t, aiEnv(t))
	alice := s.Client("alice", true)
	first := get(t, alice, "/api/v1/rows/ai")
	if len(first["rows"].([]any)) != 0 || !strings.Contains(first["note"].(string), "being made") {
		t.Fatal(first)
	}
	uid := userID(t, s, "alice")
	if !aiAsked(t, s, uid) {
		t.Fatal("the worker was not asked")
	}
	// The worker gave up for now (package airows' back-off record): Home
	// says why and asks for nothing.
	until := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)
	s.KV.Do(context.Background(), s.KV.B().Del().Key(fmt.Sprintf("task:ai-rows:pending:%d", uid)).Build())
	s.KV.Do(context.Background(), s.KV.B().Set().Key(fmt.Sprintf("ai-rows:fail:%d", uid)).
		Value(fmt.Sprintf(`{"count": 1, "why": "the AI didn't answer", "until": %q}`, until)).Build())
	note := get(t, alice, "/api/v1/rows/ai")["note"].(string)
	if !strings.Contains(note, "couldn't be made just now: the AI didn't answer") || !strings.Contains(note, "in about 10 minutes") {
		t.Fatal(note)
	}
	if aiAsked(t, s, uid) {
		t.Fatal("asked again during the back-off")
	}
}

// aiAsked reports whether a rebuild of the person's AI rows is waiting for
// a worker (package airows' queue, "ai-rows").
func aiAsked(t *testing.T, s *testenv.Server, uid int32) bool {
	t.Helper()
	n, err := s.KV.Do(context.Background(), s.KV.B().Exists().Key(fmt.Sprintf("task:ai-rows:pending:%d", uid)).Build()).AsInt64()
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestPickedForYouShowsTheCachedRowsAndWhatTheAIRead(t *testing.T) {
	s, _ := testenv.StartFaked(t, aiEnv(t))
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/home")
	uid := userID(t, s, "alice")
	payload := `{"v": 4, "rows": [{"theme": "Heists", "keys": ["movie:603", "movie:604"]}, {"theme": "Space", "keys": ["movie:605"]}],
		"basis": {"films": 60, "films_total": 70, "ratings": 0, "ratings_total": 0, "comments": 0, "comments_total": 0, "comments_shortened": 0, "comment_chars": 300}}`
	s.Exec(t, `INSERT INTO ai_row_cache (user_id, generated_at, payload_json, model) VALUES ($1, now(), $2, 'test-model')`, uid, payload)
	// Watched titles are left out of the rows.
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source) VALUES ($1, 'movie', 604, 'Movie 604', now() - interval '1 day', 'manual')`, uid)
	got := get(t, alice, "/api/v1/rows/ai")
	rs := got["rows"].([]any)
	if got["note"] != nil || len(rs) != 2 {
		t.Fatal(got)
	}
	r0 := rs[0].(map[string]any)
	if r0["title"] != "Heists" || r0["kicker"] != "Picked for you" || r0["subtitle"] != "From your 60 latest of 70 films" || len(r0["items"].([]any)) != 1 {
		t.Fatal(r0)
	}
	// Fresh rows of the model in use: the worker is not asked.
	if aiAsked(t, s, uid) {
		t.Fatal("fresh rows were sent for a rebuild")
	}
}

func TestAListRowCarriesItsListAndGroup(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	r := alice.Do(t, "POST", "/api/v1/lists", `{"name": "Rainy Sunday"}`)
	lid := int(r.Map(t)["id"].(float64))
	alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "movie", "tmdb_id": 603}`)
	row := get(t, alice, fmt.Sprintf("/api/v1/rows/list:%d", lid))["rows"].([]any)[0].(map[string]any)
	if row["title"] != "Rainy Sunday" || row["subtitle"] != "Your list" || row["group"] != "mine" || row["list_id"] != float64(lid) || row["more"] != nil {
		t.Fatal(row)
	}
	// Someone else's list is no row at all.
	if got := get(t, s.Client("bob", true), fmt.Sprintf("/api/v1/rows/list:%d", lid))["rows"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
}
