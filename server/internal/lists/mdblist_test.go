package lists_test

// MDBList lists kept in step (the worker's nightly mdblist job and the
// admin's "Sync now"), and the nightly copy of MDBList's top lists:
// tests/test_outside_services.py (followed lists, the nightly copy) and
// tests/test_lists.py (follow and sync), against a stub MDBList.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/clients"
	"movienight/server/internal/homecfg"
	"movienight/server/internal/lists"
	"movienight/server/internal/settings"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/titles"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeMDBList answers like MDBList for list 2194 ("Latest TV Shows").
type fakeMDBList struct {
	items  atomic.Value // the items answer, as JSON
	status atomic.Int32 // non-zero: every call answers this status
	calls  atomic.Int32
}

func newFake(t *testing.T) (*fakeMDBList, *httptest.Server) {
	f := &fakeMDBList{}
	f.items.Store(`[{"id":1399,"title":"Show 1399","mediatype":"show","release_year":2011},
		{"ids":{"tmdb":603},"title":"Movie 603","mediatype":"movie","release_year":2000},
		{"title":"No id here","mediatype":"movie"},
		{"id":1399,"title":"Show 1399 again","mediatype":"show"}]`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if s := f.status.Load(); s != 0 {
			w.WriteHeader(int(s))
			_, _ = w.Write([]byte(`{"error":"API limit reached"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/lists/top":
			_, _ = w.Write([]byte(`[{"id":2194,"name":"Latest TV Shows","slug":"latest-tv-shows","user_name":"gary","items":2,"likes":40},
				{"name":"No id"}]`))
		case "/lists/2194":
			_, _ = w.Write([]byte(`[{"id":2194,"name":"Latest TV Shows (renamed)","slug":"latest-tv-shows","user_name":"gary","likes":41,"description":"New shows"}]`))
		case "/lists/2194/items":
			_, _ = w.Write([]byte(f.items.Load().(string)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// syncer is the lists service with the stub MDBList as its source.
type syncer struct {
	*lists.Service
	mdb *clients.MDBList
}

func (s syncer) syncAll(ctx context.Context) (int, error)     { return s.SyncAll(ctx, s.mdb) }
func (s syncer) refreshCopy(ctx context.Context) (int, error) { return s.RefreshCopy(ctx, s.mdb) }

func setup(t *testing.T) (syncer, *fakeMDBList, *pgxpool.Pool) {
	pool, kv := testbase.Conns(t)
	f, srv := newFake(t)
	q := store.New(pool)
	st := settings.New(q)
	tt := titles.New(q, func() titles.Fetcher { return nil }, quiet)
	svc := lists.New(pool, q, tt, st, homecfg.New(q, st), quiet)
	return syncer{svc, clients.NewMDBList(srv.Client(), srv.URL, "key", kv, quiet)}, f, pool
}

// followed adds a followed MDBList list with the given items.
func followed(t *testing.T, pool *pgxpool.Pool, originID string, items ...string) int32 {
	t.Helper()
	ctx := context.Background()
	var id int32
	if err := pool.QueryRow(ctx, `INSERT INTO lists (name, kind, source, created_at, origin, origin_id)
		VALUES ('Latest TV Shows', 'list', 'follow', now(), 'mdblist', $1) RETURNING id`, originID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for i, it := range items {
		media, tid, _ := strings.Cut(it, ":")
		if _, err := pool.Exec(ctx, `INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at)
			VALUES ($1, $2, $3::int, $4, now())`, id, media, tid, i); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func items(t *testing.T, pool *pgxpool.Pool, listID int32) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT media_type || ':' || tmdb_id FROM list_items WHERE list_id = $1 ORDER BY position`, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func TestAFollowedListTakesTheOriginalsTitlesAndDetails(t *testing.T) {
	s, _, pool := setup(t)
	ctx := context.Background()
	id := followed(t, pool, "2194", "movie:1", "movie:603")
	n, err := s.syncAll(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// In MDBList's order, each title once; the one without a TMDB id counted.
	if got := strings.Join(items(t, pool, id), ","); got != "tv:1399,movie:603" {
		t.Fatal(got)
	}
	var name string
	var likes, notShown *int32
	var desc *string
	if err := pool.QueryRow(ctx, `SELECT name, likes, description, not_shown FROM lists WHERE id = $1 AND synced_at IS NOT NULL`, id).
		Scan(&name, &likes, &desc, &notShown); err != nil {
		t.Fatal(err)
	}
	if name != "Latest TV Shows (renamed)" || *likes != 41 || *desc != "New shows" || *notShown != 1 {
		t.Fatal(name, *likes, *desc, *notShown)
	}
	// The names seed the title cache.
	var title string
	var year int32
	if err := pool.QueryRow(ctx, `SELECT title, year FROM titles WHERE key = 'tv:1399'`).Scan(&title, &year); err != nil ||
		title != "Show 1399" || year != 2011 {
		t.Fatal(title, year, err)
	}
}

func TestAFollowedListKeepsItsTitlesWhenMDBListCantSendThem(t *testing.T) {
	s, f, pool := setup(t)
	ctx := context.Background()
	id := followed(t, pool, "2194", "tv:1399", "movie:603")

	f.items.Store(`[]`)
	l, err := store.New(pool).GetList(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var refusal *apierr.Error
	if _, err := s.Sync(ctx, s.mdb, &l); !errors.As(err, &refusal) || refusal.Status != http.StatusBadGateway {
		t.Fatal(err)
	}
	f.status.Store(http.StatusBadGateway)
	if n, err := s.syncAll(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if got := len(items(t, pool, id)); got != 2 {
		t.Fatalf("%d titles left", got)
	}
}

func TestOnceMDBListRefusesTheRestWaitForTheNextRun(t *testing.T) {
	s, f, pool := setup(t)
	followed(t, pool, "2194")
	followed(t, pool, "2195")
	followed(t, pool, "2196")
	f.status.Store(http.StatusTooManyRequests)
	if n, err := s.syncAll(context.Background()); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	// One refused call, then the shared breaker answers for MDBList: the
	// other two lists cost no request at all.
	if c := f.calls.Load(); c != 1 {
		t.Fatalf("%d calls to MDBList", c)
	}
}

func copyOf(t *testing.T, pool *pgxpool.Pool, key string) map[string]any {
	t.Helper()
	var raw string
	if err := pool.QueryRow(context.Background(), "SELECT value FROM app_settings WHERE key = $1", key).Scan(&raw); err != nil {
		t.Fatal(key, err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheNightlyCopyTakesEveryTopListAndItsTitles(t *testing.T) {
	s, f, pool := setup(t)
	ctx := context.Background()
	// A summary the API saved earlier stays alongside what the copy adds.
	if _, err := pool.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ('mdblist:copy:list:2194', '{"seen_by_api": true}')`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.refreshCopy(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	top := copyOf(t, pool, "mdblist:copy:top")["lists"].([]any)
	if top[0].(map[string]any)["mdblist_id"].(float64) != 2194 {
		t.Fatal(top)
	}
	kept := copyOf(t, pool, "mdblist:copy:list:2194")
	entries, _ := json.Marshal(kept["entries"])
	if string(entries) != `[["tv",1399,"Show 1399",2011],["movie",603,"Movie 603",2000],["tv",1399,"Show 1399 again",null]]` ||
		kept["no_tmdb"].(float64) != 1 || kept["seen_by_api"] != true || kept["summary"] == nil {
		t.Fatal(string(entries), kept)
	}

	// Refused: the run stops, the copy stays as it was.
	f.status.Store(http.StatusTooManyRequests)
	if n, err := s.refreshCopy(ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if top := copyOf(t, pool, "mdblist:copy:top")["lists"].([]any); len(top) != 2 {
		t.Fatal(top)
	}
}

func TestACopyThatFailsSaysSo(t *testing.T) {
	s, f, _ := setup(t)
	f.status.Store(http.StatusInternalServerError)
	if _, err := s.refreshCopy(context.Background()); err == nil {
		t.Fatal("a failing MDBList must be an error, not an empty copy")
	}
}
