package title_test

// Seasons and episodes: ticks, hearts and ratings.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/testbase"
)

const ep = "/api/v1/title/tv/1399/episode"

func season(t *testing.T, e *env, c *testenvClient, n int) []map[string]any {
	t.Helper()
	page := ok(t, c, "GET", fmt.Sprintf("/api/v1/title/tv/1399/season/%d", n))
	var out []map[string]any
	for _, x := range page["episodes"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func hearts(eps []map[string]any) string {
	var out []string
	for _, e := range eps {
		if e["favorite"] == true {
			out = append(out, fmt.Sprint(e["number"]))
		}
	}
	return strings.Join(out, ",")
}

func ratings(eps []map[string]any) string {
	var out []string
	for _, e := range eps {
		if e["rating"] != nil {
			out = append(out, fmt.Sprintf("%v=%v", e["number"], e["rating"]))
		}
	}
	return strings.Join(out, ",")
}

func count(t *testing.T, e *env, sql string) int {
	t.Helper()
	var n int
	if err := e.s.Pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnEpisodeCanBeLikedAndUnliked(t *testing.T) {
	e := start(t)
	if r := e.alice.Do(t, "POST", ep+"/1/3/favorite", nil); r.Body != `{"ok":true,"favorite":true}` {
		t.Fatal(r.StatusCode, r.Body)
	}
	ok(t, e.alice, "POST", ep+"/1/3/favorite") // twice is still one
	ok(t, e.alice, "POST", ep+"/2/1/favorite")
	if h := hearts(season(t, e, e.alice, 1)); h != "3" {
		t.Fatal(h)
	}
	if h := hearts(season(t, e, e.alice, 2)); h != "1" {
		t.Fatal(h)
	}
	if n := count(t, e, `SELECT count(*) FROM episode_favorites`); n != 2 {
		t.Fatal(n)
	}
	if r := e.alice.Do(t, "DELETE", ep+"/1/3/favorite", nil); r.Body != `{"ok":true,"favorite":false}` {
		t.Fatal(r.Body)
	}
	ok(t, e.alice, "DELETE", ep+"/1/3/favorite") // already gone: no error
	if h := hearts(season(t, e, e.alice, 1)); h != "" {
		t.Fatal(h)
	}
}

func TestEpisodeMarksArePerPersonAndNotTheShows(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "POST", ep+"/1/1/favorite")
	ok(t, e.alice, "PUT", ep+"/1/1/rating", `{"rating": 9}`)
	if h, r := hearts(season(t, e, e.bob, 1)), ratings(season(t, e, e.bob, 1)); h != "" || r != "" {
		t.Fatal(h, r)
	}
	st := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/status")
	if st["favorite"] != false || st["rating"] != nil {
		t.Fatal(st)
	}
	community := ok(t, e.bob, "GET", "/api/v1/title/tv/1399")["community"].(map[string]any)
	if community["count"] != 0.0 {
		t.Fatal(community)
	}
	// Rating the show leaves its episodes alone, and the other way round.
	ok(t, e.alice, "PUT", ep+"/1/2/rating", `{"rating": 4}`)
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/rating", `{"rating": 9}`)
	ok(t, e.alice, "DELETE", "/api/v1/title/tv/1399/rating")
	if r := ratings(season(t, e, e.alice, 1)); r != "1=9,2=4" {
		t.Fatal(r)
	}
}

func TestAnEpisodeCanBeRatedReratedAndCleared(t *testing.T) {
	e := start(t)
	if r := e.alice.Do(t, "PUT", ep+"/1/3/rating", `{"rating": 8}`); r.Body != `{"ok":true,"rating":8}` {
		t.Fatal(r.StatusCode, r.Body)
	}
	ok(t, e.alice, "PUT", ep+"/1/3/rating", `{"rating": 7}`)
	ok(t, e.alice, "PUT", ep+"/2/1/rating", `{"rating": 10}`)
	if r := ratings(season(t, e, e.alice, 1)); r != "3=7" {
		t.Fatal(r)
	}
	if n := count(t, e, `SELECT count(*) FROM episode_ratings`); n != 2 {
		t.Fatal("re-rating is one row:", n)
	}
	if r := e.alice.Do(t, "DELETE", ep+"/1/3/rating", nil); r.Body != `{"ok":true,"rating":null}` {
		t.Fatal(r.Body)
	}
	ok(t, e.alice, "DELETE", ep+"/1/3/rating")
	if r := ratings(season(t, e, e.alice, 2)); r != "1=10" {
		t.Fatal(r)
	}
	// The POST alias is gone: no client sends it.
	if r := e.alice.Do(t, "POST", ep+"/1/3/rating", `{"rating": 7}`); r.StatusCode != 405 && r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestTheEpisodeScaleIsATitles(t *testing.T) {
	e := start(t)
	for _, bad := range []string{`{"rating": 0}`, `{"rating": 11}`, `{"rating": -1}`, `{"rating": 4.5}`, `{"rating": "x"}`, `{"rating": null}`, `{}`} {
		call(t, e.alice, 422, "PUT", ep+"/1/1/rating", bad)
	}
	for _, good := range []int{1, 10} {
		if r := ok(t, e.alice, "PUT", ep+"/1/1/rating", fmt.Sprintf(`{"rating": %d}`, good)); r["rating"] != float64(good) {
			t.Fatal(r)
		}
	}
}

func TestEpisodeWritesAtOnceNeverFail(t *testing.T) {
	e := start(t)
	ok(t, e.alice, "GET", "/api/v1/title/tv/1399/status")
	var wg sync.WaitGroup
	codes := make([]int, 50)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch {
			case i%6 == 1:
				codes[i] = e.alice.Do(t, "DELETE", ep+"/1/5/rating", nil).StatusCode
			case i%6 == 4:
				codes[i] = e.alice.Do(t, "DELETE", ep+"/1/5/favorite", nil).StatusCode
			case i%2 == 0:
				codes[i] = e.alice.Do(t, "PUT", ep+"/1/5/rating", fmt.Sprintf(`{"rating": %d}`, 1+i%10)).StatusCode
			default:
				codes[i] = e.alice.Do(t, "POST", ep+"/1/5/favorite", nil).StatusCode
			}
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("call %d: %d", i, c)
		}
	}
	if n := count(t, e, `SELECT count(*) FROM episode_ratings`); n > 1 {
		t.Fatal(n)
	}
	ok(t, e.alice, "PUT", ep+"/1/5/rating", `{"rating": 6}`)
	if r := ratings(season(t, e, e.alice, 1)); r != "5=6" {
		t.Fatal(r)
	}
}

func TestOnlyShowsHaveEpisodes(t *testing.T) {
	e := start(t)
	call(t, e.alice, 404, "POST", "/api/v1/title/movie/603/episode/1/1/favorite")
	call(t, e.alice, 404, "PUT", "/api/v1/title/movie/603/episode/1/1/rating", `{"rating": 5}`)
	call(t, e.alice, 404, "GET", "/api/v1/title/movie/603/season/1")
	call(t, e.alice, 404, "GET", "/api/v1/title/movie/603/next")
	call(t, e.alice, 422, "POST", ep+"/-1/1/favorite")
	call(t, e.alice, 422, "POST", ep+"/1/2147483648/favorite")
	// A season numbered by its year is fine.
	ok(t, e.alice, "POST", ep+"/2023/1/favorite")
	if r := ok(t, e.alice, "PUT", ep+"/2023/1/rating", `{"rating": 6}`); r["rating"] != 6.0 {
		t.Fatal(r)
	}
	if r := e.alice.Do(t, "GET", "/api/v1/title/tv/1399/season/2023", nil); r.StatusCode == 422 {
		t.Fatal(r.Body)
	}
}

func TestTickingASeasonAndTheSeasonPage(t *testing.T) {
	e := start(t)
	e.s.SeedLibrary(t)
	var eps []string
	for n := 1; n <= 10; n++ {
		eps = append(eps, fmt.Sprintf(`{"season": 1, "episode": %d}`, n))
	}
	body := `{"episodes": [` + strings.Join(eps, ",") + `]}`
	if r := ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", body); r["changed"] != 10.0 {
		t.Fatal(r)
	}
	// Again: nothing new.
	if r := ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", body); r["changed"] != 0.0 {
		t.Fatal(r)
	}
	page := season(t, e, e.alice, 1)
	if len(page) != 10 || page[0]["watched"] != 1.0 || page[0]["name"] != "Episode 1" || page[0]["aired"] != true ||
		page[0]["in_library"] != true || len(page[0]["watch_ids"].([]any)) != 1 || page[0]["last_watched_at"] == nil {
		t.Fatal(page[0])
	}
	var title, epTitle string
	_ = e.s.Pool.QueryRow(context.Background(), `SELECT title, episode_title FROM history WHERE episode = 3`).Scan(&title, &epTitle)
	if title != "Show 1399" || epTitle != "Episode 3" {
		t.Fatal(title, epTitle)
	}
	d := ok(t, e.alice, "GET", "/api/v1/title/tv/1399")
	if s := d["seasons"].([]any)[0].(map[string]any); s["number"] != 1.0 || s["watched"] != 10.0 {
		t.Fatal(s)
	}
	// Unticking two.
	r := ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes",
		`{"episodes": [{"season": 1, "episode": 2}, {"season": 1, "episode": 3}, {"season": 1, "episode": 2}], "watched": false}`)
	if r["changed"] != 2.0 || count(t, e, `SELECT count(*) FROM history`) != 8 {
		t.Fatal(r)
	}
	call(t, e.alice, 422, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": []}`)
}

