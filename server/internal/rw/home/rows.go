package home

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/airows"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/homecfg"
	"movienight/server/internal/rows"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// buildRows is one Home row key's rows:
// "continue" (landscape), "because" and "ai" (several rows), or one row of
// any other key. The note says why "Picked for you" has no rows.
func (r *routes) buildRows(ctx context.Context, u *store.User, tab, key string) ([]schema.Row, *string, error) {
	switch key {
	case "continue":
		items, err := r.continueRow(ctx, u, tab)
		if err != nil || len(items) == 0 {
			return []schema.Row{}, nil, err
		}
		return []schema.Row{{Landscape: &schema.LandscapeRow{ID: key, Title: homecfg.RowTitle(tab, key, nil), Style: "landscape", Items: items}}}, nil, nil
	case "because":
		got, err := r.because(ctx, u, tab)
		return got, nil, err
	case "ai":
		return r.aiRows(ctx, u)
	}
	page, err := r.rows.Load(ctx, *u, tab, key, 1, rows.RowSize, "", "list")
	if err != nil {
		return nil, nil, err
	}
	items, err := r.rows.Cards(ctx, *u, page.Metas)
	if err != nil || len(items) == 0 {
		return []schema.Row{}, nil, err
	}
	row := &schema.PosterRow{ID: key, Style: "poster", Items: items}
	if rest, ok := strings.CutPrefix(key, "list:"); ok {
		id, _ := strconv.Atoi(rest)
		l, err := r.lists.GetVisible(ctx, u.ID, int32(id))
		if err != nil {
			return nil, nil, err
		}
		by, group := homecfg.ListByLine(l), homecfg.ListGroup(l)
		row.Title, row.Subtitle, row.ListID, row.Group = l.Name, &by, &l.ID, &group
	} else {
		row.Title = homecfg.RowTitle(tab, key, nil)
	}
	if page.More || len(items) >= rows.RowSize {
		row.More = &schema.RowMore{Key: key, Title: row.Title}
	}
	return []schema.Row{{Poster: row}}, nil, nil
}

// ---------------------------------------------------------------------------
// The hero banner
// ---------------------------------------------------------------------------

