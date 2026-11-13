package lists_test

// Ported from the Python's tests/test_lists.py, the lists parts of
// test_outside_services.py and test_no_caps.py: own lists, curated
// (this install) lists, MDBList lists, stars and Home pins, and MDBList's copy.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"movienight/server/internal/clients"
	"movienight/server/internal/rows"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func start(t *testing.T) (*testenv.Server, *testenv.Fakes) {
	t.Helper()
	return testenv.StartFaked(t, testbase.BaseEnv(t))
}

func curatedList(t *testing.T, s *testenv.Server) int {
	t.Helper()
	var id int
	if err := s.Pool.QueryRow(context.Background(), `INSERT INTO lists (owner_id, name, kind, source, origin, created_at)
		VALUES (NULL, 'Reddit Top 100', 'list', 'copy', 'trakt', now()) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.Exec(t, `INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at) VALUES
		($1, 'movie', 603, 0, now()), ($1, 'tv', 1399, 1, now())`, id)
	return id
}

func post(t *testing.T, c *testenv.Client, path, body string) map[string]any {
	t.Helper()
	r := c.Do(t, http.MethodPost, path, body)
	if r.StatusCode != 200 {
		t.Fatalf("POST %s: %d %s", path, r.StatusCode, r.Body)
	}
	return r.Map(t)
}

func get(t *testing.T, c *testenv.Client, path string) map[string]any {
	t.Helper()
	r := c.Do(t, http.MethodGet, path, nil)
	if r.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, r.StatusCode, r.Body)
	}
	return r.Map(t)
}

func keys(m map[string]any) []string {
	var out []string
	for _, c := range m["items"].([]any) {
		out = append(out, c.(map[string]any)["key"].(string))
	}
	return out
}

func TestAnOwnListIsPrivateAndEditable(t *testing.T) {
	s, _ := start(t)
	alice, bob := s.Client("alice", true), s.Client("bob", true)
	lid := int(post(t, alice, "/api/v1/lists", `{"name": "Rainy Sunday"}`)["id"].(float64))
	if !post(t, alice, fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "movie", "tmdb_id": 603}`)["added"].(bool) ||
		!post(t, alice, fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "tv", "tmdb_id": 1399}`)["added"].(bool) {
		t.Fatal("not added")
	}
	if post(t, alice, fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "tv", "tmdb_id": 1399}`)["added"].(bool) {
		t.Fatal("added twice")
	}
	l := get(t, alice, fmt.Sprintf("/api/v1/lists/%d", lid))
	if l["movies"] != 1.0 || l["shows"] != 1.0 || l["group"] != "mine" || l["editable"] != true || l["count"] != 2.0 {
		t.Fatalf("%v", l)
	}
	if k := keys(get(t, alice, fmt.Sprintf("/api/v1/lists/%d?media=tv", lid))); len(k) != 1 || k[0] != "tv:1399" {
		t.Fatal(k)
	}
	for _, r := range []*testenv.Response{
		bob.Do(t, "GET", fmt.Sprintf("/api/v1/lists/%d", lid), nil),
		bob.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "movie", "tmdb_id": 1}`),
		bob.Do(t, "DELETE", fmt.Sprintf("/api/v1/lists/%d", lid), nil),
	} {
		if r.StatusCode != 404 || r.Map(t)["detail"] != "List not found" {
			t.Fatalf("bob: %d %s", r.StatusCode, r.Body)
		}
	}
	if r := alice.Do(t, "DELETE", fmt.Sprintf("/api/v1/lists/%d/items/tv/1399", lid), nil); r.StatusCode != 200 {
		t.Fatal(r.Body)
	}
	if k := keys(get(t, alice, fmt.Sprintf("/api/v1/lists/%d", lid))); len(k) != 1 || k[0] != "movie:603" {
		t.Fatal(k)
	}
}

func TestACuratedListIsFixedButCanBeStarredPinnedAndCopied(t *testing.T) {
	s, _ := start(t)
	alice, bob, owner := s.Client("alice", true), s.Client("bob", true), s.Client("owner", true)
	lid := curatedList(t, s)
	if r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/items", lid), `{"media_type": "movie", "tmdb_id": 1}`); r.StatusCode != 409 {
		t.Fatal(r.StatusCode)
	}
	if r := alice.Do(t, "PATCH", fmt.Sprintf("/api/v1/lists/%d", lid), `{"name": "Mine now"}`); r.StatusCode != 409 {
		t.Fatal(r.StatusCode)
	}
	r := alice.Do(t, "PATCH", fmt.Sprintf("/api/v1/lists/%d", lid), `{"starred": true, "pinned": true}`)
	if f := r.Map(t); f["starred"] != true || f["pinned"] != true {
		t.Fatal(r.Body)
	}
	for _, x := range get(t, bob, "/api/v1/lists")["lists"].([]any) {
		if x.(map[string]any)["starred"] != false {
			t.Fatal("stars are per person")
		}
	}
	var pinned []map[string]any
	for _, x := range get(t, alice, "/api/v1/home/config?tab=for_you")["rows"].([]any) {
		if row := x.(map[string]any); row["list"] == true && row["on"] == true {
			pinned = append(pinned, row)
		}
	}
	if len(pinned) != 1 || pinned[0]["key"] != fmt.Sprintf("list:%d", lid) || pinned[0]["group"] != "shared" {
		t.Fatal(pinned)
	}
	// The pinned list sits right after "Plan to watch", near the top.
	cfg := get(t, alice, "/api/v1/home/config?tab=for_you")["rows"].([]any)
	if cfg[2].(map[string]any)["key"] != "watchlist" || cfg[3].(map[string]any)["key"] != fmt.Sprintf("list:%d", lid) {
		t.Fatal(cfg[:4])
	}
	// A reset keeps it pinned.
	post(t, alice, "/api/v1/home/config/reset?tab=for_you", "")
	if get(t, alice, fmt.Sprintf("/api/v1/lists/%d", lid))["pinned"] != true {
		t.Fatal("reset unpinned the list")
	}
	copyID := int(post(t, alice, fmt.Sprintf("/api/v1/lists/%d/copy", lid), "")["id"].(float64))
	c := get(t, alice, fmt.Sprintf("/api/v1/lists/%d", copyID))
	if c["group"] != "mine" || c["count"] != 2.0 || c["editable"] != true || strings.Join(keys(c), ",") != "movie:603,tv:1399" {
		t.Fatal(c)
	}
	if r := alice.Do(t, "DELETE", fmt.Sprintf("/api/v1/lists/%d", lid), nil); r.StatusCode != 403 {
		t.Fatal("only admins remove curated lists", r.StatusCode)
	}
	if r := owner.Do(t, "DELETE", fmt.Sprintf("/api/v1/lists/%d", lid), nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, r.Body)
	}
}

func TestSorting(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	lid := int(post(t, alice, "/api/v1/lists", `{"name": "Sorted"}`)["id"].(float64))
	for i, tid := range []int{700, 500, 600} {
		post(t, alice, fmt.Sprintf("/api/v1/lists/%d/items", lid), fmt.Sprintf(`{"media_type": "movie", "tmdb_id": %d}`, tid))
		// Distinct added times, as three taps would have.
		s.Exec(t, `UPDATE list_items SET added_at = now() + make_interval(secs => $1) WHERE tmdb_id = $2`, i, tid)
	}
	order := func(q string) string {
		return strings.Join(keys(get(t, alice, fmt.Sprintf("/api/v1/lists/%d%s", lid, q))), ",")
	}
	if o := order(""); o != "movie:700,movie:500,movie:600" {
		t.Fatal(o)
	}
	if o := order("?sort=title"); o != "movie:500,movie:600,movie:700" {
		t.Fatal(o)
	}
	if o := order("?sort=added"); o != "movie:600,movie:500,movie:700" {
		t.Fatal(o)
	}
	if r := alice.Do(t, "GET", fmt.Sprintf("/api/v1/lists/%d?sort=random", lid), nil); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
}

func TestManyAddsAtOnceTakeDistinctGapFreePositions(t *testing.T) {
	// The Python read max(position) and wrote max+1 in two steps: adds at
	// once took the same position. The list's row is locked now.
	s, _ := start(t)
	alice := s.Client("alice", true)
	lid := int(post(t, alice, "/api/v1/lists", `{"name": "Busy"}`)["id"].(float64))
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/items", lid), fmt.Sprintf(`{"media_type": "movie", "tmdb_id": %d}`, 1000+i))
			if r.StatusCode != 200 {
				t.Error(r.StatusCode, r.Body)
			}
		}()
	}
	wg.Wait()
	rows, err := s.Pool.Query(context.Background(), `SELECT position FROM list_items WHERE list_id = $1 ORDER BY position`, lid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var p int
		_ = rows.Scan(&p)
		got = append(got, p)
	}
	for i, p := range got {
		if p != i+1 {
			t.Fatalf("positions %v", got)
		}
	}
	if len(got) != 30 {
		t.Fatal(len(got))
	}
}

func TestANameTooLongIsRefusedInPlainWords(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	r := alice.Do(t, "POST", "/api/v1/lists", `{"name": "`+strings.Repeat("n", 301)+`"}`)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "The name is too long: at most 300 characters." {
		t.Fatal(r.StatusCode, r.Body)
	}
	if r := alice.Do(t, "POST", "/api/v1/lists", `{"name": "`+strings.Repeat("n", 300)+`"}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, r.Body)
	}
	if r := alice.Do(t, "POST", "/api/v1/lists", `{"name": "   "}`); r.StatusCode != 422 || r.Map(t)["detail"] != "A list needs a name" {
		t.Fatal(r.Body)
	}
}