func TestASeasonTickIsAllOrNothing(t *testing.T) {
	e := start(t)
	// The database refuses episode 5: the whole tick is refused, none of
	// the season is written (the Python committed episode by episode).
	e.s.Exec(t, `ALTER TABLE history ADD CONSTRAINT test_no_five CHECK (episode IS DISTINCT FROM 5)`)
	t.Cleanup(func() { e.s.Exec(t, `ALTER TABLE history DROP CONSTRAINT IF EXISTS test_no_five`) })
	var eps []string
	for n := 1; n <= 10; n++ {
		eps = append(eps, fmt.Sprintf(`{"season": 1, "episode": %d}`, n))
	}
	call(t, e.alice, 500, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [`+strings.Join(eps, ",")+`]}`)
	if n := count(t, e, `SELECT count(*) FROM history`); n != 0 {
		t.Fatalf("%d episodes were ticked", n)
	}
}

func TestATickIsMarkedPlayedInThePersonsJellyfin(t *testing.T) {
	// DRY_RUN off: under it the marks are not sent (TestDryRunChangesNothingOutside).
	base := testbase.BaseEnv(t)
	base["DRY_RUN"] = "false"
	e := startWith(t, base)
	e.s.SeedLibrary(t)
	ok(t, e.alice, "GET", "/api/v1/title/tv/1399/status")
	e.s.Exec(t, `UPDATE users SET jellyfin_user_id = $1 WHERE username = 'alice'`, strings.Repeat("f", 32))
	_ = e.s.KV.Do(context.Background(), e.s.KV.B().Set().Key("c:jf:continue:1").Value("x").Build())
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [{"season": 1, "episode": 1}, {"season": 1, "episode": 2}]}`)
	// The API only asks; nothing reaches Jellyfin until a worker serves the
	// queue (cmd/worker runs JFSync.RunQueued).
	time.Sleep(100 * time.Millisecond)
	if n := e.f.Jellyfin.Count("POST /Users/" + strings.Repeat("f", 32) + "/PlayedItems/"); n != 0 {
		t.Fatalf("the API marked %d episodes itself", n)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go e.s.App.RW.JFSync.RunQueued(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for e.f.Jellyfin.Count("POST /Users/"+strings.Repeat("f", 32)+"/PlayedItems/") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.f.Jellyfin.Count("POST /Users/" + strings.Repeat("f", 32) + "/PlayedItems/"); n != 2 {
		t.Fatal(e.f.Jellyfin.Calls())
	}
	// Up next is made stale once Jellyfin has the marks.
	for time.Now().Before(deadline) {
		if n, _ := e.s.KV.Do(context.Background(), e.s.KV.B().Exists().Key("c:jf:continue:1").Build()).AsInt64(); n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := e.s.KV.Do(context.Background(), e.s.KV.B().Exists().Key("c:jf:continue:1").Build()).AsInt64(); n != 0 {
		t.Fatal("Up next was not made stale")
	}
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [{"season": 1, "episode": 1}], "watched": false}`)
	for e.f.Jellyfin.Count("DELETE /Users/") < 1 && time.Now().Before(deadline.Add(5*time.Second)) {
		time.Sleep(20 * time.Millisecond)
	}
	if e.f.Jellyfin.Count("DELETE /Users/") != 1 {
		t.Fatal("not unplayed")
	}
}

func TestUpNextCarriesTheEpisodesOwnMarks(t *testing.T) {
	e := start(t)
	if n := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/next")["next"].(map[string]any); n["label"] != "Play S01E01" {
		t.Fatal(n)
	}
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [{"season": 1, "episode": 1}]}`)
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/rating", `{"rating": 3}`)
	for _, c := range []*testenvClient{e.bob} {
		ok(t, c, "PUT", ep+"/1/2/rating", `{"rating": 9}`)
		ok(t, c, "POST", ep+"/1/2/favorite")
	}
	n := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/next")["next"].(map[string]any)
	if n["season"] != 1.0 || n["episode"] != 2.0 || n["favorite"] != false || n["rating"] != nil || n["label"] != "Continue S01E02" {
		t.Fatal(n)
	}
	ok(t, e.alice, "PUT", ep+"/1/2/rating", `{"rating": 7}`)
	ok(t, e.alice, "POST", ep+"/1/2/favorite")
	n = ok(t, e.alice, "GET", "/api/v1/title/tv/1399/next")["next"].(map[string]any)
	if n["favorite"] != true || n["rating"] != 7.0 {
		t.Fatal(n)
	}
	// The end of a season leads to the next; caught up with what aired: none.
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [{"season": 1, "episode": 10}]}`)
	if n := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/next")["next"].(map[string]any); n["label"] != "Continue S02E01" {
		t.Fatal(n)
	}
	ok(t, e.alice, "POST", "/api/v1/title/tv/1399/episodes", `{"episodes": [{"season": 3, "episode": 10}]}`)
	if n := ok(t, e.alice, "GET", "/api/v1/title/tv/1399/next")["next"]; n != nil {
		t.Fatal(n)
	}
}
