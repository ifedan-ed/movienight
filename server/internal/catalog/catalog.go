// Package catalog keeps the search catalog: a Meilisearch index ("catalog")
// of every movie and show on TMDB with enough ratings (5 by default: about
// 200,000 titles, films in every language), each as title | original title |
// (year) | genres | plot, so a described plot ("kids with a treasure map
// trying to save their homes") finds the film, which TMDB's own title search
// cannot (app/services/catalog.py).
//
// The worker builds it weekly (never the API: a build is about 4,000 TMDB
// calls). TMDB's discover is read a year at a time; Meilisearch only embeds
// a title again when its text changes, so each document carries a hash "h"
// and only new and changed ones are sent. Titles that left TMDB's lists are
// removed only after a read with no failed page: an incomplete read never
// takes a title for gone.
//
// Memory stays bounded: documents are built, compared and sent a page at a
// time (BATCH at most held), never all 200,000 at once as the Python's dict
// held them. What is kept for the whole run is only what must be: the
// index's current id -> hash map, the ids seen, and the word counts for the
// spelling vocabulary.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/spelling"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
)

const (
	// FirstYear is the earliest year read.
	FirstYear = 1900
	// MaxPages: TMDB's discover serves at most 500 pages (10,000 titles) of
	// one query, an outside limit Movie Night can't raise. A year with more
	// is read in date ranges, halved until each fits; only a single day
	// with more than 10,000 counts as a failed read (docs/limits.md).
	MaxPages = 500
	// Batch is how many documents go to Meilisearch in one request.
	Batch = 2000
	// pageConcurrency bounds parallel TMDB calls (TMDB allows about 50 a
	// second per key; the API shares the key) and yearsAtOnce the years
	// walked together, as in the Python.
	pageConcurrency = 6
	yearsAtOnce     = 8
)

// Summary is one run's outcome, kept under status.CatalogKey (the
// Python's key and fields: the admins' notices in package status read it).
type Summary struct {
	At          string `json:"at"`
	Titles      int    `json:"titles"`
	Changed     int    `json:"changed"`
	Removed     int    `json:"removed"`
	FailedPages int    `json:"failed_pages"`
	// Meilisearch's task ids, to follow the embedding.
	Tasks []int `json:"tasks"`
}

// Service builds the catalog.
type Service struct {
	cfg       *config.Config
	pool      *pgxpool.Pool
	cl        *clients.Set
	log       *slog.Logger
	firstYear int
}

// New makes the service.
func New(cfg *config.Config, pool *pgxpool.Pool, cl *clients.Set, log *slog.Logger) *Service {
	return &Service{cfg: cfg, pool: pool, cl: cl, log: log, firstYear: FirstYear}
}

