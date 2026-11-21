// Package episodes is a show's seasons and episodes for one person: TMDB's
// episode list with their ticks (watched), hearts and ratings, what is in
// the Jellyfin library, the next episode to play, and ticking or unticking
// episodes. Ticking also marks them played in
// the person's Jellyfin (unticking unplays them), so Jellyfin's Next Up
// agrees.
//
// What changed from the Python, on purpose:
//
//   - Ticking a whole season is one statement (TickEpisodes /
//     UntickEpisodes): all of it is recorded or none, and it costs one round
//     trip. The Python committed each episode on its own, so a failure half
//     way left half a season ticked.
//   - "Today" (has it aired?) is the person's day (their zone, else the
//     install's TIMEZONE), not the server's.
//   - The Jellyfin marks run after the write, detached from the request,
//     with their own deadline; a failure is logged (the tick itself stands,
//     as in the Python).
package episodes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/history"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/tmdbcache"
	"movienight/server/internal/users"
)

// JellyfinEpisodesTTL is how long a show's Jellyfin episodes are kept.
const JellyfinEpisodesTTL = 120 * time.Second

// Service is the episodes service.
type Service struct {
	q       *store.Queries
	cfg     *config.Config
	cache   *cache.Cache
	clients *clients.Set
	history *history.Service
	titles  *titles.Service
	marks   Marker
	library Library
	log     *slog.Logger
}

// Library is the episodes Movie Night's own libraries hold
// (libraries.Service.Episodes).
type Library interface {
	Episodes(ctx context.Context, showID int) ([][2]int, error)
}

// Marker records Jellyfin played marks for a worker to make
// (jfsync.Service.AskMarks).
type Marker interface {
	AskMarks(ctx context.Context, userID int32, items []string, played bool, when time.Time) error
}

// New makes the service.
func New(q *store.Queries, cfg *config.Config, c *cache.Cache, cl *clients.Set, h *history.Service,
	t *titles.Service, m Marker, lib Library, log *slog.Logger) *Service {
	return &Service{q: q, cfg: cfg, cache: c, clients: cl, history: h, titles: t, marks: m, library: lib, log: log}
}

// zone is the person's own time zone (users.Location): their watches are
// merged by their days, and "aired by today" is their today.
func (s *Service) zone(u store.User) *time.Location { return users.Location(u, s.cfg.Location) }

// today is the person's date: an episode airing "on the 12th" counts as out
// once it is the 12th where they are.
func (s *Service) today(u store.User) string { return time.Now().In(s.zone(u)).Format("2006-01-02") }

// Aired reports whether an air date ("2011-04-17") is today or before.
func Aired(date, today string) bool {
	if len(date) < 10 {
		return false
	}
	if _, err := time.Parse("2006-01-02", date[:10]); err != nil {
		return false
	}
	return date[:10] <= today
}

// libraryUser is whose Jellyfin view to read: the person's own, else the
// install owner's (JELLYFIN_USER_ID).
func (s *Service) libraryUser(u store.User) string {
	if u.JellyfinUserID != nil && *u.JellyfinUserID != "" {
		return *u.JellyfinUserID
	}
	return s.cfg.JellyfinUserID
}

// jellyfinEpisodes is (season, episode) -> Jellyfin episode item for a show
// in the library. Jellyfin trouble leaves the season without library marks
// (logged): the page still shows.
func (s *Service) jellyfinEpisodes(ctx context.Context, u store.User, showID, season int) map[[2]int]clients.JSON {
	out := map[[2]int]clients.JSON{}
	show := int32(showID)
	series, err := s.q.LibraryItemFor(ctx, store.LibraryItemForParams{MediaType: "tv", TmdbID: &show})
	uid := s.libraryUser(u)
	if err != nil || uid == "" {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.log.WarnContext(ctx, "library lookup failed", "err", err)
		}
		return out
	}
	jf := s.clients.Jellyfin
	items, err := cache.Cached(ctx, s.cache, fmt.Sprintf(cache.JellyfinPrefix+"eps:%s:%s:%d", uid, series, season),
		cache.Options{TTL: JellyfinEpisodesTTL}, func(ctx context.Context) ([]clients.JSON, error) {
			return jf.Episodes(ctx, series, uid, season)
		})
	if err != nil {
		s.log.WarnContext(ctx, "Jellyfin episodes failed", "err", err)
		return out
	}
	for _, it := range items {
		sn, ok1 := it["ParentIndexNumber"].(float64)
		en, ok2 := it["IndexNumber"].(float64)
		if ok1 && ok2 && it["LocationType"] != "Virtual" {
			out[[2]int{int(sn), int(en)}] = it
		}
	}
	return out
}