func TestTheNameLimitIsASetting(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["NAME_MAX_CHARS"] = "10"
	s, _ := testenv.StartFaked(t, env)
	r := s.Client("alice", true).Do(t, "POST", "/api/v1/lists", `{"name": "eleven chars"}`)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "The name is too long: at most 10 characters." {
		t.Fatal(r.StatusCode, r.Body)
	}
}

func TestTheListsTab(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	curatedList(t, s)
	s.Exec(t, `INSERT INTO users (username, is_admin, created_at) VALUES ('x', false, now())`) // someone else
	get(t, alice, "/api/v1/lists")                                                             // alice exists from here
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source)
		SELECT id, 'movie', 603, now(), 'app' FROM users WHERE username = 'alice'`)
	data := get(t, alice, "/api/v1/lists")
	first := data["lists"].([]any)[0].(map[string]any)
	if first["group"] != "shared" || first["by"] != "Shared list" || len(first["posters"].([]any)) != 2 {
		t.Fatal(first)
	}
	wl := data["watchlist"].(map[string]any)
	if wl["count"] != 1.0 || wl["posters"].([]any)[0] != "/p603.jpg" {
		t.Fatal(wl)
	}
	if data["favorites"].(map[string]any)["count"] != 0.0 {
		t.Fatal(data)
	}
}

func TestCollectionsPageOnPastTheOldCaps(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/lists")
	s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source)
		SELECT u.id, 'movie', 600 + n, n, now() - make_interval(days => n), 'app' FROM users u, generate_series(1, 10) n WHERE u.username = 'alice'`)
	top := get(t, alice, "/api/v1/collection/rated")
	if top["name"] != "Your top rated" || top["count"] != 4.0 || strings.Join(keys(top), ",") != "movie:610,movie:609,movie:608,movie:607" {
		t.Fatal(top)
	}
	all := get(t, alice, "/api/v1/collection/ratings?page=501")
	if all["count"] != 10.0 || len(all["items"].([]any)) != 0 || all["more"] != false {
		t.Fatal(all)
	}
}

