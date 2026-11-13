// Package rows loads the titles behind every single Home row key and the
// "See all" grids, list pages and collections behind them
// (app/services/rows.py load_items and its TMDB lists): the person's own
// collections and the library from the database, TMDB's lists through the
// shared cache.
//
// It returns title metadata, not cards: the caller collects every title on
// its page and loads the person's state for them once (userstate.Load), so
// a page costs one state query however many rows it holds.
//
// What changed from the Python, on purpose:
//   - Personal rows, the library and a list's items are paged in SQL, not
//     read whole and sliced.
//   - "In your library, not watched yet" is worked out in SQL and shuffled
//     per day of the person's zone (the Python used the server's date).
//   - TMDB pages passing through are remembered only when that changes
//     something (titles.Remember), not upserted card by card on every GET.
//   - TMDB's lists are cached in Valkey for every replica, with a kept copy
//     served when TMDB fails (the Python's deliberate degrade); with no copy
//     the grid answers 502 in plain words (ErrTMDBDown), never an empty page.
package rows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/homecfg"
	"movienight/server/internal/lists"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/users"
	"movienight/server/internal/userstate"
)

// Sizes and cache times (docs/limits.md).
const (
	RowSize       = 24 // titles in one Home row
	PageSize      = 40 // titles per page of a "See all" grid, a list's page, a collection
	TMDBLastPage  = 500
	discoverTTL   = 30 * time.Minute
	recsTTL       = 12 * time.Hour
	keptCopy      = 24 * time.Hour // how long a TMDB page is kept past its freshness, for when TMDB is down
	TMDBDownText  = "TMDB didn't answer just now, so this couldn't be loaded. Try again in a moment."
	TMDBDownRow   = "TMDB didn't answer just now; this row comes back when it does."
	TMDBEnd       = "TMDB lists only the first 10,000 titles here (its 500 pages). Narrow it down (a genre, a year, a streaming service) to see others."
	topRatedFilms = "2000"
	topRatedShows = "500"
)

// ErrTMDBDown is a TMDB list page TMDB couldn't send and no copy is kept
// of: a grid answers it as is (502 with TMDBDownText), a Home row says
// TMDBDownRow under its title instead.
var ErrTMDBDown = apierr.New(http.StatusBadGateway, TMDBDownText)

// TMDBKinds are the row kinds that are TMDB lists (they end at page 500).
var TMDBKinds = map[string]bool{"trending": true, "popular": true, "upcoming": true, "top_rated": true,
	"provider": true, "genre": true, "recs": true}

// TMDBMore: more pages of a TMDB list when it sent some and this isn't its last.
func TMDBMore(results int, page int) bool { return results > 0 && page < TMDBLastPage }

// EndNote is TMDBEnd on the last page TMDB serves, when a TMDB grid stops there.
func EndNote(page, items int, more bool) *string {
	if page >= TMDBLastPage && items > 0 && !more {
		s := TMDBEnd
		return &s
	}
	return nil
}

// Loader loads rows.
type Loader struct {
	cfg    *config.Config
	q      *store.Queries
	cache  *cache.Cache
	cl     *clients.Set
	titles *titles.Service
	lists  *lists.Service
	state  *userstate.Service
	log    *slog.Logger
}

// New makes the loader.
func New(cfg *config.Config, q *store.Queries, c *cache.Cache, cl *clients.Set, t *titles.Service, l *lists.Service,
	st *userstate.Service, log *slog.Logger) *Loader {
	return &Loader{cfg: cfg, q: q, cache: c, cl: cl, titles: t, lists: l, state: st, log: log}
}

// ForDeps builds the loader (and the lists service) from the routes' deps.
func ForDeps(d *rw.Deps) (*Loader, *lists.Service, *homecfg.Service) {
	h := homecfg.New(d.Q, d.Settings)
	ls := lists.New(d.Pool, d.Q, d.Titles, d.Settings, h, d.Log)
	return New(d.Cfg, d.Q, d.Cache, d.Clients, d.Titles, ls, d.State, d.Log), ls, h
}

// Cards loads the person's state for metas (one query) and makes their cards.
func (l *Loader) Cards(ctx context.Context, u store.User, metas []*titles.Meta) ([]schema.Card, error) {
	st, err := l.state.Load(ctx, u, KeysOf(metas))
	if err != nil {
		return nil, err
	}
	return st.Cards(metas), nil
}

