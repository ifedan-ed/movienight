// Package history is a person's watch history: their merged watches (one
// per title or episode and day), a title's watches, marking and removing
// watches, Profile's numbers and the "Rate what you just watched" prompt.
//
// Shared: the title routes, Profile (GET /history, /me stats, the rate
// prompt) and, for MarkWatched, anything that records a watch.
//
// What changed from the Python, on purpose:
//
//   - The day a watch is merged by is the person's own (their reported
//     zone, else the install's TIMEZONE: users.Location), not UTC. Every
//     reader takes that zone as an argument, so the caller decides it once
//     per request.
//   - The History page is grouped and paged in SQL (HistoryPage): the
//     Python read every watch the person ever made to show 40.
//   - Stats are counted in SQL, "this year" in the person's zone.
//   - "Not now" on the rate prompt is one atomic statement.
package history

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/rw/schema"
	"movienight/server/internal/settings"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// RateDismissedKey is the user setting holding every dismissed rate prompt.
const RateDismissedKey = "rate_prompt_dismissed"

// RatePromptDays is how far back the rate prompt looks.
const RatePromptDays = 21

// Service reads and writes history.
type Service struct {
	q        *store.Queries
	settings *settings.Service
}

// New makes the service.
func New(q *store.Queries, s *settings.Service) *Service {
	return &Service{q: q, settings: s}
}

// WithTx is the service on a transaction (episodes ticks a season in one).
func (s *Service) WithTx(tx pgx.Tx) *Service {
	return &Service{q: s.q.WithTx(tx), settings: s.settings}
}

// historyMedia is the history table's media_type for a title's media.
func historyMedia(media string) string {
	if media == "movie" {
		return "movie"
	}
	return "episode"
}

// ISO writes a time as Python's isoformat() did (microseconds only when
// there are any), in the person's zone: a watch at "8 pm" reads as 20:00
// with its offset, which every client parses.
func ISO(t time.Time, loc *time.Location) string {
	t = t.In(loc)
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05-07:00")
	}
	return t.Format("2006-01-02T15:04:05.000000-07:00")
}

