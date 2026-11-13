package search_test

// Behaviour tests of search, the AI's best guess, people, /titles/fill and
// the Jellyfin image proxy, ported from the Python's tests/test_search.py,
// tests/test_spelling.py and tests/test_no_caps.py, against the fakes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

// cat is a catalog hit as Meilisearch returns it.
func cat(id int, title string, score float64, votes int) fakes.JSON {
	m := fakes.Movie(id)
	m["title"] = title
	return fakes.JSON{"id": fmt.Sprintf("m%d", id), "tmdb_id": id, "media_type": "movie", "title": title,
		"original_title": title, "votes": votes, "_rankingScore": score, "tmdb": m}
}

type searchOut struct {
	Library []map[string]any `json:"library"`
	TMDB    []map[string]any `json:"tmdb"`
	People  []map[string]any `json:"people"`
	First   string           `json:"first"`
	AI      bool             `json:"ai"`
	Page    int              `json:"page"`
	More    map[string]bool  `json:"more"`
	Note    *string          `json:"note"`
	Fixed   *string          `json:"corrected"`
}

func search(t *testing.T, c *testenv.Client, params url.Values) searchOut {
	t.Helper()
	r := c.Do(t, "GET", "/api/v1/search?"+params.Encode(), nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	var out searchOut
	r.JSON(t, &out)
	return out
}

func q(text string, kv ...string) url.Values {
	v := url.Values{"q": {text}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func field(cards []map[string]any, k string) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i], _ = c[k].(string)
	}
	return out
}

func start(t *testing.T, env testbase.Env) (*testenv.Server, *testenv.Fakes, *testenv.Client) {
	s, f := testenv.StartFaked(t, env)
	return s, f, s.Client("alice", true)
}

func TestTheBestMatchOutsideTheLibraryComesFirst(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(9341, "Treasure Kids", 0.80, 12), cat(9340, "The Goonies", 0.93, 6433)}
	r := search(t, alice, q("kids with a treasure map save their homes"))
	if r.First != "tmdb" || strings.Join(field(r.TMDB, "title")[:3], ",") != "The Goonies,Treasure Kids,Movie 777" {
		t.Fatalf("%s %v", r.First, field(r.TMDB, "title"))
	}
	if strings.Join(field(r.Library, "title"), ",") != "Movie 603" {
		t.Fatal(field(r.Library, "title"))
	}
}

func TestTheLibraryComesFirstWhenItMatchesBest(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(9341, "Treasure Kids", 0.55, 1000)}
	r := search(t, alice, q("movie 603"))
	if r.First != "library" || r.Library[0]["title"] != "Movie 603" {
		t.Fatalf("%+v", r)
	}
}

func TestAnExactTitleFromTMDBBeatsAVagueCatalogHit(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(9341, "Treasure Kids", 0.7, 1000)}
	r := search(t, alice, q("Movie 777"))
	if r.First != "tmdb" || r.TMDB[0]["title"] != "Movie 777" {
		t.Fatalf("%+v", r)
	}
}

func TestTitlesInTheLibraryAreNotRepeated(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(603, "Movie 603", 0.99, 1000)}
	r := search(t, alice, q("movie 603"))
	if strings.Join(field(r.Library, "key"), ",") != "movie:603" || strings.Contains(strings.Join(field(r.TMDB, "key"), ","), "movie:603") {
		t.Fatalf("%v %v", field(r.Library, "key"), field(r.TMDB, "key"))
	}
}

func TestTheMediaFilterAppliesToTheCatalog(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(9340, "The Goonies", 0.93, 1000)}
	r := search(t, alice, q("goonies", "media", "tv"))
	for _, c := range append(r.TMDB, r.Library...) {
		if c["media_type"] != "tv" {
			t.Fatalf("%v", c)
		}
	}
}

func TestALibraryTitleFoundInTheCatalogCountsAsYours(t *testing.T) {
	s, f, alice := start(t, testbase.BaseEnv(t))
	s.SeedLibrary(t)
	f.Meili.Catalog = []fakes.JSON{{"id": "t1399", "tmdb_id": 1399, "media_type": "tv", "title": "Show 1399",
		"original_title": "Show 1399", "votes": 900, "_rankingScore": 0.95, "tmdb": fakes.Show(1399)}}
	r := search(t, alice, q("dragons and a throne"))
	if r.Library[0]["key"] != "tv:1399" || r.First != "library" || r.Library[0]["in_library"] != true {
		t.Fatalf("%+v", r.Library)
	}
	if strings.Contains(strings.Join(field(r.TMDB, "key"), ","), "tv:1399") {
		t.Fatal(field(r.TMDB, "key"))
	}
}

