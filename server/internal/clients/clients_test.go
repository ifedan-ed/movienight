package clients_test

// The outside-service clients against fake upstreams (httptest): what each
// does with a not-found, a refusal, a failure and a partial answer.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/testbase"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func upstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestTMDBNotFoundRetryAndRateLimit(t *testing.T) {
	var calls atomic.Int32
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch {
		case r.URL.Path == "/movie/1":
			w.WriteHeader(404)
		case r.URL.Path == "/movie/2" && n%3 != 0: // twice 429, then the answer
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		case r.URL.Path == "/movie/2":
			fmt.Fprint(w, `{"id": 2, "title": "Two"}`)
		case r.URL.Path == "/movie/3":
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		case r.URL.Path == "/search/multi":
			if r.URL.Query().Get("api_key") != "k" || r.URL.Query().Get("include_adult") != "true" {
				t.Errorf("query %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"results": [{"id": 1, "adult": true}, {"id": 2}], "total_pages": 7}`)
		}
	})
	ctx := context.Background()
	tm := clients.NewTMDB(http.DefaultClient, s.URL, "k", "en-US", "US", true)
	if got, err := tm.Basic(ctx, "movie", 1); got != nil || err != nil {
		t.Fatal("404:", got, err)
	}
	calls.Store(0)
	got, err := tm.Basic(ctx, "movie", 2)
	if err != nil || got["title"] != "Two" || got["media_type"] != "movie" {
		t.Fatal("retry:", got, err)
	}
	// The Python answered None after a third 429, which read as "not found".
	var te *clients.TMDBError
	if _, err := tm.Basic(ctx, "movie", 3); !errors.As(err, &te) || te.Status != 429 {
		t.Fatal("rate limited:", err)
	}
	rows, pages, err := tm.SearchMultiPage(ctx, "x", 1)
	if err != nil || len(rows) != 1 || pages != 7 { // HIDE_ADULT_TITLES drops the adult one
		t.Fatal(rows, pages, err)
	}
}

func TestMDBListLeavesAFailingOrRefusingServiceAloneAcrossReplicas(t *testing.T) {
	_, kv := testbase.Conns(t)
	var calls atomic.Int32
	status := atomic.Int32{}
	status.Store(500)
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			fmt.Fprint(w, `[{"id": 5, "name": "Best", "mediatype": "show"}]`)
		} else if status.Load() == 403 {
			fmt.Fprint(w, `{"error": "API Limit Reached!"}`)
		}
	})
	ctx := context.Background()
	one := clients.NewMDBList(http.DefaultClient, s.URL, "k", kv, quiet)
	two := clients.NewMDBList(http.DefaultClient, s.URL, "k", kv, quiet) // another replica
	if _, err := one.TopLists(ctx); err == nil {
		t.Fatal("a 500 was not an error")
	}
	if _, err := two.TopLists(ctx); err == nil || calls.Load() != 1 {
		t.Fatal("the other replica called the failing service again:", calls.Load(), err)
	}
	// Refusal: recorded for admins, and said as MDBList's own limit.
	_ = kv.Do(ctx, kv.B().Flushdb().Build()).Error()
	status.Store(403)
	var refused *clients.MDBListRefused
	if _, err := one.TopLists(ctx); !errors.As(err, &refused) {
		t.Fatal(err)
	}
	if r, _ := two.Refusal(ctx); r == nil || r["why"] != "HTTP 403" {
		t.Fatal("refusal not shared:", r)
	}
	if _, err := two.TopLists(ctx); !errors.As(err, &refused) || calls.Load() != 2 {
		t.Fatal("refused calls continued:", calls.Load(), err)
	}
	// A good answer clears the refusal.
	_ = kv.Do(ctx, kv.B().Del().Key("breaker:mdblist").Build()).Error()
	status.Store(200)
	lists, err := one.TopLists(ctx)
	if err != nil || len(lists) != 1 || lists[0]["media"] != "tv" {
		t.Fatal(lists, err)
	}
	if r, _ := one.Refusal(ctx); r != nil {
		t.Fatal("refusal kept after a good answer")
	}
}