func intp(p *int32) *int {
	if p == nil {
		return nil
	}
	n := int(*p)
	return &n
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// watch builds one merged watch, its time written in loc.
func watch(loc *time.Location, ids []int32, media string, tmdbID *int32, title string, season, episode *int32,
	episodeTitle string, at time.Time, sources []string) schema.Watch {
	w := schema.Watch{IDs: make([]int, len(ids)), MediaType: media, Title: title, Season: intp(season),
		Episode: intp(episode), EpisodeTitle: strp(episodeTitle), WatchedAt: ISO(at, loc),
		Sources: []string{}}
	for i, id := range ids {
		w.IDs[i] = int(id)
	}
	if tmdbID != nil {
		k := titles.Key(map[bool]string{true: "tv", false: "movie"}[media == "episode"], int(*tmdbID))
		w.Key = &k
	}
	// Each source once, in the order of the rows (newest first).
	seen := map[string]bool{}
	for _, src := range sources {
		if !seen[src] {
			seen[src] = true
			w.Sources = append(w.Sources, src)
		}
	}
	return w
}

// Page is one page of the person's merged watches, newest first, and how
// many there are in all, merged by days in loc (the person's zone). media:
// "" every watch, "movie" films, "tv" episodes.
func (s *Service) Page(ctx context.Context, userID int32, loc *time.Location, media string, page, size int) ([]schema.Watch, int, error) {
	m := ""
	if media == "movie" || media == "tv" {
		m = historyMedia(media)
	}
	rows, err := s.q.HistoryPage(ctx, store.HistoryPageParams{UserID: userID, Media: m, Tz: loc.String(),
		Lim: int32(size), Off: int32((page - 1) * size)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]schema.Watch, 0, len(rows))
	total := 0
	for _, r := range rows {
		total = int(r.Total)
		out = append(out, watch(loc, r.Ids, r.MediaType, r.TmdbID, r.Title, r.Season, r.Episode, r.EpisodeTitle, r.WatchedAt, r.Sources))
	}
	if len(rows) == 0 {
		// A page past the end carries no total of its own.
		n, err := s.q.HistoryCount(ctx, store.HistoryCountParams{UserID: userID, Media: m, Tz: loc.String()})
		if err != nil {
			return nil, 0, err
		}
		total = int(n)
	}
	return out, total, nil
}

// TitleWatches is one title's merged watches, newest first (a show's: every
// episode's), merged by days in loc (the person's zone).
func (s *Service) TitleWatches(ctx context.Context, userID int32, loc *time.Location, media string, tmdbID int) ([]schema.Watch, error) {
	rows, err := s.q.TitleWatches(ctx, store.TitleWatchesParams{UserID: userID, TmdbID: ptr32(tmdbID),
		Media: historyMedia(media), Tz: loc.String()})
	if err != nil {
		return nil, err
	}
	out := make([]schema.Watch, 0, len(rows))
	for _, r := range rows {
		out = append(out, watch(loc, r.Ids, r.MediaType, r.TmdbID, r.Title, r.Season, r.Episode, r.EpisodeTitle, r.WatchedAt, r.Sources))
	}
	return out, nil
}

func ptr32(n int) *int32 {
	v := int32(n)
	return &v
}

// EpisodeWatches is a show's watches per (season, episode), newest first.
func (s *Service) EpisodeWatches(ctx context.Context, userID int32, loc *time.Location, showID int) (map[[2]int][]schema.Watch, error) {
	all, err := s.TitleWatches(ctx, userID, loc, "tv", showID)
	if err != nil {
		return nil, err
	}
	out := map[[2]int][]schema.Watch{}
	for _, w := range all {
		if w.Season != nil && w.Episode != nil {
			k := [2]int{*w.Season, *w.Episode}
			out[k] = append(out[k], w)
		}
	}
	return out, nil
}

// Mark is one watch to record.
type Mark struct {
	Media        string // "movie" or "tv"
	TMDBID       int
	Title        string
	When         time.Time // zero: now
	Season       *int
	Episode      *int
	EpisodeTitle *string
	Source       string // "" is "manual"
}

// MarkWatched records a watch and returns its id. A film marked watched
// leaves the watchlist (MarkWatched is for watches marked in Movie Night;
// plays reported by players should not take a film off the watchlist).
// Call it on a WithTx service to keep both in one transaction.
func (s *Service) MarkWatched(ctx context.Context, userID int32, m Mark) (int, error) {
	when := m.When
	if when.IsZero() {
		when = time.Now()
	}
	src := m.Source
	if src == "" {
		src = "manual"
	}
	var season, episode *int32
	if m.Season != nil {
		season = ptr32(*m.Season)
	}
	if m.Episode != nil {
		episode = ptr32(*m.Episode)
	}
	id, err := s.q.InsertWatch(ctx, store.InsertWatchParams{UserID: userID, MediaType: historyMedia(m.Media),
		TmdbID: ptr32(m.TMDBID), Season: season, Episode: episode, EpisodeTitle: m.EpisodeTitle, Title: m.Title,
		WatchedAt: when, Source: src})
	if err != nil {
		return 0, err
	}
	if m.Media == "movie" {
		if err := s.q.WatchlistDropMovie(ctx, store.WatchlistDropMovieParams{UserID: userID, TmdbID: int32(m.TMDBID)}); err != nil {
			return 0, err
		}
	}
	return int(id), nil
}

// RemoveEntries deletes the person's watches by id; with tmdbID > 0, only
// that title's. Returns how many went.
func (s *Service) RemoveEntries(ctx context.Context, userID int32, ids []int, media string, tmdbID int) (int, error) {
	raw := make([]int32, len(ids))
	for i, id := range ids {
		raw[i] = int32(id)
	}
	n, err := s.q.RemoveWatches(ctx, store.RemoveWatchesParams{UserID: userID, Ids: raw, TmdbID: int32(tmdbID),
		Media: historyMedia(media)})
	return int(n), err
}

// Stats is Profile's numbers; "this year" is the current year in loc (the
// person's zone), its watches dated by their days there.
func (s *Service) Stats(ctx context.Context, userID int32, loc *time.Location) (schema.Stats, error) {
	r, err := s.q.HistoryStats(ctx, store.HistoryStatsParams{UserID: userID, Tz: loc.String(),
		Year: int32(time.Now().In(loc).Year())})
	if err != nil {
		return schema.Stats{}, err
	}
	return schema.Stats{Movies: int(r.Movies), MoviePlays: int(r.MoviePlays), Episodes: int(r.Episodes),
		Shows: int(r.Shows), Hours: int(r.Hours), ThisYear: int(r.ThisYear), Ratings: int(r.Ratings),
		Watchlist: int(r.Watchlist)}, nil
}

// Prompt is the film the rate prompt asks about.
type Prompt struct {
	Key    string
	TMDBID int
	Title  string // as the watch recorded it
}

// RatePrompt is the newest film watched in the last RatePromptDays days that
// has no rating and wasn't dismissed; nil when there is none. The window is
// a length of time back from now, not calendar days, so it needs no zone.
func (s *Service) RatePrompt(ctx context.Context, userID int32) (*Prompt, error) {
	var dismissed []string
	if _, err := s.settings.User(ctx, userID, RateDismissedKey, &dismissed); err != nil {
		return nil, fmt.Errorf("rate prompt: %w", err)
	}
	if dismissed == nil {
		dismissed = []string{}
	}
	r, err := s.q.RatePrompt(ctx, store.RatePromptParams{UserID: userID,
		Since: time.Now().Add(-RatePromptDays * 24 * time.Hour), Dismissed: dismissed})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Prompt{Key: titles.Key("movie", int(r.TmdbID)), TMDBID: int(r.TmdbID), Title: r.Title}, nil
}

// DismissRatePrompt remembers "Not now" for a title key, for good.
func (s *Service) DismissRatePrompt(ctx context.Context, userID int32, key string) error {
	return s.q.DismissRatePrompt(ctx, store.DismissRatePromptParams{UserID: userID, Key: key})
}