func TestCardsCarryThePersonsOwnState(t *testing.T) {
	s, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Catalog = []fakes.JSON{cat(9340, "The Goonies", 0.93, 6433)}
	alice.Do(t, "GET", "/api/v1/search?q=x", nil) // creates alice
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source)
		SELECT id, 'movie', 9340, now(), 'app' FROM users WHERE username = 'alice'`)
	r := search(t, alice, q("goonies"))
	if r.TMDB[0]["key"] != "movie:9340" || r.TMDB[0]["watchlist"] != true {
		t.Fatalf("%v", r.TMDB[0])
	}
}

func TestSearchRunsBothWordingsAndSaysSo(t *testing.T) {
	s, f, alice := start(t, testbase.BaseEnv(t))
	vocab, _ := json.Marshal(map[string]int{"kids": 700, "treasure": 800, "map": 900, "amp": 2, "with": 8000})
	s.Exec(t, `INSERT INTO app_settings (key, value) VALUES ('catalog_vocab', $1)`, string(vocab))
	r := search(t, alice, q("trasure amp"))
	if r.Fixed == nil || *r.Fixed != "treasure map" {
		t.Fatal(r.Fixed)
	}
	// Both wordings, searched at once (in either order).
	if qs := append([]string(nil), f.Meili.Queries...); len(qs) != 2 || !((qs[0] == "trasure amp" && qs[1] == "treasure map") ||
		(qs[1] == "trasure amp" && qs[0] == "treasure map")) {
		t.Fatal(f.Meili.Queries)
	}
	if r := search(t, alice, q("treasure map")); r.Fixed != nil {
		t.Fatal(*r.Fixed)
	}
}

// libraryHits answers the library index with hits (up to the asked limit
// and Meilisearch's most, maxHits).
func libraryHits(f *testenv.Fakes, hits []fakes.JSON, maxHits int) {
	f.Meili.Override("POST /indexes/library/search", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Limit int }
		_ = json.NewDecoder(r.Body).Decode(&body)
		fakes.Reply(w, fakes.JSON{"hits": hits[:min(len(hits), body.Limit, maxHits)]})
	})
}

func TestSearchShowsEveryMatchPageByPage(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	for n := 0; n < 70; n++ {
		f.Meili.Catalog = append(f.Meili.Catalog, cat(5000+n, fmt.Sprintf("Batman %d", n), 0.9-float64(n)*0.001, 1000))
	}
	var lib []fakes.JSON
	for n := 0; n < 30; n++ {
		lib = append(lib, fakes.JSON{"id": fmt.Sprintf("%032x", n), "tmdb_id": 3000 + n, "media_type": "movie",
			"title": fmt.Sprintf("Batman Library %d", n), "_rankingScore": 0.95 - float64(n)*0.001})
	}
	libraryHits(f, lib, 1000)
	f.TMDB.Override("/search/multi", func(w http.ResponseWriter, r *http.Request) {
		p := 1
		fmt.Sscan(r.URL.Query().Get("page"), &p)
		var people []fakes.JSON
		for n := (p - 1) * 15; n < p*15 && n < 30; n++ {
			people = append(people, fakes.JSON{"id": 900 + n, "media_type": "person", "name": fmt.Sprintf("Person %d", n)})
		}
		fakes.Reply(w, fakes.JSON{"results": people, "total_pages": 2})
	})
	one := search(t, alice, q("batman"))
	if len(one.Library) != 24 || len(one.TMDB) != 30 || len(one.People) != 15 || one.Page != 1 ||
		!one.More["library"] || !one.More["tmdb"] || !one.More["people"] {
		t.Fatalf("%d %d %d %v", len(one.Library), len(one.TMDB), len(one.People), one.More)
	}
	two := search(t, alice, q("batman", "page", "2"))
	if len(two.Library) != 30 || len(two.TMDB) != 60 || len(two.People) != 30 ||
		two.More["library"] || !two.More["tmdb"] || two.More["people"] {
		t.Fatalf("%d %d %d %v", len(two.Library), len(two.TMDB), len(two.People), two.More)
	}
	if strings.Join(field(two.Library, "key")[:24], ",") != strings.Join(field(one.Library, "key"), ",") {
		t.Fatal("page 2 does not start with page 1")
	}
	three := search(t, alice, q("batman", "page", "3"))
	if len(three.TMDB) != 70 || three.More["tmdb"] || three.Note != nil {
		t.Fatalf("%d %v %v", len(three.TMDB), three.More, three.Note)
	}
}

func TestSearchStopsAtTheLastPageAndReadsTMDBAFewPagesAtATime(t *testing.T) {
	s, f, alice := start(t, testbase.BaseEnv(t))
	// "batmn" is searched as typed and as "batman".
	s.Exec(t, `INSERT INTO app_settings (key, value) VALUES ('catalog_vocab', '{"batman": 1000}')`)
	var mu sync.Mutex
	var calls []string
	var running, most atomic.Int32
	f.TMDB.Override("/search/multi", func(w http.ResponseWriter, r *http.Request) {
		now := running.Add(1)
		for {
			m := most.Load()
			if now <= m || most.CompareAndSwap(m, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		mu.Lock()
		calls = append(calls, r.URL.Query().Get("query")+"#"+r.URL.Query().Get("page"))
		mu.Unlock()
		p := 0
		fmt.Sscan(r.URL.Query().Get("page"), &p)
		// TMDB reports more pages than the 500 it serves.
		fakes.Reply(w, fakes.JSON{"results": []fakes.JSON{{"id": 90000 + p, "media_type": "person", "name": fmt.Sprintf("Person %d", p)}},
			"total_pages": 10000})
	})
	for _, p := range []string{"1000000", "26", "500"} {
		r := alice.Do(t, "GET", "/api/v1/search?"+q("batmn", "page", p).Encode(), nil)
		if r.StatusCode != 422 || r.Map(t)["detail"] != "Page: Input should be less than or equal to 25." {
			t.Fatalf("page %s: %d %s", p, r.StatusCode, r.Body)
		}
	}
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	// Paging one "Show more" at a time: one new TMDB page per wording each step.
	for _, p := range []string{"1", "2", "3"} {
		search(t, alice, q("batmn", "page", p))
	}
	if len(calls) != 6 {
		t.Fatal(calls)
	}
	// The deepest page, nothing cached: 25 pages per wording, each once.
	calls = nil
	if err := s.KV.Do(context.Background(), s.KV.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	last := search(t, alice, q("batmn", "page", "25"))
	if len(calls) != 50 || most.Load() > 4 {
		t.Fatalf("%d calls, %d at once", len(calls), most.Load())
	}
	if len(last.People) != 25 || last.More["library"] || last.More["tmdb"] || last.More["people"] ||
		last.Note == nil || *last.Note != "That's as far as one search goes (page 25); add words to narrow it down." {
		t.Fatalf("%d %v %v", len(last.People), last.More, last.Note)
	}
	calls = nil
	again := search(t, alice, q("batmn", "page", "3"))
	if len(calls) != 0 || !again.More["people"] || again.Note != nil {
		t.Fatalf("%v %v %v", calls, again.More, again.Note)
	}
}

func TestASectionStoppedByMeilisearchSaysSo(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["SEARCH_MAX_HITS"] = "60"
	_, f, alice := start(t, env)
	var many []fakes.JSON
	for n := 0; n < 500; n++ {
		many = append(many, fakes.JSON{"id": fmt.Sprintf("%032x", n), "tmdb_id": 3000 + n, "media_type": "movie",
			"title": fmt.Sprintf("Film %d", n), "_rankingScore": 0.9})
	}
	libraryHits(f, many, 60)
	two := search(t, alice, q("film", "page", "2"))
	if len(two.Library) != 48 || !two.More["library"] || two.Note != nil {
		t.Fatalf("%d %v %v", len(two.Library), two.More, two.Note)
	}
	three := search(t, alice, q("film", "page", "3"))
	if len(three.Library) != 60 || three.More["library"] || three.Note == nil ||
		*three.Note != "These are the 60 best matches in your library (the most one search gives); add words to narrow it down." {
		t.Fatalf("%d %v %v", len(three.Library), three.More, three.Note)
	}
}

func TestALibrarySearchFailureLeavesTheRestWorking(t *testing.T) {
	// The deliberate degrade: one section down, the others still answer.
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.Meili.Fail("POST /indexes/library/search", 500)
	r := search(t, alice, q("Movie 777"))
	if len(r.Library) != 0 || r.TMDB[0]["title"] != "Movie 777" {
		t.Fatalf("%+v", r)
	}
}

func TestMDBListFindsWhatTheOthersMissed(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	f.TMDB.Override("/search/multi", func(w http.ResponseWriter, _ *http.Request) {
		fakes.Reply(w, fakes.JSON{"results": []fakes.JSON{}, "total_pages": 0})
	})
	r := search(t, alice, q("cheeky"))
	if strings.Join(field(r.TMDB, "key"), ",") != "movie:22705" {
		t.Fatal(field(r.TMDB, "key"))
	}
}

func TestASearchIsRefusedPastItsLengthInPlainWords(t *testing.T) {
	_, _, alice := start(t, testbase.BaseEnv(t))
	r := alice.Do(t, "GET", "/api/v1/search?q="+strings.Repeat("y", 201), nil)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "A search is too long: at most 200 characters." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := alice.Do(t, "GET", "/api/v1/search?q="+strings.Repeat("y", 200), nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	env := testbase.BaseEnv(t)
	env["SEARCH_MAX_CHARS"] = "50"
	_, _, bob := start(t, env)
	if r := bob.Do(t, "GET", "/api/v1/search/ai?q="+strings.Repeat("y", 51), nil); r.StatusCode != 422 ||
		r.Map(t)["detail"] != "A search is too long: at most 50 characters." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestAnEmptySearchFindsNothingWithoutAsking(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	r := search(t, alice, q("   "))
	if len(r.Library)+len(r.TMDB)+len(r.People) != 0 || r.First != "library" || len(f.TMDB.Calls()) != 0 {
		t.Fatalf("%+v", r)
	}
}

// ---------------------------------------------------------------------------
// The AI's best guess
// ---------------------------------------------------------------------------

func aiEnv(t *testing.T, ai *fakes.AI) testbase.Env {
	env := testbase.BaseEnv(t)
	env["LITELLM_API_KEY"], env["LITELLM_BASE_URL"], env["SEARCH_AI_MODEL"] = "test-key", ai.URL, "test-search-model"
	return env
}

var named = fakes.JSON{"matches": []fakes.JSON{{"title": "Movie Named", "year": 1985, "type": "movie", "why": "kids, a map, the treasure"},
	{"title": "", "type": "movie"}}}

func TestTheAIIsOffUnlessAModelIsSet(t *testing.T) {
	_, _, alice := start(t, testbase.BaseEnv(t))
	if r := search(t, alice, q("kids with a treasure map save their homes")); r.AI {
		t.Fatal("ai hint without a model")
	}
	r := alice.Do(t, "GET", "/api/v1/search/ai?q=kids+with+a+treasure+map", nil)
	if r.Body != `{"enabled":false,"matches":[],"note":null}` {
		t.Fatal(r.Body)
	}
}

func TestTheAINamesTheFilmForADescribedPlot(t *testing.T) {
	ai := fakes.NewAI(t, named)
	_, _, alice := start(t, aiEnv(t, ai))
	if !search(t, alice, q("kids with a treasure map save their homes")).AI {
		t.Fatal("no ai hint for a description")
	}
	if search(t, alice, q("Movie 777")).AI {
		t.Fatal("ai hint for a title")
	}
	var got struct {
		Enabled bool
		Matches []struct {
			Card map[string]any
			Why  string
		}
	}
	alice.Do(t, "GET", "/api/v1/search/ai?"+q("kids with a treasure map save their homes").Encode(), nil).JSON(t, &got)
	if !got.Enabled || len(got.Matches) != 1 || got.Matches[0].Card["title"] != "Movie Named" || got.Matches[0].Why != "kids, a map, the treasure" {
		t.Fatalf("%+v", got)
	}
	alice.Do(t, "GET", "/api/v1/search/ai?"+q("Kids with a treasure map, save their homes!").Encode(), nil)
	if n := ai.Count("POST /chat/completions"); n != 1 {
		t.Fatalf("the same description asked %d times", n)
	}
}

func TestTheAIHasNoDailyOrPerPersonLimit(t *testing.T) {
	ai := fakes.NewAI(t, named)
	s, _, alice := start(t, aiEnv(t, ai))
	bob := s.Client("bob", true)
	for n := 0; n < 60; n++ {
		who := alice
		if n%2 == 0 {
			who = bob
		}
		r := who.Do(t, "GET", "/api/v1/search/ai?"+q(fmt.Sprintf("description number %d of a film", n)).Encode(), nil)
		if !strings.Contains(r.Body, "Movie Named") {
			t.Fatalf("%d: %s", n, r.Body)
		}
	}
	if n := ai.Count("POST /chat/completions"); n != 60 {
		t.Fatal(n)
	}
}

// forgetGuesses is "a minute later" for the AI's cached guesses.
func forgetGuesses(t *testing.T, s *testenv.Server) {
	ctx := context.Background()
	keys, err := s.KV.Do(ctx, s.KV.B().Keys().Pattern("c:ai-guess:*").Build()).AsStrSlice()
	if err != nil || len(keys) == 0 {
		t.Fatal("no cached guess", err)
	}
	_ = s.KV.Do(ctx, s.KV.B().Del().Key(keys...).Build()).Error()
}

func TestAFailedOrEmptyAIAnswerIsNotKeptForADay(t *testing.T) {
	ai := fakes.NewAI(t, named)
	s, _, alice := start(t, aiEnv(t, ai))
	var calls atomic.Int32
	ai.Override("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(500)
		case 2:
			fakes.Reply(w, fakes.JSON{"choices": []fakes.JSON{{"message": fakes.JSON{"content": `{"matches": []}`}}}})
		default:
			fakes.Reply(w, fakes.JSON{"choices": []fakes.JSON{{"message": fakes.JSON{"content": `{"matches": [{"title": "Movie Named", "year": 1985, "type": "movie", "why": "it fits"}]}`}}}})
		}
	})
	path := "/api/v1/search/ai?" + q("kids with a treasure map save their homes").Encode()
	note := func() any { return alice.Do(t, "GET", path, nil).Map(t)["note"] }
	if n := note(); n != "The AI didn't answer this time. Search again to ask it again." {
		t.Fatal(n)
	}
	// Kept a minute, not a day: the cache entry expires after retryAfter.
	ttl, _ := s.KV.Do(context.Background(), s.KV.B().Keys().Pattern("c:ai-guess:*").Build()).AsStrSlice()
	pttl, _ := s.KV.Do(context.Background(), s.KV.B().Pttl().Key(ttl[0]).Build()).AsInt64()
	if pttl <= 0 || pttl > 61_000 {
		t.Fatalf("a failed answer is kept %d ms", pttl)
	}
	note()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	forgetGuesses(t, s)
	if n := note(); n != "The AI had no guess for this description." || calls.Load() != 2 {
		t.Fatal(n, calls.Load())
	}
	forgetGuesses(t, s)
	if r := alice.Do(t, "GET", path, nil); !strings.Contains(r.Body, "Movie Named") || !strings.Contains(r.Body, `"note":null`) {
		t.Fatal(r.Body)
	}
	alice.Do(t, "GET", path, nil) // a real answer is kept the day
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}

func TestADescriptionInAnyScriptGetsTheAI(t *testing.T) {
	_, _, alice := start(t, aiEnv(t, fakes.NewAI(t, named)))
	for _, text := range []string{"дети ищут карту сокровищ", "Παιδιά με χάρτη θησαυρού", "宝の地図を持った子供たちが家を救う", "보물 지도를 가진 아이들"} {
		if !search(t, alice, q(text)).AI {
			t.Errorf("%q", text)
		}
	}
	if search(t, alice, q("карта сокровищ")).AI {
		t.Fatal("two words got the AI")
	}
}

func TestAnAIThatRunsOutOfRoomSaysSo(t *testing.T) {
	ai := fakes.NewAI(t, named)
	env := aiEnv(t, ai)
	env["SEARCH_AI_MAX_TOKENS"] = "64"
	_, _, alice := start(t, env)
	var asked []float64
	ai.Override("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, body["max_tokens"].(float64))
		fakes.Reply(w, fakes.JSON{"choices": []fakes.JSON{{"message": fakes.JSON{"content": ""}, "finish_reason": "length"}}})
	})
	got := alice.Do(t, "GET", "/api/v1/search/ai?"+q("a reasoning model thinks for too long").Encode(), nil).Map(t)
	if len(asked) != 1 || asked[0] != 64 || len(got["matches"].([]any)) != 0 ||
		!strings.HasPrefix(got["note"].(string), "The AI ran out of room before it answered") {
		t.Fatalf("%v %v", asked, got)
	}
}

// ---------------------------------------------------------------------------
// People, poster fill, images
// ---------------------------------------------------------------------------

func TestAPersonsPageListsTheirTitles(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	r := alice.Do(t, "GET", "/api/v1/person/5", nil).Map(t)
	items := r["items"].([]any)
	if r["name"] != "Some Person" || len(items) != 1 || items[0].(map[string]any)["key"] != "movie:603" {
		t.Fatal(r)
	}
	alice.Do(t, "GET", "/api/v1/person/5", nil)
	if n := f.TMDB.Count("GET /person/"); n != 1 {
		t.Fatalf("person asked %d times", n)
	}
	f.TMDB.Fail("/person/6", 404)
	if r := alice.Do(t, "GET", "/api/v1/person/6", nil); r.StatusCode != 404 || r.Map(t)["detail"] != "Person not found" {
		t.Fatal(r.StatusCode, r.Body)
	}
	f.TMDB.Fail("/person/7", 500)
	if r := alice.Do(t, "GET", "/api/v1/person/7", nil); r.StatusCode != 502 || r.Map(t)["detail"] != "Couldn't reach TMDB" {
		t.Fatal(r.StatusCode, r.Body)
	}
	if r := alice.Do(t, "GET", "/api/v1/person/0", nil); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
}

func TestFillGivesPostersAndAsksTMDBForABoundedNumber(t *testing.T) {
	_, f, alice := start(t, testbase.BaseEnv(t))
	r := alice.Do(t, "POST", "/api/v1/titles/fill", `{"keys": ["movie:603", "tv:1399", "movie:9000005"]}`)
	var got struct{ Titles map[string]map[string]any }
	r.JSON(t, &got)
	if got.Titles["movie:603"]["poster"] != "/p603.jpg" || got.Titles["tv:1399"]["title"] != "Show 1399" ||
		got.Titles["movie:603"]["year"] != float64(2000) || got.Titles["movie:603"]["vote"] != 7.0 {
		t.Fatal(r.Body)
	}
	// 100 unknown keys: at most 60 TMDB calls (the Python made 100).
	f.TMDB.Reset()
	var keys []string
	for n := 0; n < 100; n++ {
		keys = append(keys, fmt.Sprintf(`"movie:%d"`, 10000+n))
	}
	r = alice.Do(t, "POST", "/api/v1/titles/fill", `{"keys": [`+strings.Join(keys, ",")+`]}`)
	got.Titles = nil
	r.JSON(t, &got)
	if n := f.TMDB.Count("GET /movie/"); n != 60 || len(got.Titles) != 60 {
		t.Fatalf("%d TMDB calls, %d titles", n, len(got.Titles))
	}
	if r := alice.Do(t, "POST", "/api/v1/titles/fill", `{"keys": ["film:1"]}`); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
}

func TestFillTakesAtMostBatchMaxItemsKeys(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["BATCH_MAX_ITEMS"] = "2"
	_, _, alice := start(t, env)
	r := alice.Do(t, "POST", "/api/v1/titles/fill", `{"keys": ["movie:1", "movie:2", "movie:3"]}`)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "Keys: at most 2 at a time." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestTheJellyfinImageProxy(t *testing.T) {
	srv, f, alice := start(t, testbase.BaseEnv(t))
	id := strings.Repeat("a", 32)
	if r := srv.Client("", false).Do(t, "GET", "/api/v1/img/jf/"+id, nil); r.StatusCode != 401 {
		t.Fatal("an anonymous caller got an image", r.StatusCode)
	}
	f.Jellyfin.Override("/Items/"+id+"/Images/Primary", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maxWidth") != "300" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("RIFF....WEBP"))
	})
	r := alice.Do(t, "GET", "/api/v1/img/jf/"+id+"?w=300", nil)
	if r.StatusCode != 200 || r.Body != "RIFF....WEBP" || r.Header.Get("Content-Type") != "image/webp" ||
		r.Header.Get("Cache-Control") != "private, max-age=604800" {
		t.Fatalf("%d %q %v", r.StatusCode, r.Body, r.Header)
	}
	if r := alice.Do(t, "GET", "/api/v1/img/jf/"+strings.Repeat("b", 32), nil); r.StatusCode != 404 || r.Map(t)["detail"] != "No image" {
		t.Fatal(r.StatusCode, r.Body)
	}
	f.Jellyfin.Override("/Items/"+strings.Repeat("c", 32)+"/Images/Backdrop", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	})
	if r := alice.Do(t, "GET", "/api/v1/img/jf/"+strings.Repeat("c", 32)+"?kind=Backdrop", nil); r.StatusCode != 502 || r.Map(t)["detail"] != "Not an image" {
		t.Fatal(r.StatusCode, r.Body)
	}
	for _, bad := range []string{"/api/v1/img/jf/xyz", "/api/v1/img/jf/" + id + "?kind=Logo", "/api/v1/img/jf/" + id + "?w=5000"} {
		if r := alice.Do(t, "GET", bad, nil); r.StatusCode != 422 && r.StatusCode != 404 {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
}
