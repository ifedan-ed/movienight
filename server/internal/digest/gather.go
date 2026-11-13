package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// watch is one watch of a title (or episode) on one day: the same watch can
// arrive twice (a mark here that Jellyfin later reports too), so rows for
// the same title and episode on the same day count once.
type watch struct {
	mediaType string // "movie" or "episode"
	key       string // the title's cache key; "" for a watch without a TMDB id
	tmdbID    int
	title     string
	season    *int32
	episode   *int32
	at        time.Time // the newest row's time
}

// groupWatches merges rows (newest first) into one watch per title or
// episode and local day, newest first. The day is the person's (loc, their
// zone), where the Python took the UTC day: a film watched at 23:30 in New
// York and marked again at 00:10 is one evening's watch, not two.
func groupWatches(rows []store.History, loc *time.Location) []watch {
	type id struct {
		media, title, day string
		tmdb              int32
		season, episode   int32
		hasS, hasE        bool
	}
	seen := map[id]bool{}
	var out []watch
	for _, h := range rows {
		k := id{media: h.MediaType, day: h.WatchedAt.In(loc).Format("2006-01-02")}
		if h.TmdbID != nil {
			k.tmdb = *h.TmdbID
		} else {
			k.title = h.Title
		}
		if h.Season != nil {
			k.season, k.hasS = *h.Season, true
		}
		if h.Episode != nil {
			k.episode, k.hasE = *h.Episode, true
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		w := watch{mediaType: h.MediaType, title: h.Title, season: h.Season, episode: h.Episode, at: h.WatchedAt}
		if h.TmdbID != nil {
			w.tmdbID = int(*h.TmdbID)
			media := "movie"
			if h.MediaType == "episode" {
				media = "tv"
			}
			w.key = titles.Key(media, w.tmdbID)
		}
		out = append(out, w)
	}
	return out
}

// short is the first n characters of a long text with "…", so it is plain
// there is more (counted in characters, not bytes).
func short(text string, n int) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return strings.TrimRight(string(r[:n]), " \t\n") + "…"
}

func strPtr(s string) *string { return &s }

// counter counts genres, remembering first-seen order so ties come out in
// the order they first appeared (as Python's Counter.most_common).
type counter struct {
	order []string
	n     map[string]int
}

func (c *counter) add(s string) {
	if c.n == nil {
		c.n = map[string]int{}
	}
	if c.n[s] == 0 {
		c.order = append(c.order, s)
	}
	c.n[s]++
}