func num(m clients.JSON, k string) (int, bool) {
	f, ok := m[k].(float64)
	return int(f), ok
}

func strp(m clients.JSON, k string) *string {
	if s, ok := m[k].(string); ok && s != "" {
		return &s
	}
	return nil
}

// SeasonPage is a season's episodes for this person; nil when TMDB has no
// such season.
func (s *Service) SeasonPage(ctx context.Context, u store.User, showID, number int) (*schema.SeasonOut, error) {
	data, err := tmdbcache.Season(ctx, s.cache, s.clients.TMDB, showID, number)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	jfEps := s.jellyfinEpisodes(ctx, u, showID, number)
	libEps := map[[2]int]bool{}
	if s.library != nil {
		eps, err := s.library.Episodes(ctx, showID)
		if err != nil {
			// As with Jellyfin: the page shows, without the library marks.
			s.log.WarnContext(ctx, "library episodes failed", "err", err)
		}
		for _, e := range eps {
			libEps[e] = true
		}
	}
	watched, err := s.history.EpisodeWatches(ctx, u.ID, s.zone(u), showID)
	if err != nil {
		return nil, err
	}
	raw, _ := data["episodes"].([]any)
	keys := make([][3]int, 0, len(raw))
	for _, x := range raw {
		if e, ok := x.(map[string]any); ok {
			if n, ok := num(e, "episode_number"); ok {
				keys = append(keys, [3]int{showID, number, n})
			}
		}
	}
	marks, err := s.Marks(ctx, u.ID, keys)
	if err != nil {
		return nil, err
	}
	today := s.today(u)
	out := &schema.SeasonOut{ShowID: showID, Number: number, Name: strp(data, "name"), Overview: strp(data, "overview"),
		AirDate: strp(data, "air_date"), Poster: strp(data, "poster_path"), Episodes: []schema.Episode{}}
	for _, x := range raw {
		e, _ := x.(map[string]any)
		n, ok := num(e, "episode_number")
		if !ok {
			continue
		}
		name := fmt.Sprintf("Episode %d", n)
		if v := strp(e, "name"); v != nil {
			name = *v
		}
		ep := schema.Episode{Number: n, Name: name, Overview: strp(e, "overview"), AirDate: strp(e, "air_date"),
			Still: strp(e, "still_path"), WatchIDs: []int{}}
		if ep.AirDate != nil {
			ep.Aired = Aired(*ep.AirDate, today)
		}
		if rt, ok := num(e, "runtime"); ok && rt > 0 {
			ep.Runtime = &rt
		}
		if v, ok := e["vote_average"].(float64); ok && v != 0 {
			r := math.Round(v*10) / 10
			ep.Vote = &r
		}
		seen := watched[[2]int{number, n}]
		ep.Watched = len(seen)
		for _, w := range seen {
			ep.WatchIDs = append(ep.WatchIDs, w.IDs...)
		}
		if len(seen) > 0 {
			at := seen[0].WatchedAt
			ep.LastWatchedAt = &at
		}
		ep.InLibrary = libEps[[2]int{number, n}]
		if jf, ok := jfEps[[2]int{number, n}]; ok {
			ep.InLibrary = true
			if id, _ := jf["Id"].(string); id != "" {
				ep.JellyfinID = &id
			}
		}
		m := marks[[3]int{showID, number, n}]
		ep.Favorite, ep.Rating = m.Favorite, m.Rating
		out.Episodes = append(out.Episodes, ep)
	}
	return out, nil
}