func TestMDBListHelpers(t *testing.T) {
	for in, want := range map[string]clients.ListRef{
		"12345":                                 {ID: "12345"},
		"mdblist.com/lists/linaspurinis/top":    {User: "linaspurinis", Slug: "top"},
		"https://www.mdblist.com/lists/a/b?x=1": {User: "a", Slug: "b"},
	} {
		if got, ok := clients.ParseListURL(in); !ok || got != want {
			t.Errorf("%s: %+v", in, got)
		}
	}
	if _, ok := clients.ParseListURL("https://evil.example/lists/a/b"); ok {
		t.Error("another host was accepted")
	}
	r := clients.RatingsOf(clients.JSON{"score": 71.0, "ratings": []any{
		map[string]any{"source": "imdb", "value": 7.5, "votes": 1000.0},
		map[string]any{"source": "tomatoes", "score": 88.0, "url": "/m/x"},
		map[string]any{"source": "trakt", "value": 78.0, "votes": 5.0},
	}})
	if r["imdb_rating"] != 7.5 || r["rt_critics"] != 88 || r["rt_url"] != "https://www.rottentomatoes.com/m/x" || r["trakt_rating"] != 7.8 {
		t.Fatal(r)
	}
}

func jellyfinPages(t *testing.T, pages func(start int) (items []string, total int)) *clients.Jellyfin {
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), `MediaBrowser Client="Movie Night"`) {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		var start int
		fmt.Sscan(r.URL.Query().Get("StartIndex"), &start)
		items, total := pages(start)
		var parts []string
		for _, id := range items {
			parts = append(parts, `{"Id":"`+id+`"}`)
		}
		fmt.Fprintf(w, `{"Items":[%s],"TotalRecordCount":%d}`, strings.Join(parts, ","), total)
	})
	return clients.NewJellyfin(http.DefaultClient, http.DefaultClient, s.URL, s.URL, "key", false)
}

func TestJellyfinWholeListReadsAreWholeOrRefused(t *testing.T) {
	ctx := context.Background()
	ok := jellyfinPages(t, func(start int) ([]string, int) {
		if start == 0 {
			return []string{"a", "b"}, 3
		}
		return []string{"c"}, 3
	})
	if items, err := ok.Played(ctx, "u", ""); err != nil || len(items) != 3 {
		t.Fatal(items, err)
	}
	var inc *clients.IncompleteRead
	changed := jellyfinPages(t, func(start int) ([]string, int) {
		if start == 0 {
			return []string{"a", "b"}, 3
		}
		return []string{"c"}, 4
	})
	if _, err := changed.Played(ctx, "u", ""); !errors.As(err, &inc) || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	twice := jellyfinPages(t, func(start int) ([]string, int) { return []string{"a", "b"}, 3 })
	if _, err := twice.Played(ctx, "u", ""); !errors.As(err, &inc) || !strings.Contains(err.Error(), "twice") {
		t.Fatal(err)
	}
	short := jellyfinPages(t, func(start int) ([]string, int) {
		if start == 0 {
			return []string{"a"}, 3
		}
		return nil, 3
	})
	if _, err := short.Played(ctx, "u", ""); !errors.As(err, &inc) || !strings.Contains(err.Error(), "stopped") {
		t.Fatal(err)
	}
}

func TestJellyfinLinks(t *testing.T) {
	j := clients.NewJellyfin(http.DefaultClient, http.DefaultClient, "http://jf:8096", "https://jf.example", "key", false)
	if got := j.StreamURL("i1", "m1", "mkv,webm", "tok"); got != "https://jf.example/Videos/i1/stream.mkv?api_key=tok&mediaSourceId=m1&static=true" {
		t.Fatal(got)
	}
	if got := clients.AuthHeader("t", "dev", "Movie Night"); got != `MediaBrowser Client="Movie Night", Device="Movie Night", DeviceId="dev", Version="2.0", Token="t"` {
		t.Fatal(got)
	}
}

func TestSeerrDryRunSendsNothing(t *testing.T) {
	var calls atomic.Int32
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(409) })
	ctx := context.Background()
	dry := clients.NewSeerr(http.DefaultClient, s.URL, "k", true)
	if out, err := dry.CreateRequest(ctx, 603, "movie", 0, nil); err != nil || out["dry_run"] != true || calls.Load() != 0 {
		t.Fatal(out, err, calls.Load())
	}
	live := clients.NewSeerr(http.DefaultClient, s.URL, "k", false)
	if out, err := live.CreateRequest(ctx, 603, "movie", 7, nil); err != nil || out["already"] != true {
		t.Fatal(out, err)
	}
	off := clients.NewSeerr(http.DefaultClient, "", "", false)
	var se *clients.SeerrError
	if _, err := off.MediaInfo(ctx, "movie", 1); !errors.As(err, &se) || se.Status != 503 {
		t.Fatal(err)
	}
}