// heroCards is the big swipeable banner: the two newest library additions
// with a backdrop, then up to five trending titles not yet watched. TMDB
// being down leaves the library's two alone (a deliberate degrade).
func (r *routes) heroCards(ctx context.Context, u *store.User, tab string) ([]schema.Card, error) {
	media := homecfg.TabMedia(tab)
	keys, _, err := r.rows.PersonalKeys(ctx, *u, "recent", media, 6, 0)
	if err != nil {
		return nil, err
	}
	metas, err := r.d.Titles.Ensure(ctx, keys, 6)
	if err != nil {
		return nil, err
	}
	var lib []*titles.Meta
	for _, k := range keys {
		if m := metas[k]; m != nil {
			lib = append(lib, m)
		}
	}
	trend, err := r.rows.Trending(ctx, media, 1)
	if err != nil && !errors.Is(err, rows.ErrTMDBDown) {
		return nil, err
	}
	hot := r.rows.Remember(ctx, trend, media)
	cards, err := r.rows.Cards(ctx, *u, append(append([]*titles.Meta{}, lib...), hot...))
	if err != nil {
		return nil, err
	}
	byKey := map[string]schema.Card{}
	for _, c := range cards {
		byKey[c.Key] = c
	}
	var out []schema.Card
	taken := map[string]bool{}
	for _, m := range lib {
		if c := byKey[m.Key]; c.Backdrop != nil && len(out) < 2 {
			label := "New in your library"
			c.HeroLabel = &label
			out = append(out, c)
			taken[c.Key] = true
		}
	}
	n := 0
	for _, m := range hot {
		c := byKey[m.Key]
		if c.Backdrop == nil || c.Watched > 0 || taken[c.Key] || n >= 5 {
			continue
		}
		label := "Trending"
		c.HeroLabel = &label
		out = append(out, c)
		taken[c.Key] = true
		n++
	}
	info := map[string]*titles.Meta{}
	for _, m := range append(lib, hot...) {
		info[m.Key] = m
	}
	for i := range out {
		if m := info[out[i].Key]; m != nil {
			out[i].Overview = m.Overview
			g := m.Genres
			if len(g) > 3 {
				g = g[:3]
			}
			out[i].Genres = append([]string{}, g...)
		}
	}
	if out == nil {
		out = []schema.Card{}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Up next: Jellyfin's "Continue watching" and "Next up"
// ---------------------------------------------------------------------------

// Up next is cached for continueTTL under cache.ContinueKey; everything
// that changes what the person watched forgets it (see there).
const continueTTL = 60 * time.Second

type continueData struct {
	Resume []clients.JSON `json:"resume"`
	Next   []clients.JSON `json:"next"`
}

// continueRow is every item of the person's Jellyfin "Continue watching"
// and "Next up" (no cap), one per show, as wide items. No linked Jellyfin:
// no row. Jellyfin failing: no row, logged (a deliberate degrade: Home
// still loads).
func (r *routes) continueRow(ctx context.Context, u *store.User, tab string) ([]schema.UpNextItem, error) {
	if u.JellyfinUserID == nil || *u.JellyfinUserID == "" {
		return nil, nil
	}
	jfUser := *u.JellyfinUserID
	got, err := cache.Cached(ctx, r.d.Cache, cache.ContinueKey(u.ID), cache.Options{TTL: continueTTL},
		func(ctx context.Context) (continueData, error) {
			resume, err := r.d.Clients.Jellyfin.Resume(ctx, jfUser)
			if err != nil {
				return continueData{}, err
			}
			next, err := r.d.Clients.Jellyfin.NextUp(ctx, jfUser)
			return continueData{Resume: resume, Next: next}, err
		})
	if err != nil {
		r.d.Log.WarnContext(ctx, "Jellyfin's Up next couldn't be read; the row is left out", "err", err)
		return nil, nil
	}
	media := homecfg.TabMedia(tab)
	all := append(append([]clients.JSON{}, got.Resume...), got.Next...)
	var seriesIDs []string
	for _, it := range all {
		if s, _ := it["SeriesId"].(string); s != "" {
			seriesIDs = append(seriesIDs, s)
		}
	}
	series := map[string]int{}
	if len(seriesIDs) > 0 {
		rows, err := r.d.Q.SeriesTMDBIDs(ctx, seriesIDs)
		if err != nil {
			return nil, err
		}
		for _, s := range rows {
			series[s.JellyfinID] = int(s.TmdbID)
		}
	}
	str := func(m clients.JSON, k string) *string {
		if s, ok := m[k].(string); ok && s != "" {
			return &s
		}
		return nil
	}
	intOf := func(m clients.JSON, k string) *int {
		if f, ok := m[k].(float64); ok {
			n := int(f)
			return &n
		}
		return nil
	}
	var out []schema.UpNextItem
	var keys []string
	seen := map[string]bool{}
	for _, it := range all {
		kind, _ := it["Type"].(string)
		e := schema.UpNextItem{JellyfinID: str(it, "Id"), ImageItem: str(it, "Id"), SeriesItem: str(it, "SeriesId")}
		switch kind {
		case "Movie":
			if media == "tv" {
				continue
			}
			ids, _ := it["ProviderIds"].(map[string]any)
			if t, _ := ids["Tmdb"].(string); t != "" {
				if n, err := strconv.Atoi(t); err == nil && n > 0 {
					k := titles.Key("movie", n)
					e.Key = &k
				}
			}
			e.Title, _ = it["Name"].(string)
		case "Episode":
			if media == "movie" {
				continue
			}
			sid, _ := it["SeriesId"].(string)
			if seen[sid] {
				continue // one per show
			}
			seen[sid] = true
			if tid, ok := series[sid]; ok {
				k := titles.Key("tv", tid)
				e.Key = &k
			}
			e.Title, _ = it["SeriesName"].(string)
			sn, en := "?", "?"
			if s := intOf(it, "ParentIndexNumber"); s != nil {
				sn = strconv.Itoa(*s)
			}
			if n := intOf(it, "IndexNumber"); n != nil {
				en = strconv.Itoa(*n)
			}
			sub := fmt.Sprintf("S%s · E%s", sn, en)
			if name, _ := it["Name"].(string); name != "" {
				sub += " – " + name
			}
			e.Subtitle, e.Season, e.Episode = &sub, intOf(it, "ParentIndexNumber"), intOf(it, "IndexNumber")
		default:
			continue
		}
		if ud, ok := it["UserData"].(map[string]any); ok {
			if p, ok := ud["PlayedPercentage"].(float64); ok {
				e.Progress = int(math.Round(p))
			}
		}
		if ticks, ok := it["RunTimeTicks"].(float64); ok {
			if m := int(math.Round(ticks / 600_000_000)); m > 0 {
				e.RuntimeMin = &m
			}
		}
		if e.Key != nil {
			keys = append(keys, *e.Key)
		}
		out = append(out, e)
	}
	metas, err := r.d.Titles.Ensure(ctx, keys, len(keys))
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Key == nil {
			continue
		}
		m, id, _ := titles.Split(*out[i].Key)
		mt := schema.MediaType(m)
		out[i].MediaType, out[i].TMDBID = &mt, &id
		if meta := metas[*out[i].Key]; meta != nil {
			out[i].Backdrop, out[i].Poster = meta.BackdropPath, meta.PosterPath
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// "Because you watched" and the rotation of row sets
// ---------------------------------------------------------------------------

// Sets and rows per set of "Because you watched" / "Picked for you".
const (
	sets        = 3
	rowsPerSet  = 5
	pinRecent   = 24 * time.Hour
	seedsWanted = sets * rowsPerSet
)

// rotationIndex is the set on show: morning (5–12), afternoon (12–18) and
// night (18–5) each get a different set, in the person's own zone (tz:
// rw.Deps.Zone), and the sets move on by one each day, so someone who only
// opens the app at night still sees every set within three days. The small hours count with the
// evening before.
func rotationIndex(now time.Time, tz *time.Location) int {
	local := now.In(tz)
	h := local.Hour()
	part := 2
	if h >= 5 && h < 12 {
		part = 0
	} else if h >= 12 && h < 18 {
		part = 1
	}
	// Python's date.toordinal(): 0001-01-01 is day 1.
	y, m, d := local.Date()
	day := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()/86400) + 719163
	if h < 5 {
		day--
	}
	return (part + day) % sets
}

// seed is one title "Because you watched" starts from.
type seed struct {
	media string
	id    int
	name  string
	last  time.Time
}

// becauseSet is one set's seeds: the pool (newest first) dealt round robin
// into sets, so each mixes recent and older watches; anything watched in
// the last day leads every set.
func becauseSet(pool []seed, index int, now time.Time) []seed {
	if len(pool) == 0 {
		return nil
	}
	var pinned []seed
	isPinned := map[int]bool{}
	for i := 0; i < len(pool) && i < sets; i++ {
		if now.Sub(pool[i].last) <= pinRecent {
			pinned = append(pinned, pool[i])
			isPinned[i] = true
		}
	}
	out := pinned
	for i := index % sets; i < len(pool); i += sets {
		if !isPinned[i] {
			out = append(out, pool[i])
		}
	}
	if len(out) > rowsPerSet {
		out = out[:rowsPerSet]
	}
	return out
}

func (r *routes) because(ctx context.Context, u *store.User, tab string) ([]schema.Row, error) {
	got, err := r.d.Q.RecentSeeds(ctx, store.RecentSeedsParams{UserID: u.ID, Media: homecfg.TabMedia(tab), Lim: seedsWanted})
	if err != nil {
		return nil, err
	}
	pool := make([]seed, len(got))
	for i, s := range got {
		pool[i] = seed{s.Media, int(s.TmdbID), s.Title, s.Last}
	}
	now := time.Now()
	chosen := becauseSet(pool, rotationIndex(now, r.d.Zone(*u)), now)
	perSeed := make([][]*titles.Meta, len(chosen))
	var all []*titles.Meta
	for i, s := range chosen {
		results, _ := r.rows.Recommendations(ctx, s.media, s.id, 1, false) // trouble: the row is left out (logged)
		perSeed[i] = r.rows.Remember(ctx, results, s.media)
		all = append(all, perSeed[i]...)
	}
	st, err := r.d.State.Load(ctx, *u, rows.KeysOf(all))
	if err != nil {
		return nil, err
	}
	out := []schema.Row{}
	kicker := "Because you watched"
	for i, s := range chosen {
		var items []schema.Card
		for _, c := range st.Cards(perSeed[i]) {
			if c.Watched == 0 && len(items) < rows.RowSize {
				items = append(items, c)
			}
		}
		if len(items) == 0 {
			continue
		}
		k := kicker
		out = append(out, schema.Row{Poster: &schema.PosterRow{
			ID: fmt.Sprintf("because:%s:%d", s.media, s.id), Kicker: &k, Title: s.name, Style: "poster", Items: items,
			More: &schema.RowMore{Key: fmt.Sprintf("recs:%s:%d", s.media, s.id), Title: "Because you watched " + s.name},
		}})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// "Picked for you": the AI's rows, read from their cache
// ---------------------------------------------------------------------------

// The AI rows are made by the worker, not here (the Python rebuilt them in
// a background task of the web process, which a second replica would have
// done again). Package airows is the one contract between the two: Serve
// answers the cached rows and, when they are stale, queues a rebuild for a
// worker; Note says why there are none; BasisLine says what the model read.

func (r *routes) aiRows(ctx context.Context, u *store.User) ([]schema.Row, *string, error) {
	if !r.d.Cfg.AIEnabled() {
		return []schema.Row{}, nil, nil
	}
	cached, basis, err := r.d.AIRows.Serve(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}

	// One set of five rows at a time (all of them when there are five or fewer).
	chosen := cached
	if len(chosen) > rowsPerSet {
		var groups [][]int
		for i := 0; i < len(chosen); i += rowsPerSet {
			var g []int
			for j := i; j < min(i+rowsPerSet, len(chosen)); j++ {
				g = append(g, j)
			}
			groups = append(groups, g)
		}
		pick := groups[rotationIndex(time.Now(), r.d.Zone(*u))%len(groups)]
		picked := chosen[:0:0]
		for _, j := range pick {
			picked = append(picked, chosen[j])
		}
		chosen = picked
	}
	var keys []string
	for _, row := range chosen {
		keys = append(keys, row.Keys...)
	}
	metas, err := r.d.Titles.Ensure(ctx, keys, 60)
	if err != nil {
		return nil, nil, err
	}
	st, err := r.d.State.Load(ctx, *u, keys)
	if err != nil {
		return nil, nil, err
	}
	out := []schema.Row{}
	kicker := "Picked for you"
	for i, row := range chosen {
		ms := make([]*titles.Meta, 0, len(row.Keys))
		for _, k := range row.Keys {
			ms = append(ms, metas[k])
		}
		var items []schema.Card
		for _, c := range st.Cards(ms) {
			if c.Watched == 0 {
				items = append(items, c)
			}
		}
		if len(items) == 0 {
			continue
		}
		k := kicker
		out = append(out, schema.Row{Poster: &schema.PosterRow{ID: fmt.Sprintf("ai:%d", i), Kicker: &k, Title: row.Theme, Style: "poster", Items: items}})
	}
	if len(out) > 0 {
		// What the AI read, under the first row (docs/limits.md, Recommendations).
		out[0].Poster.Subtitle = airows.BasisLine(basis)
		return out, nil, nil
	}
	note, err := r.d.AIRows.Note(ctx, u.ID, false)
	return out, note, err
}