// Mark is a person's own heart and rating of one episode.
type Mark struct {
	Favorite bool
	Rating   *int
}

// Marks is the person's own heart and rating of each episode given as
// (show tmdb id, season, episode): for the places that show an episode on
// its own (season page, History, Up next), so they show the episode's and
// never the show's. Every key asked for is in the answer.
func (s *Service) Marks(ctx context.Context, userID int32, eps [][3]int) (map[[3]int]Mark, error) {
	out := make(map[[3]int]Mark, len(eps))
	shows := map[int32]bool{}
	for _, k := range eps {
		out[k] = Mark{}
		shows[int32(k[0])] = true
	}
	if len(shows) == 0 {
		return out, nil
	}
	ids := make([]int32, 0, len(shows))
	for id := range shows {
		ids = append(ids, id)
	}
	rows, err := s.q.EpisodeMarksForShows(ctx, store.EpisodeMarksForShowsParams{UserID: userID, Shows: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		k := [3]int{int(r.TmdbID), int(r.Season), int(r.Episode)}
		m, wanted := out[k]
		if !wanted {
			continue
		}
		if r.Liked {
			m.Favorite = true
		} else if r.Rating != nil {
			v := int(*r.Rating)
			m.Rating = &v
		}
		out[k] = m
	}
	return out, nil
}

// NextEpisode is the episode to play next: after the last one ticked, or
// S1E1; nil when the person is caught up with what has aired. It carries
// that episode's own heart and rating.
func (s *Service) NextEpisode(ctx context.Context, u store.User, showID int) (*schema.NextEpisode, error) {
	d, err := tmdbcache.Details(ctx, s.cache, s.clients.TMDB, "tv", showID)
	if err != nil || d == nil {
		return nil, err
	}
	counts := map[int]int{}
	seasons, _ := d["seasons"].([]any)
	for _, x := range seasons {
		sm, _ := x.(map[string]any)
		if n, ok := num(sm, "season_number"); ok && n != 0 {
			c, _ := num(sm, "episode_count")
			counts[n] = c
		}
	}
	if len(counts) == 0 {
		return nil, nil
	}
	last, _ := d["last_episode_to_air"].(map[string]any)
	ls, _ := num(last, "season_number")
	le, _ := num(last, "episode_number")
	watched, err := s.history.EpisodeWatches(ctx, u.ID, s.zone(u), showID)
	if err != nil {
		return nil, err
	}
	var regular [][2]int
	for k := range watched {
		if k[0] >= 1 {
			regular = append(regular, k)
		}
	}
	var sn, en int
	if len(regular) > 0 {
		sort.Slice(regular, func(i, j int) bool {
			return regular[i][0] < regular[j][0] || (regular[i][0] == regular[j][0] && regular[i][1] < regular[j][1])
		})
		top := regular[len(regular)-1]
		sn, en = top[0], top[1]+1
		if en > counts[sn] {
			sn, en = sn+1, 1
		}
		for {
			c, ok := counts[sn]
			if !ok || c != 0 {
				break
			}
			sn++
		}
	} else {
		sn, en = math.MaxInt, 1
		for n := range counts {
			sn = min(sn, n)
		}
	}
	if _, ok := counts[sn]; !ok || sn > ls || (sn == ls && en > le) {
		return nil, nil
	}
	verb := "Play"
	if len(regular) > 0 {
		verb = "Continue"
	}
	marks, err := s.Marks(ctx, u.ID, [][3]int{{showID, sn, en}})
	if err != nil {
		return nil, err
	}
	m := marks[[3]int{showID, sn, en}]
	return &schema.NextEpisode{Season: sn, Episode: en, Label: fmt.Sprintf("%s S%02dE%02d", verb, sn, en),
		Favorite: m.Favorite, Rating: m.Rating}, nil
}

// SetWatched ticks (watched) or unticks episodes of a show, all in one
// statement, and returns how many changed. when zero is now.
func (s *Service) SetWatched(ctx context.Context, u store.User, showID int, eps [][2]int, watched bool, when time.Time) (int, error) {
	seen := map[[2]int]bool{}
	var uniq [][2]int
	for _, e := range eps {
		if !seen[e] {
			seen[e] = true
			uniq = append(uniq, e)
		}
	}
	var changed [][2]int
	if watched {
		key := titles.Key("tv", showID)
		metas, err := s.titles.Ensure(ctx, []string{key}, 1)
		if err != nil {
			return 0, err
		}
		title := ""
		if m := metas[key]; m != nil {
			title = m.Title
		}
		// Episode names from TMDB's season (cached), so the history reads
		// "S1E2 – The Name". A season TMDB can't send now is ticked unnamed.
		names := map[[2]int]string{}
		for season := range seasonsOf(uniq) {
			data, err := tmdbcache.Season(ctx, s.cache, s.clients.TMDB, showID, season)
			if err != nil {
				s.log.WarnContext(ctx, "TMDB season for episode names failed; ticked without names", "err", err)
				continue
			}
			list, _ := data["episodes"].([]any)
			for _, x := range list {
				e, _ := x.(map[string]any)
				if n, ok := num(e, "episode_number"); ok {
					if nm, _ := e["name"].(string); nm != "" {
						names[[2]int{season, n}] = nm
					}
				}
			}
		}
		if when.IsZero() {
			when = time.Now()
		}
		p := store.TickEpisodesParams{UserID: u.ID, TmdbID: int32(showID), Title: title, WatchedAt: when}
		for _, e := range uniq {
			p.Seasons = append(p.Seasons, int32(e[0]))
			p.Episodes = append(p.Episodes, int32(e[1]))
			p.Names = append(p.Names, names[e])
		}
		rows, err := s.q.TickEpisodes(ctx, p)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			changed = append(changed, [2]int{int(*r.Season), int(*r.Episode)})
		}
	} else {
		keys := make([]string, len(uniq))
		for i, e := range uniq {
			keys[i] = strconv.Itoa(e[0]) + ":" + strconv.Itoa(e[1])
		}
		show := int32(showID)
		rows, err := s.q.UntickEpisodes(ctx, store.UntickEpisodesParams{UserID: u.ID, TmdbID: &show, Keys: keys})
		if err != nil {
			return 0, err
		}
		gone := map[[2]int]bool{}
		for _, r := range rows {
			k := [2]int{int(*r.Season), int(*r.Episode)}
			if !gone[k] {
				gone[k] = true
				changed = append(changed, k)
			}
		}
	}
	if len(changed) > 0 && u.JellyfinUserID != nil && *u.JellyfinUserID != "" {
		s.markInJellyfin(ctx, u, showID, changed, watched, when)
	}
	return len(changed), nil
}

func seasonsOf(eps [][2]int) map[int]bool {
	out := map[int]bool{}
	for _, e := range eps {
		out[e[0]] = true
	}
	return out
}

// markInJellyfin asks for the changed episodes to be set played (or
// unplayed) in the person's Jellyfin; a worker does it and then makes their
// Up next row stale (jfsync.AskMarks).
func (s *Service) markInJellyfin(ctx context.Context, u store.User, showID int, changed [][2]int, played bool, when time.Time) {
	var items []string
	for season := range seasonsOf(changed) {
		got := s.jellyfinEpisodes(ctx, u, showID, season)
		for _, k := range changed {
			if it, ok := got[k]; ok {
				if id, _ := it["Id"].(string); id != "" {
					items = append(items, id)
				}
			}
		}
	}
	if len(items) == 0 {
		return
	}
	// A worker makes the calls (jfsync's marks queue); the person has their
	// answer already. A failure to ask is logged: the ticks are saved here
	// either way.
	if err := s.marks.AskMarks(ctx, u.ID, items, played, when); err != nil {
		s.log.WarnContext(ctx, "Jellyfin played marks could not be asked for", "user", u.ID, "err", err)
	}
}