// DRY_RUN: nothing that changes Jellyfin or Seerr is sent (played marks,
// signing a session out, creating a Seerr user); reads still are.
func TestDryRunChangesNothingOutside(t *testing.T) {
	var writes, reads atomic.Int32
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads.Add(1)
			fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
			return
		}
		writes.Add(1)
		fmt.Fprint(w, `[]`)
	})
	ctx := context.Background()
	for _, dry := range []bool{true, false} {
		writes.Store(0)
		j := clients.NewJellyfin(http.DefaultClient, http.DefaultClient, s.URL, s.URL, "key", dry)
		_ = j.MarkPlayed(ctx, "u", "i", time.Now())
		_ = j.MarkUnplayed(ctx, "u", "i")
		_ = j.SignOut(ctx, "tok", "dev")
		_, _ = clients.NewSeerr(http.DefaultClient, s.URL, "k", dry).ImportJellyfinUser(ctx, "u")
		if want := map[bool]int32{true: 0, false: 4}[dry]; writes.Load() != want {
			t.Fatalf("dry=%v: %d writes, want %d", dry, writes.Load(), want)
		}
	}
	j := clients.NewJellyfin(http.DefaultClient, http.DefaultClient, s.URL, s.URL, "key", true)
	if _, err := j.Played(ctx, "u", ""); err != nil || reads.Load() == 0 {
		t.Fatal("reads must still go out under DRY_RUN", err)
	}
}

func TestMeiliFallsBackToWordsWhileTheEmbedderLoads(t *testing.T) {
	var hybrid atomic.Int32
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"hybrid"`) {
			hybrid.Add(1)
			w.WriteHeader(400)
			fmt.Fprint(w, `{"code":"invalid_embedder"}`)
			return
		}
		fmt.Fprint(w, `{"hits":[{"id":"x"}]}`)
	})
	m := clients.NewMeili(http.DefaultClient, s.URL, "", "bge", 1000, quiet)
	hits, err := m.Search(context.Background(), "q", clients.SearchOptions{Index: clients.LibraryIndex, SemanticRatio: 0.8, Limit: 5})
	if err != nil || len(hits) != 1 || hybrid.Load() != 1 {
		t.Fatal(hits, err)
	}
	doc := clients.LibraryDoc(clients.JSON{"Id": "i", "Name": "Heat", "ProductionYear": 1995.0, "Genres": []any{"Crime"},
		"Overview": "LA.", "ProviderIds": map[string]any{"Tmdb": "949"}, "Type": "Movie",
		"People": []any{map[string]any{"Name": "Al Pacino"}, map[string]any{"Name": "Al Pacino"}}})
	if doc["searchText"] != "Heat | (1995) | Crime | LA. | Cast/crew: Al Pacino" || doc["tmdb_id"] != 949 {
		t.Fatal(doc)
	}
}

func TestLiteLLMParsesRetriesAndSaysWhenOutOfRoom(t *testing.T) {
	replies := []string{
		`{"choices":[{"message":{"content":"not json"},"finish_reason":"length"}]}`,
		`{"choices":[{"message":{"content":"thinking..."},"finish_reason":"length"}]}`,
	}
	var n atomic.Int32
	s := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		i := int(n.Add(1)) - 1
		if i < len(replies) {
			fmt.Fprint(w, replies[i])
			return
		}
		fmt.Fprint(w, "{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"rows\\\": [1]}\\n```\"}}]}")
	})
	l := clients.NewLiteLLM(http.DefaultClient, s.URL+"/v1", "k", "m", 100, quiet)
	var room *clients.AIOutOfRoom
	if _, err := l.ChatJSON(context.Background(), "sys", "user", 2); !errors.As(err, &room) {
		t.Fatal(err)
	}
	out, err := l.ChatJSON(context.Background(), "sys", "user", 2)
	if err != nil || fmt.Sprint(out) != "map[rows:[1]]" {
		t.Fatal(out, err)
	}
	if _, err := clients.NewLiteLLM(http.DefaultClient, s.URL, "", "", 1, quiet).ChatJSON(context.Background(), "", "", 1); !errors.Is(err, clients.ErrAIOff) {
		t.Fatal(err)
	}
	if v, err := clients.ParseJSON(`Sure! {"a": 1} hope that helps`); err != nil || fmt.Sprint(v) != "map[a:1]" {
		t.Fatal(v, err)
	}
}
