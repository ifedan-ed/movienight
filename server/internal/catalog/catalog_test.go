package catalog_test

// The search catalog: the document per title, the sync sending only new
// and changed titles, adult titles and TMDB's 500-page limit, against
// stub TMDB and Meilisearch servers and the real Postgres.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/catalog"
	"movienight/server/internal/clients"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/testbase"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func obj(t *testing.T, s string) jsonx.Obj {
	t.Helper()
	var o jsonx.Obj
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestDoc(t *testing.T) {
	d := catalog.Doc(obj(t, `{"id": 9340, "title": "The Goonies", "original_title": "The Goonies", "release_date": "1985-06-07",
		"genre_ids": [12, 35], "overview": "Kids  find a\ntreasure map.", "vote_count": 6433, "poster_path": "/g.jpg"}`), "movie")
	if d["id"] != "m9340" || d["year"] != 1985 || d["votes"] != 6433 ||
		d["searchText"] != "The Goonies | (1985) | Adventure, Comedy | Kids find a treasure map." {
		t.Fatal(d)
	}
	tm := d["tmdb"].(map[string]any)
	if tm["poster_path"] != "/g.jpg" || tm["media_type"] != "movie" {
		t.Fatal(tm)
	}
	if catalog.Doc(obj(t, `{"id": 1, "title": " "}`), "movie") != nil {
		t.Fatal("a title without a name is no document")
	}
	show := catalog.Doc(obj(t, `{"id": 1399, "name": "Show", "original_name": "Show", "first_air_date": "2011-04-17"}`), "tv")
	if show["id"] != "t1399" || show["year"] != 2011 {
		t.Fatal(show)
	}
}

// The index already holds the Python's documents; a Go hash that differed
// would send (and re-embed) all of them. These hashes came from the Python's
// catalog_doc run on the same input.
func TestDocHashesAreThePythons(t *testing.T) {
	for _, c := range []struct{ raw, media, h string }{
		{`{"id": 9340, "title": "The Goonies", "original_title": "The Goonies", "release_date": "1985-06-07", "genre_ids": [12, 35], "overview": "Kids  find a\ntreasure map.", "vote_count": 6433, "vote_average": 7.0, "poster_path": "/g.jpg", "adult": false, "popularity": 12.5}`,
			"movie", "bdeae6359d3e8580"},
		{`{"id": 1399, "name": "Gâme \"of\" Thrones 🐉", "original_name": "왕좌의 게임", "first_air_date": "2011-04-17", "genre_ids": [10765, 18, 9999], "overview": "Seven noble families &amp; a\u0007 throne.", "vote_count": 25000, "vote_average": 8.456, "backdrop_path": null, "poster_path": ""}`,
			"tv", "d8a99f96d078c10c"},
	} {
		if got := catalog.Doc(obj(t, c.raw), c.media)["h"]; got != c.h {
			t.Errorf("%s: hash %s, the Python's %s", c.media, got, c.h)
		}
	}
}

// fakeTMDB serves discover: a few titles in 1985, or perDay titles a day of
// 2001 (a year past 500 pages), nothing in other years.
type fakeTMDB struct {
	perDay  func(day time.Time) int
	failAll bool
}

func movie(id int, adult bool) jsonx.Obj {
	return jsonx.Obj{"id": id, "title": fmt.Sprintf("Movie %d", id), "original_title": fmt.Sprintf("Movie %d", id),
		"release_date": "1985-06-07", "genre_ids": []int{12}, "overview": "A treasure map and a treasure hunt, treasure.",
		"vote_count": 100, "vote_average": 7.5, "adult": adult}
}