// Built reports whether the catalog was ever built (a run's status is kept;
// the worker builds it soon after its first start when not).
func (s *Service) Built(ctx context.Context) (bool, error) {
	raw, err := store.New(s.pool).GetAppSetting(ctx, status.CatalogKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var v map[string]any
	return json.Unmarshal([]byte(raw), &v) == nil, nil
}

// Sync brings the catalog index in step with TMDB: new and changed titles
// are sent (Meilisearch embeds them in the background), titles that left
// are removed, but only after a read with no failed page.
func (s *Service) Sync(ctx context.Context) (Summary, error) {
	started := time.Now()
	meili := s.cl.Meili
	if err := meili.EnsureIndex(ctx, clients.CatalogIndex); err != nil {
		return Summary{}, err
	}
	current, err := meili.FieldValues(ctx, clients.CatalogIndex, "h")
	if err != nil {
		return Summary{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sk := &sink{s: s, current: current, seen: make(map[string]struct{}, len(current)), counts: map[string]int{}, cancel: cancel}

	failed := 0
	for _, kind := range []struct {
		media string
		votes int
	}{{"movie", s.cfg.CatalogMovieMinVotes}, {"tv", s.cfg.CatalogTVMinVotes}} {
		failed += s.fetch(ctx, kind.media, kind.votes, sk.add)
	}
	sk.mu.Lock()
	defer sk.mu.Unlock()
	if sk.err == nil {
		sk.flush(ctx)
	}
	if sk.err != nil {
		return Summary{}, sk.err
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	var gone []string
	if failed == 0 {
		for id := range current {
			if _, ok := sk.seen[id]; !ok {
				gone = append(gone, id)
			}
		}
	}
	for i := 0; i < len(gone); i += Batch {
		if err := meili.DeleteDocuments(ctx, clients.CatalogIndex, gone[i:min(i+Batch, len(gone))]); err != nil {
			return Summary{}, err
		}
	}

	q := store.New(s.pool)
	if failed == 0 {
		// The Python saved the vocabulary after every run, so a run that
		// could read little of TMDB replaced a good vocabulary with a thin
		// one; only a whole read replaces it here.
		raw, err := json.Marshal(spelling.Keep(sk.counts))
		if err != nil {
			return Summary{}, err
		}
		if err := q.SetAppSetting(ctx, store.SetAppSettingParams{Key: spelling.VocabKey, Value: string(raw)}); err != nil {
			return Summary{}, err
		}
	} else {
		s.log.WarnContext(ctx, "catalog: some of TMDB couldn't be read; no title removed, the spelling vocabulary kept as it was",
			"failed_pages", failed)
	}
	sum := Summary{At: status.ISO(started), Titles: len(sk.seen), Changed: sk.changed, Removed: len(gone),
		FailedPages: failed, Tasks: sk.tasks}
	if sum.Tasks == nil {
		sum.Tasks = []int{}
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		return Summary{}, err
	}
	if err := q.SetAppSetting(ctx, store.SetAppSettingParams{Key: status.CatalogKey, Value: string(raw)}); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

// sink takes each page of results as it is read: builds the documents,
// counts their words, and sends the new and changed ones in batches. Pages
// arrive from many goroutines; the mutex makes them take turns, and a send
// under it makes the readers wait, so documents never pile up faster than
// Meilisearch takes them.
type sink struct {
	s       *Service
	current map[string]any
	mu      sync.Mutex
	seen    map[string]struct{}
	counts  map[string]int
	buffer  []clients.JSON
	changed int
	tasks   []int
	err     error
	cancel  context.CancelFunc
}

func (k *sink) add(ctx context.Context, media string, results []jsonx.Obj) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return
	}
	for _, r := range results {
		if k.s.cfg.HideAdultTitles && jsonx.Bool(r, "adult") {
			continue // HIDE_ADULT_TITLES leaves out what TMDB marks adult, nothing else
		}
		d := Doc(r, media)
		if d == nil {
			continue
		}
		id := d["id"].(string)
		if _, dup := k.seen[id]; dup {
			continue // a title TMDB lists twice in one read is sent once
		}
		k.seen[id] = struct{}{}
		spelling.AddWords(k.counts, d["searchText"].(string))
		if h, _ := k.current[id].(string); h == d["h"] {
			continue
		}
		k.changed++
		k.buffer = append(k.buffer, d)
		if len(k.buffer) >= Batch {
			k.flush(ctx)
		}
	}
}

// flush sends the buffered documents (k.mu held). A failure stops the run:
// cancel ends every read still going.
func (k *sink) flush(ctx context.Context) {
	if len(k.buffer) == 0 {
		return
	}
	task, err := k.s.cl.Meili.ReplaceDocuments(ctx, clients.CatalogIndex, k.buffer)
	k.buffer = k.buffer[:0]
	if err != nil {
		k.err = fmt.Errorf("sending catalog documents: %w", err)
		k.cancel()
		return
	}
	k.tasks = append(k.tasks, task)
}

// fetch reads every TMDB title of one kind with at least minVotes ratings,
// a year at a time, handing each page's results to emit. Returns how many
// reads failed (a page that couldn't be read, or a day too big to read
// whole); with any, nothing is removed afterwards.
func (s *Service) fetch(ctx context.Context, media string, minVotes int,
	emit func(context.Context, string, []jsonx.Obj)) int {
	yearParam, dateParam, order := "primary_release_year", "primary_release_date", "original_title.asc"
	if media == "tv" {
		yearParam, dateParam, order = "first_air_date_year", "first_air_date", "original_name.asc"
	}
	// A stable order (by original title): popularity changes while the
	// pages are walked, and a title moving between pages would be skipped,
	// then taken for gone.
	sem := make(chan struct{}, pageConcurrency)
	var failed atomic.Int64

	page := func(where map[string]string, n int) jsonx.Obj {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			failed.Add(1)
			return nil
		}
		defer func() { <-sem }()
		params := map[string]string{"sort_by": order, "vote_count.gte": strconv.Itoa(minVotes)}
		for k, v := range where {
			params[k] = v
		}
		data, err := s.cl.TMDB.DiscoverPage(ctx, media, n, true, params)
		if err == nil && data == nil {
			err = errors.New("TMDB answered 404")
		}
		if err != nil {
			failed.Add(1)
			if ctx.Err() == nil {
				s.log.WarnContext(ctx, "catalog: a TMDB page couldn't be read", "media", media, "where", where, "page", n, "err", err)
			}
			return nil
		}
		return data
	}
	pagesOf := func(d jsonx.Obj) int {
		n, _ := jsonx.Int(d, "total_pages")
		return max(n, 1)
	}
	// walk hands over the first page, then reads the rest in parallel.
	walk := func(where map[string]string, first jsonx.Obj, pages int) {
		emit(ctx, media, jsonx.List(first, "results"))
		var wg sync.WaitGroup
		for n := 2; n <= pages; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				if d := page(where, n); d != nil {
					emit(ctx, media, jsonx.List(d, "results"))
				}
			}(n)
		}
		wg.Wait()
	}
	// span reads the titles first released (or aired) from lo to hi,
	// halving the range until it fits in MaxPages.
	var span func(lo, hi time.Time)
	span = func(lo, hi time.Time) {
		where := map[string]string{dateParam + ".gte": lo.Format(time.DateOnly), dateParam + ".lte": hi.Format(time.DateOnly)}
		first := page(where, 1)
		if first == nil {
			return
		}
		pages := pagesOf(first)
		if pages > MaxPages {
			if lo.Before(hi) {
				days := int(hi.Sub(lo).Hours() / 24)
				mid := lo.AddDate(0, 0, days/2)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); span(lo, mid) }()
				go func() { defer wg.Done(); span(mid.AddDate(0, 0, 1), hi) }()
				wg.Wait()
				return
			}
			// One day with more than 10,000 titles can't be split further:
			// read what TMDB serves, and count it as a failed read so no
			// title is taken for gone.
			failed.Add(1)
			s.log.WarnContext(ctx, "catalog: one day has more titles than TMDB serves; nothing will be removed",
				"media", media, "day", lo.Format(time.DateOnly), "pages", pages, "max", MaxPages)
			pages = MaxPages
		}
		walk(where, first, pages)
	}
	oneYear := func(year int) {
		where := map[string]string{yearParam: strconv.Itoa(year)}
		first := page(where, 1)
		if first == nil {
			return
		}
		if pages := pagesOf(first); pages > MaxPages {
			span(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC))
		} else {
			walk(where, first, pages)
		}
	}

	// Through next year (TMDB lists films dated ahead), in the install's
	// time zone: the catalog is the install's, built by its schedule.
	last := time.Now().In(s.cfg.Location).Year() + 1
	for y := s.firstYear; y <= last; y += yearsAtOnce {
		if ctx.Err() != nil {
			break
		}
		var wg sync.WaitGroup
		for year := y; year < y+yearsAtOnce && year <= last; year++ {
			wg.Add(1)
			go func(year int) { defer wg.Done(); oneYear(year) }(year)
		}
		wg.Wait()
	}
	return int(failed.Load())
}