func (c *counter) top(k int) []string {
	out := append([]string(nil), c.order...)
	sort.SliceStable(out, func(i, j int) bool { return c.n[out[i]] > c.n[out[j]] })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// horizonDays: how far ahead "Coming from your watchlist" looks.
var horizonDays = map[string]int{"week": 21, "month": 45, "year": 60}

// Gather reads everything one person's digest for [start, end) is made of.
func (s *Service) Gather(ctx context.Context, user store.User, kind string, start, end time.Time) (Data, error) {
	loc := s.zone(user) // start and end are local midnights here
	q := s.q
	rows, err := q.DigestHistory(ctx, store.DigestHistoryParams{UserID: user.ID, Start: start, PeriodEnd: end})
	if err != nil {
		return Data{}, err
	}
	// Newest first for the grouping, then oldest first for the email.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	watches := groupWatches(rows, loc)
	for i, j := 0, len(watches)-1; i < j; i, j = i+1, j-1 {
		watches[i], watches[j] = watches[j], watches[i]
	}

	ratingRows, err := q.DigestRatings(ctx, user.ID)
	if err != nil {
		return Data{}, err
	}
	type rkey struct {
		media string
		id    int32
	}
	ratings := map[rkey]store.DigestRatingsRow{}
	for _, r := range ratingRows {
		ratings[rkey{r.MediaType, r.TmdbID}] = r
	}
	var ratedNow []store.DigestRatingsRow
	for _, r := range ratingRows {
		if !r.RatedAt.Before(start) && r.RatedAt.Before(end) {
			ratedNow = append(ratedNow, r)
		}
	}
	sort.SliceStable(ratedNow, func(i, j int) bool { return ratedNow[i].Rating > ratedNow[j].Rating })

	libRows, err := q.DigestLibraryAdded(ctx, store.DigestLibraryAddedParams{Start: start, PeriodEnd: end})
	if err != nil {
		return Data{}, err
	}
	libTotal, err := q.DigestLibraryAddedCount(ctx, store.DigestLibraryAddedCountParams{Start: start, PeriodEnd: end})
	if err != nil {
		return Data{}, err
	}
	soonRows, err := q.DigestWatchlist(ctx, user.ID)
	if err != nil {
		return Data{}, err
	}

	// The titles to name: everything watched, the library's newest, and
	// what was rated (the Python left a rating's name out when the title
	// was not watched in the same period: an empty line in "Ratings").
	var want, libKeys, soonKeys []string
	for _, w := range watches {
		if w.key != "" {
			want = append(want, w.key)
		}
	}
	for _, l := range libRows {
		k := titles.Key(l.MediaType, int(l.TmdbID))
		libKeys = append(libKeys, k)
		want = append(want, k)
	}
	for _, r := range ratedNow {
		want = append(want, titles.Key(r.MediaType, int(r.TmdbID)))
	}
	for _, w := range soonRows {
		soonKeys = append(soonKeys, titles.Key(w.MediaType, int(w.TmdbID)))
	}
	metas, err := s.titles.Ensure(ctx, want, 80)
	if err != nil {
		return Data{}, err
	}
	// The watchlist from the cache only (it can be long; a title never
	// cached has no release date to show anyway).
	soonMetas, err := s.titles.GetMany(ctx, soonKeys)
	if err != nil {
		return Data{}, err
	}
	for k, m := range soonMetas {
		metas[k] = m
	}

	info := func(key, fallback string) Title {
		m, ok := metas[key]
		t := Title{Title: fallback, Genres: []string{}}
		if key != "" {
			t.Key = strPtr(key)
			media, id, _ := titles.Split(key)
			t.MediaType, t.TMDBID = media, id
		}
		if ok {
			if m.Title != "" {
				t.Title = m.Title
			}
			t.Year, t.Poster, t.Runtime = m.Year, m.PosterPath, m.Runtime
			if m.Genres != nil {
				t.Genres = m.Genres
			}
			if m.Overview != nil {
				t.Overview = short300(*m.Overview)
			}
		}
		return t
	}

	var films []Film
	var genres counter
	for _, w := range watches {
		if w.mediaType != "movie" {
			continue
		}
		f := Film{Title: info(w.key, w.title), WatchedOn: w.at.In(loc).Format("Mon Jan 2")}
		if w.key != "" {
			if r, ok := ratings[rkey{"movie", int32(w.tmdbID)}]; ok {
				f.Rating = &r.Rating
				if r.Review != nil && strings.TrimSpace(*r.Review) != "" {
					f.Review = strPtr(short(*r.Review, 400))
				}
			}
		}
		for _, g := range f.Genres {
			genres.add(g)
		}
		films = append(films, f)
	}

	type showAcc struct {
		Show
		eps map[[2]int32]bool
	}
	var shows []*showAcc
	byShow := map[string]*showAcc{}
	for _, w := range watches {
		if w.mediaType != "episode" {
			continue
		}
		k := w.key
		if k == "" {
			k = "title:" + w.title
		}
		sh := byShow[k]
		if sh == nil {
			sh = &showAcc{Show: Show{Title: info(w.key, w.title)}, eps: map[[2]int32]bool{}}
			byShow[k] = sh
			shows = append(shows, sh)
		}
		if w.season != nil && w.episode != nil {
			sh.eps[[2]int32{*w.season, *w.episode}] = true
		}
	}
	showList := make([]Show, 0, len(shows))
	for _, sh := range shows {
		eps := make([][2]int32, 0, len(sh.eps))
		for e := range sh.eps {
			eps = append(eps, e)
		}
		sort.Slice(eps, func(i, j int) bool {
			return eps[i][0] < eps[j][0] || (eps[i][0] == eps[j][0] && eps[i][1] < eps[j][1])
		})
		sh.Episodes = len(eps)
		if sh.Episodes == 0 {
			sh.Episodes = 1 // watched, episode unknown
		}
		if len(eps) > 0 {
			sh.Span = fmt.Sprintf("S%02dE%02d", eps[0][0], eps[0][1])
			if len(eps) > 1 {
				last := eps[len(eps)-1]
				sh.Span += fmt.Sprintf(" – S%02dE%02d", last[0], last[1])
			}
		}
		showList = append(showList, sh.Show)
	}
	sort.SliceStable(showList, func(i, j int) bool { return showList[i].Episodes > showList[j].Episodes })

	minutes, episodes := 0, 0
	for _, f := range films {
		if f.Runtime != nil {
			minutes += int(*f.Runtime)
		}
	}
	for _, sh := range showList {
		rt := 30 // an episode of unknown length counts as half an hour
		if sh.Runtime != nil {
			rt = int(*sh.Runtime)
		}
		minutes += rt * sh.Episodes
		episodes += sh.Episodes
	}

	favs, err := q.DigestFavoritesAdded(ctx, store.DigestFavoritesAddedParams{UserID: user.ID, Start: start, PeriodEnd: end})
	if err != nil {
		return Data{}, err
	}

	// "Coming from your watchlist": out between today and the horizon, in
	// the person's zone (the Python's date.today() was the server's).
	today := midnight(s.now(), loc)
	from, until := today.Format("2006-01-02"), today.AddDate(0, 0, horizonDays[kind]).Format("2006-01-02")
	var coming []Coming
	for _, k := range soonKeys {
		m := metas[k]
		if m.ReleaseDate == nil {
			continue
		}
		rd := *m.ReleaseDate
		if rd < from || rd > until {
			continue
		}
		d, err := time.Parse("2006-01-02", rd)
		if err != nil {
			continue // not a whole date ("2026"): nothing to show it under
		}
		coming = append(coming, Coming{Title: info(k, ""), Date: d.Format("Jan 2"), iso: rd})
	}
	sort.SliceStable(coming, func(i, j int) bool { return coming[i].iso < coming[j].iso })

	watched := map[string]bool{}
	for _, f := range films {
		if f.Key != nil {
			watched[*f.Key] = true
		}
	}
	picks, err := s.picks(ctx, user.ID, watched)
	if err != nil {
		return Data{}, err
	}

	var best *Film
	for i := range films {
		if films[i].Rating != nil && (best == nil || *films[i].Rating > *best.Rating) {
			best = &films[i]
		}
	}

	var library []Title
	for _, k := range libKeys {
		if m, ok := metas[k]; ok && m.PosterPath != nil {
			library = append(library, info(k, ""))
			if len(library) == 8 {
				break
			}
		}
	}

	rated := make([]Rated, 0, 8)
	for i, r := range ratedNow {
		if i == 8 {
			break
		}
		rated = append(rated, Rated{Title: metas[titles.Key(r.MediaType, int(r.TmdbID))].Title, Rating: r.Rating})
	}

	name := user.Username
	if user.DisplayName != nil && *user.DisplayName != "" {
		name = *user.DisplayName
	}
	d := Data{
		Kind: kind, Label: PeriodLabel(kind, start, end), Name: name,
		Films: nonNil(films), Shows: firstN(showList, 12),
		Totals: Totals{Films: len(films), Episodes: episodes, Hours: (minutes + 30) / 60},
		Rated:  rated, RatedTotal: len(ratedNow), NewFavorites: int(favs),
		Library: nonNil(library), Coming: firstN(coming, 6),
		// Lists cut short say how many more there are, so nothing is left
		// out without a word.
		More:      More{Shows: max(0, len(showList)-12), Library: max(0, int(libTotal)-len(library)), Coming: max(0, len(coming)-6)},
		TopGenres: genres.top(3), Best: best, Picks: picks,
	}
	if kind == "year" {
		d.Year = sumYear(watches, films, showList, &genres, len(ratedNow), int(favs), loc)
	}
	return d, nil
}

func short300(s string) string {
	r := []rune(s)
	if len(r) > 300 {
		return string(r[:300])
	}
	return s
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func firstN[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return nonNil(xs)
}

// sumYear: films and episodes month by month, the busiest month, the
// best-rated films (ties: the earlier watch), the most-watched shows, the
// first and the last film, and the favourite genres.
func sumYear(watches []watch, films []Film, shows []Show, genres *counter, rated, loved int, loc *time.Location) *Year {
	y := &Year{Months: make([]Month, 12), Rated: rated, Loved: loved, Genres: genres.top(5)}
	any := false
	for _, w := range watches {
		m := w.at.In(loc).Month() - 1
		if w.mediaType == "movie" {
			y.Months[m].Films++
		} else {
			y.Months[m].Episodes++
		}
		any = true
	}
	if any {
		busiest := 0
		for i, m := range y.Months {
			if m.Films+m.Episodes > y.Months[busiest].Films+y.Months[busiest].Episodes {
				busiest = i
			}
		}
		y.Busiest = strPtr(time.Month(busiest + 1).String())
	}
	var ranked []Film
	for _, f := range films {
		if f.Rating != nil {
			ranked = append(ranked, f)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return *ranked[i].Rating > *ranked[j].Rating })
	y.BestFilms = firstN(ranked, 8)
	y.TopShows = firstN(shows, 6)
	if len(films) > 0 {
		y.FirstFilm = &films[0]
	}
	if len(films) > 1 {
		y.LastFilm = &films[len(films)-1]
	}
	return y
}

// picks is up to four titles to watch next: unwatched library titles on the
// person's watchlist first, then their latest AI picks (three from each of
// the first two rows). Films watched this period are left out too.
func (s *Service) picks(ctx context.Context, userID int32, watchedNow map[string]bool) ([]Pick, error) {
	soon, err := s.q.DigestWatchlist(ctx, userID)
	if err != nil {
		return nil, err
	}
	var cands []string
	for _, w := range soon {
		cands = append(cands, titles.Key(w.MediaType, int(w.TmdbID)))
	}
	if raw, err := s.q.DigestLatestAIRows(ctx, userID); err == nil {
		var payload struct {
			Rows []struct {
				Keys []string `json:"keys"`
			} `json:"rows"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil {
			for i, row := range payload.Rows {
				if i == 2 {
					break
				}
				cands = append(cands, firstN(row.Keys, 3)...)
			}
		}
	} else if !isNoRows(err) {
		return nil, err
	}
	fromWatchlist := len(soon)

	// Which candidates are watched or in the library: asked for these ids
	// only.
	ids := make([]int32, 0, len(cands))
	for _, k := range cands {
		if _, id, ok := titles.Split(k); ok {
			ids = append(ids, int32(id))
		}
	}
	watchedRows, err := s.q.DigestWatchedAmong(ctx, store.DigestWatchedAmongParams{UserID: userID, Ids: ids})
	if err != nil {
		return nil, err
	}
	watched := map[string]bool{}
	for _, r := range watchedRows {
		watched[titles.Key(r.Media, int(r.TmdbID))] = true
	}
	libRows, err := s.q.DigestInLibraryAmong(ctx, ids)
	if err != nil {
		return nil, err
	}
	inLib := map[string]bool{}
	for _, r := range libRows {
		inLib[titles.Key(r.MediaType, int(r.TmdbID))] = true
	}

	var keys []string
	taken := map[string]bool{}
	for i, k := range cands {
		// From the watchlist only what is in the library; any AI pick.
		if (i < fromWatchlist && !inLib[k]) || watched[k] || watchedNow[k] || taken[k] {
			continue
		}
		taken[k] = true
		keys = append(keys, k)
		if len(keys) == 4 {
			break
		}
	}
	have, err := s.titles.Ensure(ctx, keys, 4)
	if err != nil {
		return nil, err
	}
	out := []Pick{}
	for _, k := range keys {
		m, ok := have[k]
		if !ok {
			continue
		}
		p := Pick{Key: k, Title: m.Title, Year: m.Year, Poster: m.PosterPath, InLibrary: inLib[k]}
		if m.Overview != nil {
			p.Overview = short300(*m.Overview)
		}
		out = append(out, p)
	}
	return out, nil
}
