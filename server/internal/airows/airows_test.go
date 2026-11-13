package airows

// tests/test_ai_rows.py, as Go behaviour tests against the real Postgres
// and Valkey, with LiteLLM and TMDB stood in by httptest servers. Inside the
// package: the worker's runOne is what a worker's queue loop calls.

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/titles"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fixture struct {
	svc      *Service
	user     int32
	aiCalls  atomic.Int32
	aiFails  atomic.Bool
	tmdbNone atomic.Bool
}

func setup(t *testing.T, extra map[string]string) *fixture {
	t.Helper()
	pool, kv := testbase.Conns(t)
	f := &fixture{}
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.aiCalls.Add(1)
		if f.aiFails.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		var rows []map[string]any
		for n := 0; n < 6; n++ {
			var titles []string
			for i := 0; i < 3; i++ {
				titles = append(titles, fmt.Sprintf("Film %d-%d (1999)", n, i))
			}
			rows = append(rows, map[string]any{"theme": fmt.Sprintf("Theme %d", n), "titles": titles})
		}
		content, _ := json.Marshal(map[string]any{"rows": rows})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(ai.Close)
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if f.tmdbNone.Load() || q == "" {
			_, _ = w.Write([]byte(`{"results": []}`))
			return
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(q))
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{
			"id": int(h.Sum32()%1_000_000) + 1, "title": q, "release_date": "1999-05-01", "poster_path": "/p.jpg"}}})
	}))
	t.Cleanup(tmdb.Close)
	env := testbase.BaseEnv(t)
	env["LITELLM_API_KEY"], env["LITELLM_MODEL"], env["LITELLM_BASE_URL"] = "k", "test-model", ai.URL
	for k, v := range extra {
		env[k] = v
	}
	cfg := env.Config(t)
	cl := clients.NewSet(cfg, kv, quiet)
	cl.TMDB = clients.NewTMDB(http.DefaultClient, tmdb.URL, "k", "en-US", "US", false)
	f.svc = New(cfg, pool, kv, cl, titles.New(store.New(pool), func() titles.Fetcher { return cl.TMDB }, quiet), quiet)
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (username, is_admin, created_at) VALUES ('alice', false, now()) RETURNING id`).Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	return f
}

// worker runs what a worker's queue loop would: every due request.
func (f *fixture) worker(t *testing.T) {
	t.Helper()
	due, err := f.svc.queue.Due(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range due {
		f.svc.runOne(context.Background(), m)
	}
}

func (f *fixture) note(t *testing.T) string {
	t.Helper()
	rows, _, err := f.svc.Serve(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.Note(context.Background(), f.user, len(rows) > 0)
	if err != nil {
		t.Fatal(err)
	}
	if n == nil {
		return ""
	}
	return *n
}

func TestAFailedRebuildBacksOffAndHomeSaysWhy(t *testing.T) {
	f := setup(t, nil)
	f.aiFails.Store(true)
	if n := f.note(t); !strings.Contains(n, "being made") {
		t.Fatalf("first view: %q", n)
	}
	// Many Home views on many replicas before a worker gets to it: one
	// request, one model call.
	for i := 0; i < 5; i++ {
		f.note(t)
	}
	f.worker(t)
	if f.aiCalls.Load() != 1 {
		t.Fatalf("%d model calls", f.aiCalls.Load())
	}
	var n string
	for i := 0; i < 5; i++ { // views during the back-off ask for nothing
		n = f.note(t)
		f.worker(t)
	}
	if f.aiCalls.Load() != 1 {
		t.Fatalf("asked again during the back-off: %d calls", f.aiCalls.Load())
	}
	if !strings.Contains(n, "couldn't be made just now: the AI didn't answer") || !strings.Contains(n, "in about 10 minutes") {
		t.Fatalf("note %q", n)
	}
	// Once the wait is over it is tried again; a second failure waits twice as long.
	f.svc.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	f.note(t)
	f.worker(t)
	if f.aiCalls.Load() != 2 {
		t.Fatalf("%d calls after the wait", f.aiCalls.Load())
	}
	if n := f.note(t); !strings.Contains(n, "in about 20 minutes") {
		t.Fatalf("second failure note %q", n)
	}
	// ...never more than RetryMax.
	for i := 0; i < 12; i++ {
		f.svc.failed(context.Background(), f.user, "the AI didn't answer")
	}
	fl, _ := f.svc.failureOf(context.Background(), f.user)
	if wait := fl.Until.Sub(f.svc.now()); wait > RetryMax || wait < RetryMax-time.Minute {
		t.Fatalf("back-off %s, want at most %s", wait, RetryMax)
	}
}

func TestPicksTMDBDoesNotKnowCountAsAFailureAndStoreNothing(t *testing.T) {
	f := setup(t, nil)
	f.tmdbNone.Store(true)
	f.note(t)
	f.worker(t)
	if n := f.note(t); !strings.Contains(n, "none of the AI's picks could be found on TMDB") {
		t.Fatalf("note %q", n)
	}
	var count int
	_ = f.svc.pool.QueryRow(context.Background(), "SELECT count(*) FROM ai_row_cache").Scan(&count)
	if count != 0 {
		t.Fatalf("%d rows stored", count)
	}
}

func TestAGoodRebuildReplacesTheRowsBeforeAndSaysWhatItRead(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	for n := 0; n < 70; n++ {
		if _, err := f.svc.pool.Exec(ctx, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
			VALUES ($1, 'movie', $2, $3, $4, 'manual')`, f.user, 600+n, fmt.Sprintf("Movie %d", 600+n),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)); err != nil {
			t.Fatal(err)
		}
	}
	f.note(t)
	f.worker(t)
	rows, basis, err := f.svc.Serve(ctx, f.user)
	if err != nil || len(rows) != 6 || len(rows[0].Keys) != 3 {
		t.Fatalf("rows %v %v", rows, err)
	}
	if n := f.note(t); n != "" {
		t.Fatalf("note with rows: %q", n)
	}
	if got := BasisLine(basis); got == nil || *got != "From your 60 latest of 70 films" {
		t.Fatalf("basis line %v", got)
	}
	if _, err := f.svc.Generate(ctx, f.user); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = f.svc.pool.QueryRow(ctx, "SELECT count(*) FROM ai_row_cache WHERE user_id = $1", f.user).Scan(&count)
	if count != 1 || f.aiCalls.Load() != 2 {
		t.Fatalf("%d cached rows after two builds, %d calls", count, f.aiCalls.Load())
	}
	// Fresh rows ask for nothing; a few new watches past the setting do.
	if _, _, err := f.svc.Serve(ctx, f.user); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.queue.Pending(ctx, fmt.Sprint(f.user)); p {
		t.Fatal("fresh rows were rebuilt")
	}
	for i := 0; i < 3; i++ {
		_, _ = f.svc.pool.Exec(ctx, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
			VALUES ($1, 'movie', $2, 'New', now(), 'manual')`, f.user, 900+i)
	}
	_, _, _ = f.svc.Serve(ctx, f.user)
	if p, _ := f.svc.queue.Pending(ctx, fmt.Sprint(f.user)); !p {
		t.Fatal("three new watches did not ask for new rows")
	}
}

func TestWhatTheAIReadsIsASettingAndSaid(t *testing.T) {
	f := setup(t, map[string]string{"AI_PROMPT_TITLES": "10", "AI_PROMPT_COMMENT_CHARS": "50"})
	ctx := context.Background()
	for n := 0; n < 25; n++ {
		_, _ = f.svc.pool.Exec(ctx, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
			VALUES ($1, 'movie', $2, $3, $4, 'manual')`, f.user, 700+n, fmt.Sprintf("Movie %d", 700+n),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n))
	}
	for _, r := range []struct {
		id     int
		rating int
		review string
	}{{603, 9, strings.Repeat("long ", 30)}, {604, 7, "short one"}} {
		_, _ = f.svc.pool.Exec(ctx, `INSERT INTO titles (key, media_type, tmdb_id, title, detailed, fetched_at)
			VALUES ($1, 'movie', $2, $3, true, now())`, fmt.Sprintf("movie:%d", r.id), r.id, fmt.Sprintf("Movie %d", r.id))
		_, _ = f.svc.pool.Exec(ctx, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, review, rated_at, source)
			VALUES ($1, 'movie', $2, $3, $4, now(), 'app')`, f.user, r.id, r.rating, r.review)
	}
	prompt, basis, err := f.svc.historySummary(ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Movie 724") || strings.Contains(prompt, "Movie 714,") {
		t.Fatalf("not the 10 latest: %s", prompt)
	}
	if !strings.Contains(prompt, strings.TrimSpace(strings.Repeat("long ", 10))+"…") || !strings.Contains(prompt, "short one") {
		t.Fatalf("comments: %s", prompt)
	}
	want := "From your 10 latest of 25 films, your 2 film ratings and your 2 comments (1 shortened to its first 50 characters)"
	b, _ := json.Marshal(basis)
	var round map[string]any // as read back from the cache (JSON numbers)
	_ = json.Unmarshal(b, &round)
	if got := BasisLine(round); got == nil || *got != want {
		t.Fatalf("basis line %v, want %q", got, want)
	}
}

func TestParseResponseKeepsTheShapeAndRefusesNothingUsable(t *testing.T) {
	long := strings.Repeat("x", 90)
	got, err := ParseResponse(map[string]any{"rows": []any{
		map[string]any{"theme": long, "titles": []any{"A (2000)", " ", 3}},
		map[string]any{"theme": "", "titles": []any{"B"}},
		"junk",
	}})
	if err != nil || len(got) != 1 || len([]rune(got[0].Theme)) != 81 || len(got[0].Titles) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseResponse(map[string]any{"rows": []any{}}); err == nil {
		t.Fatal("no rows accepted")
	}
	if name, year := SplitTitleYear("Heat (1995)"); name != "Heat" || year != 1995 {
		t.Fatalf("%q %d", name, year)
	}
}

func TestTheBasisLineSaysEachPartPlainly(t *testing.T) {
	b := map[string]any{"films": 1.0, "films_total": 1.0, "ratings": 2.0, "ratings_total": 2.0, "comments": 2.0,
		"comments_total": 2.0, "comments_shortened": 1.0, "comment_chars": 50.0}
	if got := *BasisLine(b); got != "From your 1 film, your 2 film ratings and your 2 comments (1 shortened to its first 50 characters)" {
		t.Fatal(got)
	}
	b = map[string]any{"films": 60.0, "films_total": 340.0, "ratings": 60.0, "ratings_total": 210.0, "comments": 20.0,
		"comments_total": 45.0, "comments_shortened": 3.0, "comment_chars": 300.0}
	if got := *BasisLine(b); got != "From your 60 latest of 340 films, your 60 best of 210 film ratings and your 20 latest of 45 comments (3 shortened to their first 300 characters)" {
		t.Fatal(got)
	}
	if BasisLine(nil) != nil || BasisLine(map[string]any{"films": 0.0, "films_total": 0.0}) != nil {
		t.Fatal("no basis, no line")
	}
}