func (f *fakeTMDB) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.failAll {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		q := r.URL.Query()
		p, _ := strconv.Atoi(q.Get("page"))
		if p > 500 {
			t.Errorf("TMDB serves no page past 500")
		}
		var all []jsonx.Obj
		media := strings.TrimPrefix(r.URL.Path, "/discover/")
		switch {
		case f.perDay == nil && media == "movie" && q.Get("primary_release_year") == "1985":
			// Six films, Cheeky (hidden by TMDB's default filter, not marked
			// adult) and one TMDB marks adult: nothing is left out.
			for _, id := range []int{9010, 9011, 9012, 9013, 9014, 9015, 22705} {
				all = append(all, movie(id, false))
			}
			all = append(all, movie(613683, true))
		case f.perDay == nil && media == "tv" && q.Get("first_air_date_year") == "1985":
			for id := 9020; id < 9026; id++ {
				all = append(all, jsonx.Obj{"id": id, "name": fmt.Sprintf("Show %d", id), "first_air_date": "1985-01-01"})
			}
		case f.perDay != nil && media == "movie":
			var lo, hi time.Time
			if q.Get("primary_release_year") == "2001" {
				lo, hi = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2001, 12, 31, 0, 0, 0, 0, time.UTC)
			} else if g := q.Get("primary_release_date.gte"); g != "" {
				lo, _ = time.Parse(time.DateOnly, g)
				hi, _ = time.Parse(time.DateOnly, q.Get("primary_release_date.lte"))
			}
			for d := lo; !lo.IsZero() && !d.After(hi); d = d.AddDate(0, 0, 1) {
				for n := 0; n < f.perDay(d); n++ {
					all = append(all, jsonx.Obj{"id": d.YearDay()*100_000 + n, "title": "Film", "release_date": d.Format(time.DateOnly)})
				}
			}
		}
		pages := (len(all) + 19) / 20
		start, end := min((p-1)*20, len(all)), min(p*20, len(all))
		_ = json.NewEncoder(w).Encode(jsonx.Obj{"results": all[start:end], "total_pages": pages})
	}
}

// fakeMeili keeps the catalog index's documents in memory.
type fakeMeili struct {
	mu      sync.Mutex
	docs    map[string]map[string]any
	sent    []string
	deleted []string
}