func TestMDBListExploreAndFollow(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	top := get(t, alice, "/api/v1/mdblist/lists")
	if top["mode"] != "browse" || top["results"].([]any)[0].(map[string]any)["name"] != "Latest TV Shows" {
		t.Fatal(top)
	}
	if link := get(t, alice, "/api/v1/mdblist/lists?q=https://mdblist.com/lists/gary/latest-tv-shows"); link["mode"] != "link" {
		t.Fatal(link)
	}
	out := post(t, alice, "/api/v1/mdblist/follow", `{"ref": "2194"}`)
	if out["count"] != 2.0 {
		t.Fatal(out)
	}
	if again := post(t, alice, "/api/v1/mdblist/follow", `{"ref": "2194"}`); again["id"] != out["id"] {
		t.Fatal("following twice made two lists")
	}
	l := get(t, alice, fmt.Sprintf("/api/v1/lists/%d", int(out["id"].(float64))))
	if l["group"] != "mdblist" || l["by"] != "MDBList · gary" || !strings.HasSuffix(l["origin_url"].(string), "/gary/latest-tv-shows") {
		t.Fatal(l)
	}
	// Explore marks the list as followed.
	if top := get(t, alice, "/api/v1/mdblist/lists"); top["results"].([]any)[0].(map[string]any)["followed_id"] != out["id"] {
		t.Fatal(top)
	}
	if r := alice.Do(t, "POST", "/api/v1/mdblist/follow", `{"ref": "https://example.com/x"}`); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
	if r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/sync", int(out["id"].(float64))), ""); r.StatusCode != 200 || r.Map(t)["count"] != 2.0 {
		t.Fatal(r.Body)
	}
}

