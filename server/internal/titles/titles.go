// Package titles is the title cache: TMDB's metadata for every film and show
// Movie Night has shown, kept in the titles table so a watchlist, a list or
// the history draws its cards without asking TMDB (app/services/titles.py).
//
// Shared by the API and the worker: the worker's digests, AI rows, syncs and
// nightly refresh read and write the same cache through this one package.
//
// What changed from the Python, on purpose:
//
//   - Titles passing through a page (search results, a TMDB row) are
//     written only when that changes something (Remember): a key never seen,
//     a list-sourced row more than a day old, or one still without a poster
//     that now has one. The Python upserted every card of every page on
//     every GET, so each Home view wrote ~300 rows.
//   - Writes go in one statement, sorted by key, so concurrent pages lock
//     rows in the same order and cannot deadlock.
//   - Ensure fetches missing titles from TMDB at most EnsureAtOnce at a
//     time and never more than the caller's bound.
//   - RefreshStale (the worker's nightly "titles" job) re-fetches every
//     stale detailed row, a page at a time with keyset paging, with no
//     nightly cap: the Python refreshed at most a few hundred a night, so a
//     large cache never caught up.
package titles

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/store"
)

// MovieGenres and TVGenres are TMDB's genre ids and names (they rarely
// change; the Python kept the same tables).
var MovieGenres = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime", 99: "Documentary",
	18: "Drama", 10751: "Family", 14: "Fantasy", 36: "History", 27: "Horror", 10402: "Music",
	9648: "Mystery", 10749: "Romance", 878: "Science Fiction", 10770: "TV Movie", 53: "Thriller",
	10752: "War", 37: "Western",
}

var TVGenres = map[int]string{
	10759: "Action & Adventure", 16: "Animation", 35: "Comedy", 80: "Crime", 99: "Documentary",
	18: "Drama", 10751: "Family", 10762: "Kids", 9648: "Mystery", 10763: "News", 10764: "Reality",
	10765: "Sci-Fi & Fantasy", 10766: "Soap", 10767: "Talk", 10768: "War & Politics", 37: "Western",
}

// Genres is the table for a media type.
func Genres(media string) map[int]string {
	if media == "tv" {
		return TVGenres
	}
	return MovieGenres
}

// ListStale is how old a list-sourced (not detailed) row may get before a
// page that passes the title by writes it again (TMDB's lists carry fresh
// posters and votes).
const ListStale = 24 * time.Hour

// EnsureAtOnce bounds parallel TMDB detail calls of one Ensure.
const EnsureAtOnce = 8

// Key is "movie:603" / "tv:1399".
func Key(media string, id int) string {
	if media != "tv" {
		media = "movie"
	}
	return media + ":" + strconv.Itoa(id)
}