func (m *fakeMeili) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/indexes":
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid": 1}`))
	case r.Method == http.MethodPatch:
		_, _ = w.Write([]byte(`{"taskUid": 2}`))
	case r.URL.Path == "/indexes/catalog/documents/fetch":
		var body struct{ Offset, Limit int }
		_ = json.NewDecoder(r.Body).Decode(&body)
		var res []map[string]any
		for id, d := range m.docs {
			res = append(res, map[string]any{"id": id, "h": d["h"]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": res, "total": len(res)})
	case r.Method == http.MethodPut:
		var docs []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&docs)
		for _, d := range docs {
			m.docs[d["id"].(string)] = d
			m.sent = append(m.sent, d["id"].(string))
		}
		_, _ = w.Write([]byte(`{"taskUid": 7}`))
	case strings.HasSuffix(r.URL.Path, "/delete-batch"):
		var ids []string
		_ = json.NewDecoder(r.Body).Decode(&ids)
		for _, id := range ids {
			delete(m.docs, id)
		}
		m.deleted = append(m.deleted, ids...)
		_, _ = w.Write([]byte(`{"taskUid": 8}`))
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T, env testbase.Env, tm *fakeTMDB, firstYear int) (*catalog.Service, *fakeMeili, *pgxpool.Pool) {
	pool, _ := testbase.Conns(t)
	ts := httptest.NewServer(tm.handler(t))
	t.Cleanup(ts.Close)
	fm := &fakeMeili{docs: map[string]map[string]any{}}
	ms := httptest.NewServer(http.HandlerFunc(fm.handler))
	t.Cleanup(ms.Close)
	cfg := env.Config(t)
	cl := &clients.Set{
		TMDB:  clients.NewTMDB(ts.Client(), ts.URL, "key", "en-US", "US", false),
		Meili: clients.NewMeili(ms.Client(), ms.URL, "", "model", 1000, quiet),
	}
	s := catalog.New(cfg, pool, cl, quiet)
	catalog.SetFirstYear(s, firstYear)
	return s, fm, pool
}

func env(t *testing.T) testbase.Env {
	e := testbase.BaseEnv(t)
	e["CATALOG_MOVIE_MIN_VOTES"], e["CATALOG_TV_MIN_VOTES"] = "25", "15"
	return e
}

func TestSyncSendsOnlyNewAndChangedTitles(t *testing.T) {
	s, fm, pool := setup(t, env(t), &fakeTMDB{}, 1985)
	ctx := context.Background()
	if built, _ := s.Built(ctx); built {
		t.Fatal("never built yet")
	}
	res, err := s.Sync(ctx)
	if err != nil || res.Titles != 14 || res.Changed != 14 || res.FailedPages != 0 || len(res.Tasks) != 1 {
		t.Fatal(res, err)
	}
	for _, id := range []string{"m9010", "t9022", "m22705", "m613683"} {
		if fm.docs[id] == nil {
			t.Fatal("not sent:", id)
		}
	}
	// An old title TMDB no longer lists, and the rest unchanged.
	fm.docs["m1"] = map[string]any{"id": "m1", "h": "old"}
	fm.sent = nil
	res, err = s.Sync(ctx)
	if err != nil || res.Changed != 0 || res.Removed != 1 || len(fm.sent) != 0 ||
		strings.Join(fm.deleted, ",") != "m1" {
		t.Fatal(res, err, fm.sent, fm.deleted)
	}
	if built, err := s.Built(ctx); !built || err != nil {
		t.Fatal(built, err)
	}
	var raw string
	if err := pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = 'catalog_status'").Scan(&raw); err != nil ||
		!strings.Contains(raw, `"titles":14`) {
		t.Fatal(raw, err)
	}
	// The spelling vocabulary: words seen at least three times.
	if err := pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = 'catalog_vocab'").Scan(&raw); err != nil ||
		!strings.Contains(raw, `"treasure":24`) || strings.Contains(raw, `"hunt"`) == false {
		t.Fatal(raw, err)
	}
}

func TestHideAdultTitlesLeavesOutOnlyWhatTMDBMarksAdult(t *testing.T) {
	e := env(t)
	e["HIDE_ADULT_TITLES"] = "true"
	s, fm, _ := setup(t, e, &fakeTMDB{}, 1985)
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fm.docs["m22705"] == nil || fm.docs["m613683"] != nil {
		t.Fatal(fm.sent)
	}
}

// 30 titles a day: 10,950 in the year, 548 pages. Read in halves of the
// year, every title comes in and nothing counts as failed.
func TestAYearPastTMDBs500PagesIsReadWhole(t *testing.T) {
	s, fm, _ := setup(t, env(t), &fakeTMDB{perDay: func(time.Time) int { return 30 }}, 2001)
	res, err := s.Sync(context.Background())
	if err != nil || res.FailedPages != 0 || res.Titles != 365*30 || len(fm.docs) != 365*30 {
		t.Fatal(res, err, len(fm.docs))
	}
}

// One day of more than 10,000 titles: what TMDB serves is read, the read
// counts as failed, and nothing is removed (nor the vocabulary replaced).
func TestADayPast500PagesCountsAsFailedSoNothingIsRemoved(t *testing.T) {
	june := time.Date(2001, 6, 1, 0, 0, 0, 0, time.UTC)
	s, fm, pool := setup(t, env(t), &fakeTMDB{perDay: func(d time.Time) int {
		if d.Equal(june) {
			return 10_020
		}
		return 0
	}}, 2001)
	ctx := context.Background()
	fm.docs["m1"] = map[string]any{"id": "m1", "h": "old"}
	if _, err := pool.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ('catalog_vocab', '{"kept":9}')`); err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(ctx)
	if err != nil || res.FailedPages != 1 || res.Titles != 10_000 || res.Removed != 0 || fm.docs["m1"] == nil {
		t.Fatal(res, err)
	}
	var raw string
	_ = pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = 'catalog_vocab'").Scan(&raw)
	if raw != `{"kept":9}` {
		t.Fatal("the vocabulary was replaced after an incomplete read:", raw)
	}
	_ = pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = 'catalog_status'").Scan(&raw)
	if !strings.Contains(raw, `"failed_pages":1`) {
		t.Fatal(raw)
	}
}

func TestTMDBDownRemovesNothing(t *testing.T) {
	s, fm, _ := setup(t, env(t), &fakeTMDB{failAll: true}, 2024)
	fm.docs["m1"] = map[string]any{"id": "m1", "h": "old"}
	res, err := s.Sync(context.Background())
	if err != nil || res.FailedPages == 0 || res.Removed != 0 || fm.docs["m1"] == nil {
		t.Fatal(res, err)
	}
}