func TestAFollowedListKeepsItsTitlesWhenMDBListCantSendThem(t *testing.T) {
	s, f := start(t)
	alice := s.Client("alice", true)
	out := post(t, alice, "/api/v1/mdblist/follow", `{"ref": "2194"}`)
	lid := int(out["id"].(float64))
	f.MDBList.Override("/lists/2194/items", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("[]")) })
	r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/sync", lid), "")
	if r.StatusCode != 502 || !strings.Contains(r.Map(t)["detail"].(string), "kept") {
		t.Fatal(r.StatusCode, r.Body)
	}
	f.MDBList.Fail("/lists/2194/items", 500)
	if r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/sync", lid), ""); r.StatusCode != 502 {
		t.Fatal(r.StatusCode, r.Body)
	}
	// The nightly sync (the worker) leaves it as it was too.
	_, svc, _ := rows.ForDeps(s.App.RW)
	if n, err := svc.SyncAll(context.Background(), s.App.Clients.MDBList); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if k := keys(get(t, alice, fmt.Sprintf("/api/v1/lists/%d", lid))); len(k) != 2 {
		t.Fatal(k)
	}
}

func TestTheMDBListPagesWorkFromTheCopyWhileMDBListIsDown(t *testing.T) {
	s, f := start(t)
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/mdblist/lists")
	if p := get(t, alice, "/api/v1/mdblist/lists/2194"); p["count"] != 2.0 {
		t.Fatal(p)
	}
	s.Exec(t, `SELECT 1`)
	flushCache(t, s)
	for _, p := range []string{"/lists/top", "/lists/search", "/lists/2194", "/lists/gary/latest-tv-shows", "/lists/2194/items", "/lists/999", "/lists/999/items"} {
		f.MDBList.Fail(p, 500)
	}
	top := get(t, alice, "/api/v1/mdblist/lists")
	if top["mode"] != "browse" || top["results"].([]any)[0].(map[string]any)["mdblist_id"] != 2194.0 || top["error"] != nil {
		t.Fatal(top)
	}
	found := get(t, alice, "/api/v1/mdblist/lists?q=latest+tv")
	if r := found["results"].([]any); len(r) != 1 || r[0].(map[string]any)["mdblist_id"] != 2194.0 {
		t.Fatal(found)
	}
	link := get(t, alice, "/api/v1/mdblist/lists?q=https://mdblist.com/lists/gary/latest-tv-shows")
	if link["mode"] != "link" || link["results"].([]any)[0].(map[string]any)["mdblist_id"] != 2194.0 {
		t.Fatal(link)
	}
	preview := get(t, alice, "/api/v1/mdblist/lists/2194")
	k := keys(preview)
	sort.Strings(k)
	if preview["count"] != 2.0 || strings.Join(k, ",") != "movie:603,tv:1399" {
		t.Fatal(preview)
	}
	if fl := post(t, alice, "/api/v1/mdblist/follow", `{"ref": "2194"}`); fl["count"] != 2.0 {
		t.Fatal(fl)
	}
	// A list never seen while MDBList answered still says MDBList didn't answer.
	if r := alice.Do(t, "GET", "/api/v1/mdblist/lists/999", nil); r.StatusCode != 502 {
		t.Fatal(r.StatusCode, r.Body)
	}
}