// KeysOf is the metas' keys.
func KeysOf(metas []*titles.Meta) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		if m != nil {
			out = append(out, m.Key)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// TMDB lists, cached
// ---------------------------------------------------------------------------

// tmdbList is a cached TMDB result page; a kept copy when TMDB fails,
// ErrTMDBDown when there is none.
func (l *Loader) tmdbList(ctx context.Context, key string, fn func(context.Context) ([]clients.JSON, error)) ([]clients.JSON, error) {
	got, _, err := cache.CachedOrCopy(ctx, l.cache, key, cache.Options{TTL: discoverTTL, Keep: keptCopy}, fn)
	if err != nil {
		l.log.WarnContext(ctx, "TMDB list failed and no copy is kept", "err", err)
		return nil, ErrTMDBDown
	}
	return got, nil
}

func filtersKey(f map[string]string) string {
	b, _ := json.Marshal(f) // map keys are sorted: one key per set of filters
	return string(b)
}

// Discover is a cached TMDB discover page.
func (l *Loader) Discover(ctx context.Context, media string, page int, filters map[string]string) ([]clients.JSON, error) {
	key := cache.Key(true, "tmdb", "discover", media, strconv.Itoa(page), filtersKey(filters))
	return l.tmdbList(ctx, key, func(ctx context.Context) ([]clients.JSON, error) {
		return l.cl.TMDB.Discover(ctx, media, page, filters)
	})
}

// Trending is TMDB's trending of the week (media "" for both).
func (l *Loader) Trending(ctx context.Context, media string, page int) ([]clients.JSON, error) {
	m := media
	if m == "" {
		m = "all"
	}
	return l.tmdbList(ctx, cache.Key(false, "tmdb", "trending", m, strconv.Itoa(page)), func(ctx context.Context) ([]clients.JSON, error) {
		return l.cl.TMDB.Trending(ctx, m, "week", page)
	})
}

func (l *Loader) upcoming(ctx context.Context, media string, page int) ([]clients.JSON, error) {
	return l.tmdbList(ctx, cache.Key(false, "tmdb", "upcoming", media, strconv.Itoa(page)), func(ctx context.Context) ([]clients.JSON, error) {
		return l.cl.TMDB.Upcoming(ctx, media, page)
	})
}

// Recommendations is TMDB's "more like this" for a title (its similar
// titles when it has none), kept 12 hours. strict: a failure is
// ErrTMDBDown (a grid); otherwise it is logged and nothing is returned (a
// title page or "Because you watched" leaves the section out, as before).
func (l *Loader) Recommendations(ctx context.Context, media string, id, page int, strict bool) ([]clients.JSON, error) {
	key := cache.Key(false, "tmdb", "recs", media, strconv.Itoa(id), strconv.Itoa(page))
	got, err := cache.Cached(ctx, l.cache, key, cache.Options{TTL: recsTTL}, func(ctx context.Context) ([]clients.JSON, error) {
		recs, err := l.cl.TMDB.Recommendations(ctx, media, id, page)
		if err == nil && len(recs) == 0 && page == 1 {
			recs, err = l.cl.TMDB.Similar(ctx, media, id, 1)
		}
		if recs == nil {
			recs = []clients.JSON{}
		}
		return recs, err
	})
	if err != nil {
		l.log.WarnContext(ctx, "TMDB recommendations failed", "err", err)
		if strict {
			return nil, ErrTMDBDown
		}
		return nil, nil
	}
	return got, nil
}

// SmartResults is one page of a smart list: TMDB's discover with its saved
// filters (30 minutes, a kept copy when TMDB fails, else ErrTMDBDown).
func (l *Loader) SmartResults(ctx context.Context, lst *store.List, page int) ([]clients.JSON, string, error) {
	f := map[string]any{}
	if lst.FiltersJson != nil {
		if err := json.Unmarshal([]byte(*lst.FiltersJson), &f); err != nil {
			return nil, "", fmt.Errorf("smart list %d's filters are not JSON: %w", lst.ID, err)
		}
	}
	f = lists.CleanFilters(f)
	media := f["media"].(string)
	got, err := l.Discover(ctx, media, page, lists.TMDBFilters(f, l.cfg.WatchRegion))
	return got, media, err
}

// ---------------------------------------------------------------------------
// The person's own rows and the library, paged in SQL
// ---------------------------------------------------------------------------

// PersonalKinds are the row kinds read from the database.
var PersonalKinds = map[string]bool{"watchlist": true, "favorites": true, "rated": true, "ratings": true, "recent": true, "unwatched": true}

// PersonalKeys is one page of a personal row's title keys (watchlist,
// favorites, rated: 7 and up best first, ratings: newest first, recent:
// the library's newest, unwatched: the library not yet watched, shuffled
// per day) and how many there are in all. media "" is both.
func (l *Loader) PersonalKeys(ctx context.Context, u store.User, kind, media string, limit, offset int) ([]string, int, error) {
	userID := u.ID
	type row struct {
		media string
		id    int32
		total int32
	}
	var rows []row
	lim, off := int32(limit), int32(offset)
	switch kind {
	case "watchlist":
		got, err := l.q.WatchlistPage(ctx, store.WatchlistPageParams{UserID: userID, Media: media, Lim: lim, Off: off})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range got {
			rows = append(rows, row{r.MediaType, r.TmdbID, r.Total})
		}
	case "favorites":
		got, err := l.q.FavoritesPage(ctx, store.FavoritesPageParams{UserID: userID, Media: media, Lim: lim, Off: off})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range got {
			rows = append(rows, row{r.MediaType, r.TmdbID, r.Total})
		}
	case "rated", "ratings":
		got, err := l.q.RatingsPage(ctx, store.RatingsPageParams{UserID: userID, Media: media, Top: kind == "rated", Lim: lim, Off: off})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range got {
			rows = append(rows, row{r.MediaType, r.TmdbID, r.Total})
		}
	case "recent":
		got, err := l.q.RecentLibraryPage(ctx, store.RecentLibraryPageParams{Media: media, Lim: lim, Off: off})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range got {
			rows = append(rows, row{r.MediaType, r.TmdbID, r.Total})
		}
	case "unwatched":
		// The person's own date: their shuffle moves on at their midnight.
		day := time.Now().In(users.Location(u, l.cfg.Location)).Format("2006-01-02")
		got, err := l.q.UnwatchedLibraryPage(ctx, store.UnwatchedLibraryPageParams{UserID: userID, Media: media, Day: day, Lim: lim, Off: off})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range got {
			rows = append(rows, row{r.MediaType, r.TmdbID, r.Total})
		}
	default:
		return nil, 0, fmt.Errorf("not a personal row: %q", kind)
	}
	keys := make([]string, len(rows))
	total := 0
	for i, r := range rows {
		keys[i] = titles.Key(r.media, int(r.id))
		total = int(r.total)
	}
	return keys, total, nil
}

// PersonalPage is one page of a personal row's titles, and how many the
// row holds in all.
func (l *Loader) PersonalPage(ctx context.Context, u store.User, kind, media string, page, size int) (Page, int, error) {
	offset := (page - 1) * size
	keys, total, err := l.PersonalKeys(ctx, u, kind, media, size, offset)
	if err != nil {
		return Page{}, 0, err
	}
	if len(keys) == 0 && offset > 0 {
		// Past the end there is no row to carry the window count: read it
		// from the first row (a collection's page still says its size).
		if _, total, err = l.PersonalKeys(ctx, u, kind, media, 1, 0); err != nil {
			return Page{}, 0, err
		}
	}
	metas, err := l.metasFor(ctx, keys, size)
	return Page{Metas: metas, More: offset+size < total}, total, err
}

// ---------------------------------------------------------------------------
// One row key at one page
// ---------------------------------------------------------------------------

// Page is one page of a row key's titles.
type Page struct {
	Metas []*titles.Meta
	More  bool
}

// metasFor reads (and fetches what is missing of, up to the page's size)
// the metadata of keys, in their order; titles TMDB doesn't know are left out.
func (l *Loader) metasFor(ctx context.Context, keys []string, size int) ([]*titles.Meta, error) {
	got, err := l.titles.Ensure(ctx, keys, size)
	if err != nil {
		return nil, err
	}
	out := make([]*titles.Meta, 0, len(keys))
	for _, k := range keys {
		if m := got[k]; m != nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// Remember keeps TMDB results passing through and returns them as metas.
func (l *Loader) Remember(ctx context.Context, results []clients.JSON, media string) []*titles.Meta {
	metas := titles.NormalizeAll(results, media)
	l.titles.Remember(ctx, metas)
	return metas
}

// Load is one single row key's titles at a page (not the multi rows
// "because" and "ai", nor "continue"). media "" takes the tab's; sort is
// for list rows only. An unknown key is an empty page.
func (l *Loader) Load(ctx context.Context, u store.User, tab, key string, page, size int, media, sort string) (Page, error) {
	userID := u.ID
	if media == "" {
		media = homecfg.TabMedia(tab)
	}
	kind, arg, _ := strings.Cut(key, ":")
	switch {
	case PersonalKinds[kind]:
		p, _, err := l.PersonalPage(ctx, u, kind, media, page, size)
		return p, err
	case kind == "list":
		id, err := strconv.Atoi(arg)
		if err != nil {
			return Page{}, nil
		}
		lst, err := l.lists.GetVisible(ctx, userID, int32(id))
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return Page{}, nil // not theirs or gone: no row
		}
		if err != nil {
			return Page{}, err
		}
		return l.ListPage(ctx, lst, page, size, media, sort)
	case kind == "recs":
		rmedia, rid, _ := strings.Cut(arg, ":")
		id, err := strconv.Atoi(rid)
		if (rmedia != "movie" && rmedia != "tv") || err != nil {
			return Page{}, nil
		}
		results, err := l.Recommendations(ctx, rmedia, id, page, true)
		if err != nil {
			return Page{}, err
		}
		return Page{Metas: l.Remember(ctx, results, rmedia), More: TMDBMore(len(results), page)}, nil
	}

	// TMDB rows; For You takes films where one media is needed.
	m := media
	if m == "" {
		m = "movie"
	}
	var results []clients.JSON
	var err error
	switch {
	case kind == "trending":
		results, err = l.Trending(ctx, media, page)
	case kind == "popular":
		results, err = l.Discover(ctx, m, page, map[string]string{"sort_by": "popularity.desc"})
	case kind == "upcoming":
		results, err = l.upcoming(ctx, m, page)
	case kind == "top_rated":
		// Only titles with enough votes (2,000 for films, 500 for shows),
		// so a 10/10 from a handful of people doesn't top the row.
		votes := topRatedFilms
		if m == "tv" {
			votes = topRatedShows
		}
		results, err = l.Discover(ctx, m, page, map[string]string{"sort_by": "vote_average.desc", "vote_count.gte": votes})
	case kind == "provider" && digits(arg):
		results, err = l.Discover(ctx, m, page, map[string]string{"sort_by": "popularity.desc", "with_watch_providers": arg, "watch_region": l.cfg.WatchRegion})
	case kind == "genre" && digits(arg):
		results, err = l.Discover(ctx, m, page, map[string]string{"sort_by": "popularity.desc", "with_genres": arg})
	default:
		return Page{}, nil
	}
	if err != nil {
		return Page{}, err
	}
	norm := m
	if kind == "trending" && media == "" {
		norm = "" // trending of both carries each result's media_type
	}
	return Page{Metas: l.Remember(ctx, results, norm), More: TMDBMore(len(results), page)}, nil
}

// ListPage is one page of a list: a smart list runs its filters on TMDB,
// any other list reads its items (filtered, sorted, paged in SQL).
func (l *Loader) ListPage(ctx context.Context, lst *store.List, page, size int, media, sort string) (Page, error) {
	if lst.Kind == "smart" {
		results, m, err := l.SmartResults(ctx, lst, page)
		if err != nil {
			return Page{}, err
		}
		return Page{Metas: l.Remember(ctx, results, m), More: TMDBMore(len(results), page)}, nil
	}
	offset := (page - 1) * size
	keys, total, err := l.lists.ItemsPage(ctx, lst.ID, media, sort, size, offset)
	if err != nil {
		return Page{}, err
	}
	metas, err := l.metasFor(ctx, keys, size)
	return Page{Metas: metas, More: offset+size < total}, err
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