// Split is Key's inverse. ok is false for anything that is not a key.
func Split(key string) (media string, id int, ok bool) {
	m, rest, found := strings.Cut(key, ":")
	if !found || (m != "movie" && m != "tv") {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return m, n, true
}

// Meta is one title's cached metadata.
type Meta struct {
	Key           string   `json:"key"`
	MediaType     string   `json:"media_type"`
	TMDBID        int      `json:"tmdb_id"`
	Title         string   `json:"title"`
	OriginalTitle *string  `json:"original_title,omitempty"`
	Year          *int     `json:"year,omitempty"`
	ReleaseDate   *string  `json:"release_date,omitempty"`
	PosterPath    *string  `json:"poster_path,omitempty"`
	BackdropPath  *string  `json:"backdrop_path,omitempty"`
	Overview      *string  `json:"overview,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Runtime       *int     `json:"runtime,omitempty"`
	VoteAverage   *float64 `json:"vote_average,omitempty"`
	VoteCount     *int     `json:"vote_count,omitempty"`
	IMDbID        *string  `json:"imdb_id,omitempty"`
	Detailed      bool     `json:"detailed,omitempty"`
}

// Vote is the card's rounded score (one decimal), nil for none or zero.
// Halves round up as written (TMDB's 7.55 is 7.6); the Python's round() saw
// the binary float 7.5499... and gave 7.5, so a few cards differ by 0.1.
func (m *Meta) Vote() *float64 {
	if m.VoteAverage == nil || *m.VoteAverage == 0 {
		return nil
	}
	v := math.Round(*m.VoteAverage*10) / 10
	return &v
}

func strp(m clients.JSON, k string) *string {
	if s, ok := m[k].(string); ok && s != "" {
		return &s
	}
	return nil
}

func intp(m clients.JSON, k string) *int {
	if f, ok := m[k].(float64); ok && f != 0 {
		n := int(f)
		return &n
	}
	return nil
}

func floatp(m clients.JSON, k string) *float64 {
	if f, ok := m[k].(float64); ok {
		return &f
	}
	return nil
}

func first(ps ...*string) *string {
	for _, p := range ps {
		if p != nil {
			return p
		}
	}
	return nil
}

// Normalize turns one TMDB result (discover, search, details, credits) into
// a Meta; nil for people and anything without an id. media "" reads it
// from the result (search results carry media_type; a bare result is a
// show when it has a first_air_date or only a name).
func Normalize(r clients.JSON, media string) *Meta {
	if media == "" {
		media, _ = r["media_type"].(string)
	}
	if media == "" {
		_, hasAir := r["first_air_date"]
		_, hasName := r["name"]
		_, hasTitle := r["title"]
		media = "movie"
		if hasAir || (hasName && !hasTitle) {
			media = "tv"
		}
	}
	idf, _ := r["id"].(float64)
	if (media != "movie" && media != "tv") || idf <= 0 {
		return nil
	}
	id := int(idf)
	var date *string
	if media == "movie" {
		date = strp(r, "release_date")
	} else {
		date = strp(r, "first_air_date")
	}
	m := &Meta{Key: Key(media, id), MediaType: media, TMDBID: id, ReleaseDate: date,
		OriginalTitle: first(strp(r, "original_title"), strp(r, "original_name")),
		PosterPath:    strp(r, "poster_path"), BackdropPath: strp(r, "backdrop_path"), Overview: strp(r, "overview"),
		VoteAverage: floatp(r, "vote_average"), VoteCount: intp(r, "vote_count"), Runtime: intp(r, "runtime")}
	name := first(strp(r, "title"), strp(r, "name"))
	if media == "tv" {
		name = first(strp(r, "name"), strp(r, "title"))
	}
	if name != nil {
		m.Title = *name
	}
	if date != nil && len(*date) >= 4 {
		if y, err := strconv.Atoi((*date)[:4]); err == nil {
			m.Year = &y
		}
	}
	if gs, ok := r["genres"].([]any); ok && len(gs) > 0 {
		for _, g := range gs {
			if gm, ok := g.(map[string]any); ok {
				if n, _ := gm["name"].(string); n != "" {
					m.Genres = append(m.Genres, n)
				}
			}
		}
	} else if ids, ok := r["genre_ids"].([]any); ok {
		table := Genres(media)
		for _, g := range ids {
			if f, ok := g.(float64); ok {
				if n, ok := table[int(f)]; ok {
					m.Genres = append(m.Genres, n)
				}
			}
		}
	}
	if media == "tv" && m.Runtime == nil {
		// Shows give a list of typical episode lengths.
		if rt, ok := r["episode_run_time"].([]any); ok && len(rt) > 0 {
			if f, ok := rt[0].(float64); ok && f > 0 {
				n := int(f)
				m.Runtime = &n
			}
		}
	}
	m.IMDbID = strp(r, "imdb_id")
	if m.IMDbID == nil {
		if ext, ok := r["external_ids"].(map[string]any); ok {
			m.IMDbID = strp(ext, "imdb_id")
		}
	}
	return m
}

// NormalizeAll normalizes a TMDB result list in order, dropping people and
// malformed entries and repeats.
func NormalizeAll(results []clients.JSON, media string) []*Meta {
	out := make([]*Meta, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		if m := Normalize(r, media); m != nil && !seen[m.Key] {
			seen[m.Key] = true
			out = append(out, m)
		}
	}
	return out
}

// FromRow reads a stored title.
func FromRow(t store.Title) *Meta {
	m := &Meta{Key: t.Key, MediaType: t.MediaType, TMDBID: int(t.TmdbID), Title: t.Title, OriginalTitle: t.OriginalTitle,
		ReleaseDate: t.ReleaseDate, PosterPath: t.PosterPath, BackdropPath: t.BackdropPath, Overview: t.Overview,
		VoteAverage: t.VoteAverage, IMDbID: t.ImdbID, Detailed: t.Detailed}
	if t.Year != nil {
		y := int(*t.Year)
		m.Year = &y
	}
	if t.Runtime != nil {
		n := int(*t.Runtime)
		m.Runtime = &n
	}
	if t.VoteCount != nil {
		n := int(*t.VoteCount)
		m.VoteCount = &n
	}
	if t.GenresJson != nil {
		_ = json.Unmarshal([]byte(*t.GenresJson), &m.Genres)
	}
	return m
}

// Fetcher is TMDB's small details call (clients.TMDB.Basic).
type Fetcher interface {
	Basic(ctx context.Context, media string, id int) (clients.JSON, error)
}

// Service reads and writes the cache.
type Service struct {
	q    *store.Queries
	tmdb func() Fetcher
	log  *slog.Logger
}

// New makes the service. tmdb is read on every call (tests swap the TMDB
// client after the app is built).
func New(q *store.Queries, tmdb func() Fetcher, log *slog.Logger) *Service {
	return &Service{q: q, tmdb: tmdb, log: log}
}

// GetMany reads the cached titles for keys (missing ones are absent).
func (s *Service) GetMany(ctx context.Context, keys []string) (map[string]*Meta, error) {
	out := make(map[string]*Meta, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.q.TitlesByKeys(ctx, dedupe(keys))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Key] = FromRow(r)
	}
	return out, nil
}

func dedupe(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// row is one title in UpsertTitles' JSON.
type row struct {
	Key           string   `json:"key"`
	MediaType     string   `json:"media_type"`
	TMDBID        int      `json:"tmdb_id"`
	Title         string   `json:"title"`
	OriginalTitle *string  `json:"original_title"`
	Year          *int     `json:"year"`
	ReleaseDate   *string  `json:"release_date"`
	PosterPath    *string  `json:"poster_path"`
	BackdropPath  *string  `json:"backdrop_path"`
	Overview      *string  `json:"overview"`
	GenresJSON    *string  `json:"genres_json"`
	Runtime       *int     `json:"runtime"`
	VoteAverage   *float64 `json:"vote_average"`
	VoteCount     *int     `json:"vote_count"`
	IMDbID        *string  `json:"imdb_id"`
	Detailed      bool     `json:"detailed"`
}

// Upsert writes metas (detailed: from TMDB's details call), sorted by key,
// in one statement.
func (s *Service) Upsert(ctx context.Context, metas []*Meta, detailed bool) error {
	byKey := make(map[string]*Meta, len(metas))
	for _, m := range metas {
		if m != nil {
			byKey[m.Key] = m
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	rows := make([]row, 0, len(byKey))
	for _, m := range byKey {
		r := row{Key: m.Key, MediaType: m.MediaType, TMDBID: m.TMDBID, Title: m.Title, OriginalTitle: m.OriginalTitle,
			Year: m.Year, ReleaseDate: m.ReleaseDate, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath,
			Overview: m.Overview, Runtime: m.Runtime, VoteAverage: m.VoteAverage, VoteCount: m.VoteCount,
			IMDbID: m.IMDbID, Detailed: detailed}
		if len(m.Genres) > 0 {
			b, _ := json.Marshal(m.Genres)
			g := string(b)
			r.GenresJSON = &g
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	body, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return s.q.UpsertTitles(ctx, body)
}

// Remember keeps titles that passed through a page (TMDB lists, search),
// writing only those that need it (see the package comment). A failure is
// logged and not returned: the page already has the metadata in hand, and
// warming the cache must never break it (the Python's rule too).
func (s *Service) Remember(ctx context.Context, metas []*Meta) {
	if len(metas) == 0 {
		return
	}
	keys := make([]string, 0, len(metas))
	for _, m := range metas {
		keys = append(keys, m.Key)
	}
	have, err := s.q.TitleFreshness(ctx, dedupe(keys))
	if err != nil {
		s.log.WarnContext(ctx, "title cache read failed", "err", err)
		return
	}
	known := make(map[string]store.TitleFreshnessRow, len(have))
	for _, h := range have {
		known[h.Key] = h
	}
	cutoff := time.Now().Add(-ListStale)
	var write []*Meta
	for _, m := range metas {
		h, ok := known[m.Key]
		switch {
		case !ok:
			write = append(write, m)
		case h.NoPoster && m.PosterPath != nil:
			write = append(write, m)
		case !h.Detailed && h.FetchedAt.Before(cutoff):
			write = append(write, m)
		}
	}
	if err := s.Upsert(ctx, write, false); err != nil {
		s.log.WarnContext(ctx, "title cache write failed", "err", err, "titles", len(write))
	}
}

// Ensure returns metadata for every key it can: cached ones, plus up to
// maxFetch fetched from TMDB now (EnsureAtOnce at a time) for keys never
// seen, or seeded from a list (name only) and never fetched in full. A title
// TMDB says does not exist is remembered as a bare row, so it is not asked
// for again on every view. TMDB trouble leaves that key out for now (the
// card is missing, the page still loads: the Python's deliberate degrade).
func (s *Service) Ensure(ctx context.Context, keys []string, maxFetch int) (map[string]*Meta, error) {
	keys = dedupe(keys)
	have, err := s.GetMany(ctx, keys)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, k := range keys {
		m, ok := have[k]
		if !ok || (m.PosterPath == nil && !m.Detailed) {
			if _, _, valid := Split(k); valid {
				missing = append(missing, k)
			}
		}
	}
	if len(missing) > maxFetch {
		missing = missing[:maxFetch]
	}
	if len(missing) == 0 {
		return have, nil
	}
	fetched := s.fetch(ctx, missing)
	var metas []*Meta
	for _, k := range missing {
		got, ok := fetched[k]
		if !ok {
			continue // TMDB trouble: try again on a later view
		}
		media, id, _ := Split(k)
		if got == nil {
			bare := &Meta{Key: k, MediaType: media, TMDBID: id}
			if old := have[k]; old != nil {
				bare.Title = old.Title
			}
			metas = append(metas, bare)
			continue
		}
		if m := Normalize(got, media); m != nil {
			metas = append(metas, m)
		}
	}
	if err := s.Upsert(ctx, metas, true); err != nil {
		return nil, fmt.Errorf("title cache write: %w", err)
	}
	fresh, err := s.GetMany(ctx, keysOf(metas))
	if err != nil {
		return nil, err
	}
	for k, m := range fresh {
		have[k] = m
	}
	return have, nil
}

func keysOf(metas []*Meta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.Key
	}
	return out
}

// fetch asks TMDB for each key: a payload, nil (TMDB has no such title), or
// absent (TMDB trouble, logged).
func (s *Service) fetch(ctx context.Context, keys []string) map[string]clients.JSON {
	tmdb := s.tmdb()
	out := make(map[string]clients.JSON, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, EnsureAtOnce)
	failed := 0
	for _, k := range keys {
		media, id, _ := Split(k)
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-gate }()
			got, err := tmdb.Basic(ctx, media, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			out[k] = got
		}()
	}
	wg.Wait()
	if failed > 0 {
		s.log.WarnContext(ctx, "TMDB details failed for some titles; they are tried again later", "failed", failed, "asked", len(keys))
	}
	return out
}

// One is one title's metadata through Ensure (fetched from TMDB when
// missing); nil when there is none (TMDB trouble or no such title).
func (s *Service) One(ctx context.Context, media string, id int) (*Meta, error) {
	k := Key(media, id)
	got, err := s.Ensure(ctx, []string{k}, 1)
	if err != nil {
		return nil, err
	}
	return got[k], nil
}

// How long a detailed row stays fresh: titles out lately change more
// (dates, posters, scores) than old ones. "Lately" is RecentDays.
const (
	FreshRecent = 3 * 24 * time.Hour
	FreshOld    = 30 * 24 * time.Hour
	RecentDays  = 120
)

// StalePage is how many stale titles RefreshStale reads and fetches at a
// time, which bounds the job's memory however large the cache grows.
const StalePage = 400

// RefreshStale re-fetches every cached title whose details went stale,
// oldest first, a page at a time, until none is left. Returns (refreshed,
// missed): a title TMDB couldn't send stays as it was and is tried at the
// next run (keyset paging passes it by, so one bad title cannot stall the
// run). A database error stops the run.
func (s *Service) RefreshStale(ctx context.Context, now time.Time) (refreshed, missed int, err error) {
	p := store.StaleTitlesParams{
		OldBefore: now.Add(-FreshOld), RecentBefore: now.Add(-FreshRecent),
		RecentSince: now.AddDate(0, 0, -RecentDays).UTC().Format("2006-01-02"),
		// Before every real row: the epoch and "" sort first.
		AfterFetched: time.Unix(0, 0), AfterKey: "", PageSize: StalePage,
	}
	for {
		if err := ctx.Err(); err != nil {
			return refreshed, missed, err
		}
		rows, err := s.q.StaleTitles(ctx, p)
		if err != nil {
			return refreshed, missed, fmt.Errorf("reading stale titles: %w", err)
		}
		if len(rows) == 0 {
			return refreshed, missed, nil
		}
		keys := make([]string, len(rows))
		for i, r := range rows {
			keys[i] = r.Key
		}
		var metas []*Meta
		for k, got := range s.fetch(ctx, keys) {
			// nil: TMDB no longer has it; the row stays as it was.
			if got == nil {
				continue
			}
			media, _, _ := Split(k)
			if m := Normalize(got, media); m != nil {
				metas = append(metas, m)
			}
		}
		if err := s.Upsert(ctx, metas, true); err != nil {
			return refreshed, missed, fmt.Errorf("writing refreshed titles: %w", err)
		}
		refreshed += len(metas)
		missed += len(rows) - len(metas)
		last := rows[len(rows)-1]
		p.AfterFetched, p.AfterKey = last.FetchedAt, last.Key
	}
}