func flushCache(t *testing.T, s *testenv.Server) {
	t.Helper()
	// Every cached answer and the MDBList breaker go, as the Python's
	// cache._store.clear(); the copy (in Postgres) stays.
	if err := s.KV.Do(context.Background(), s.KV.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
}

func withoutKey(s *testenv.Server) {
	s.App.Clients.MDBList = clients.NewMDBList(clients.Upstream(), "http://mdblist.invalid", "", s.KV, s.App.RW.Log)
}

func TestWithoutAnMDBListKeyTheSavedListsStillShow(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	get(t, alice, "/api/v1/mdblist/lists")
	get(t, alice, "/api/v1/mdblist/lists/2194")
	flushCache(t, s)
	withoutKey(s)
	top := get(t, alice, "/api/v1/mdblist/lists")
	if top["results"].([]any)[0].(map[string]any)["mdblist_id"] != 2194.0 || top["enabled"] != true {
		t.Fatal(top)
	}
	if p := get(t, alice, "/api/v1/mdblist/lists/2194"); p["count"] != 2.0 {
		t.Fatal(p)
	}
	if fl := post(t, alice, "/api/v1/mdblist/follow", `{"ref": "2194"}`); fl["count"] != 2.0 {
		t.Fatal(fl)
	}
}

func TestWithoutAKeyOrACopyThePageIsEmptyWithNoNotice(t *testing.T) {
	s, _ := start(t)
	alice := s.Client("alice", true)
	withoutKey(s)
	r := get(t, alice, "/api/v1/mdblist/lists")
	if len(r["results"].([]any)) != 0 || r["error"] != nil || r["enabled"] != true {
		t.Fatal(r)
	}
	if got := alice.Do(t, "GET", "/api/v1/mdblist/lists/2194", nil); got.StatusCode != 503 {
		t.Fatal(got.StatusCode)
	}
}

func TestASmartListRunsItsFiltersAndSaysSoWhenTMDBIsDown(t *testing.T) {
	s, f := start(t)
	alice := s.Client("alice", true)
	lid := int(post(t, alice, "/api/v1/lists/smart", `{"name": "Old films", "filters": {"media": "movie", "year_to": 1960, "min_rating": 7, "sort": "nonsense"}}`)["id"].(float64))
	l := get(t, alice, fmt.Sprintf("/api/v1/lists/%d", lid))
	if l["kind"] != "smart" || l["count"] != nil || len(l["items"].([]any)) != 20 || l["more"] != true {
		t.Fatal(l)
	}
	filters := l["filters"].(map[string]any)
	if filters["year_to"] != 1960.0 || filters["min_rating"] != 7.0 || filters["sort"] != nil {
		t.Fatal(filters)
	}
	var call string
	for _, c := range f.TMDB.Calls() {
		if strings.HasPrefix(c, "GET /discover/movie") {
			call = c
		}
	}
	if !strings.Contains(call, "primary_release_date.lte=1960-12-31") || !strings.Contains(call, "vote_count.gte=100") {
		t.Fatal(call)
	}
	if r := alice.Do(t, "POST", fmt.Sprintf("/api/v1/lists/%d/copy", lid), ""); r.StatusCode != 409 {
		t.Fatal(r.StatusCode)
	}
	f.TMDB.Fail("/discover/movie", 503)
	got := alice.Do(t, "GET", fmt.Sprintf("/api/v1/lists/%d?page=2", lid), nil)
	if got.StatusCode != 502 || got.Map(t)["detail"] != rows.TMDBDownText {
		t.Fatal(got.StatusCode, got.Body)
	}
}

func TestTheListQueriesUseTheirIndexes(t *testing.T) {
	s, _ := start(t)
	for name, plan := range map[string]string{
		"list items": s.Explain(t, store.ListItemsPageSQL, int32(1), "", "list", int32(0), int32(40)),
		"watchlist":  s.Explain(t, store.WatchlistPageSQL, int32(1), "", int32(0), int32(40)),
		"ratings":    s.Explain(t, store.RatingsPageSQL, int32(1), "", false, int32(0), int32(40)),
		"favorites":  s.Explain(t, store.FavoritesPageSQL, int32(1), "", int32(0), int32(40)),
	} {
		if strings.Contains(plan, "Seq Scan") {
			t.Errorf("%s scans a table whole:\n%s", name, plan)
		}
	}
}
