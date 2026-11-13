package title

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/tmdbcache"
	"movienight/server/internal/userstate"
)

// SeerrInfoTTL is how long Seerr's live record of one title is shared by
// everyone polling it. The title page's request button polls /status every
// 5 to 8 seconds while a download runs; the Python asked Seerr afresh on
// every poll, from every open page. Now one call per title per window
// serves them all.
const SeerrInfoTTL = 5 * time.Second

// SeerrInfoKeep is how long a copy of it is kept for when Seerr stops
// answering (the Python's cache.peek degrade: the button keeps its last
// known state).
const SeerrInfoKeep = 10 * time.Minute

// ongoing are TMDB's statuses of a show still being made.
var ongoing = map[string]bool{"Returning Series": true, "In Production": true, "Planned": true, "Pilot": true}

func seerrInfoKey(media string, id int) string { return fmt.Sprintf("seerr-info:%s:%d", media, id) }

// seerrInfo is Seerr's live record of a title (nil when Seerr is off or
// never heard of it). Seerr trouble with no kept copy is logged and reads
// as nil: the page shows without a request status rather than failing.
func (t *Title) seerrInfo(ctx context.Context, media string, id int) clients.JSON {
	s := t.d.Clients.Seerr
	if !s.Enabled() {
		return nil
	}
	info, _, err := cache.CachedOrCopy(ctx, t.d.Cache, seerrInfoKey(media, id), cache.Options{TTL: SeerrInfoTTL, Keep: SeerrInfoKeep},
		func(ctx context.Context) (clients.JSON, error) { return s.MediaInfo(ctx, media, id) })
	if err != nil {
		t.d.Log.WarnContext(ctx, "Seerr status failed", "err", err)
		return nil
	}
	return info
}

// withSeerr folds Seerr's live record into the title state the request
// button reads.
func (t *Title) withSeerr(st schema.TitleState, info clients.JSON) schema.TitleState {
	if info == nil {
		return st
	}
	f, _ := info["status"].(float64)
	status := userstate.StatusLabel(int(f))
	if jf, _ := info["jellyfinMediaId"].(string); status != nil && *status == "available" && jf != "" && st.JellyfinID == nil {
		st.JellyfinID = &jf
		st.JellyfinURL = userstate.JellyfinURL(t.d.Cfg.JellyfinPublicURL, jf)
		st.InLibrary = true
	}
	st.Request = status
	if st.InLibrary && (status == nil || *status == "available") {
		st.Request = nil
	}
	st.Progress = userstate.DownloadProgress(info["downloadStatus"])
	reqs, _ := info["requests"].([]any)
	latest := ""
	for _, x := range reqs {
		r, _ := x.(map[string]any)
		if c, _ := r["createdAt"].(string); c > latest {
			latest = c
		}
	}
	if latest != "" {
		st.RequestedAt = &latest
	}
	return st
}

// liveState is a title's state with Seerr's live record: the polling
// button's answer.
func (t *Title) liveState(ctx context.Context, u store.User, media string, id int) (schema.TitleState, error) {
	st, err := t.d.State.LoadKeys(ctx, u, []userstate.Key{{Media: media, ID: id}})
	if err != nil {
		return schema.TitleState{}, err
	}
	return t.withSeerr(st.Of(media, id), t.seerrInfo(ctx, media, id)), nil
}

func sp(m map[string]any, k string) *string {
	if s, ok := m[k].(string); ok && s != "" {
		return &s
	}
	return nil
}

func ip(m map[string]any, k string) *int {
	if f, ok := m[k].(float64); ok {
		n := int(f)
		return &n
	}
	return nil
}

func objs(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func names(v any, key string) []string {
	out := []string{}
	for _, m := range objs(v) {
		if s, _ := m[key].(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func certification(d clients.JSON, media, region string) *string {
	if media == "movie" {
		rd, _ := d["release_dates"].(map[string]any)
		for _, r := range objs(rd["results"]) {
			if r["iso_3166_1"] == region {
				for _, x := range objs(r["release_dates"]) {
					if c := sp(x, "certification"); c != nil {
						return c
					}
				}
			}
		}
		return nil
	}
	cr, _ := d["content_ratings"].(map[string]any)
	for _, r := range objs(cr["results"]) {
		if r["iso_3166_1"] == region {
			if c := sp(r, "rating"); c != nil {
				return c
			}
		}
	}
	return nil
}

func providers(d clients.JSON, region string) schema.Providers {
	out := schema.Providers{Stream: []schema.StreamProvider{}, Rent: []schema.StreamProvider{}, Buy: []schema.StreamProvider{}}
	wp, _ := d["watch/providers"].(map[string]any)
	res, _ := wp["results"].(map[string]any)
	r, _ := res[region].(map[string]any)
	if r == nil {
		return out
	}
	pick := func(kind string) []schema.StreamProvider {
		list := objs(r[kind])
		prio := func(m map[string]any) float64 {
			if f, ok := m["display_priority"].(float64); ok && f != 0 {
				return f
			}
			return 99
		}
		sort.SliceStable(list, func(i, j int) bool { return prio(list[i]) < prio(list[j]) })
		ps := make([]schema.StreamProvider, 0, len(list))
		for _, p := range list {
			ps = append(ps, schema.StreamProvider{ID: ip(p, "provider_id"), Name: sp(p, "provider_name"), Logo: sp(p, "logo_path")})
		}
		return ps
	}
	out.Stream, out.Rent, out.Buy, out.Link = pick("flatrate"), pick("rent"), pick("buy"), sp(r, "link")
	return out
}

// trailer is the best YouTube video: an official trailer, then any
// trailer, then a teaser, then any video.
func trailer(d clients.JSON) *string {
	vids, _ := d["videos"].(map[string]any)
	var yt []map[string]any
	for _, v := range objs(vids["results"]) {
		if v["site"] == "YouTube" && sp(v, "key") != nil {
			yt = append(yt, v)
		}
	}
	rank := func(v map[string]any) int {
		n := 0
		if o, _ := v["official"].(bool); !o {
			n += 2
		}
		if v["type"] != "Trailer" {
			n++
		}
		return n
	}
	sort.SliceStable(yt, func(i, j int) bool { return rank(yt[i]) < rank(yt[j]) })
	url := func(v map[string]any) *string {
		u := "https://www.youtube.com/watch?v=" + v["key"].(string)
		return &u
	}
	for _, kind := range []string{"Trailer", "Teaser"} {
		for _, v := range yt {
			if v["type"] == kind {
				return url(v)
			}
		}
	}
	if len(yt) > 0 {
		return url(yt[0])
	}
	return nil
}

func episodeRef(v any) *schema.EpisodeRef {
	e, _ := v.(map[string]any)
	if len(e) == 0 {
		return nil
	}
	return &schema.EpisodeRef{Season: ip(e, "season_number"), Episode: ip(e, "episode_number"), Name: sp(e, "name"), AirDate: sp(e, "air_date")}
}

// community is everyone's ratings and comments of a title on this install,
// newest first, all of them, with the average and count.
func (t *Title) community(ctx context.Context, userID int32, media string, id int) (*schema.Community, error) {
	rows, err := t.d.Q.Community(ctx, store.CommunityParams{MediaType: media, TmdbID: int32(id)})
	if err != nil {
		return nil, err
	}
	out := &schema.Community{Others: []schema.RatingEntry{}}
	total := 0
	for _, r := range rows {
		name := r.Username
		if r.DisplayName != nil && *r.DisplayName != "" {
			name = *r.DisplayName
		}
		e := schema.RatingEntry{Name: name, Rating: int(r.Rating), Review: r.Review, RatedAt: rw.ISOTime(r.RatedAt), Mine: r.UserID == userID}
		total += int(r.Rating)
		out.Count++
		if e.Mine {
			out.Mine = &e
		} else {
			out.Others = append(out.Others, e)
		}
	}
	if out.Count > 0 {
		avg := math.Round(float64(total)/float64(out.Count)*10) / 10
		out.Average = &avg
	}
	return out, nil
}

func (t *Title) resume(ctx context.Context, userID int32, media string, id int) (*schema.Resume, error) {
	r, err := t.d.Q.ResumeFor(ctx, store.ResumeForParams{UserID: userID, Media: media, MovieKey: titles.Key("movie", id), TmdbID: int32(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &schema.Resume{Season: i32(r.Season), Episode: i32(r.Episode), EpisodeTitle: r.EpisodeTitle,
		PositionS: int(r.PositionS), DurationS: i32(r.DurationS), State: r.State, Client: r.Client, Device: r.Device,
		UpdatedAt: rw.ISOTime(r.UpdatedAt)}
	if r.DurationS != nil && *r.DurationS > 0 {
		p := int(math.Round(100 * float64(r.PositionS) / float64(*r.DurationS)))
		out.Percent = &p
	}
	return out, nil
}

func i32(p *int32) *int {
	if p == nil {
		return nil
	}
	n := int(*p)
	return &n
}

// memberships: the person's own hand-made lists and whether the title is
// in each (curated lists are fixed, so they are not offered). The same
// query lists.Service.Memberships runs (db/queries/lists_lists.sql).
func (t *Title) memberships(ctx context.Context, userID int32, media string, id int) ([]schema.ListMembership, error) {
	rows, err := t.d.Q.Memberships(ctx, store.MembershipsParams{Media: media, TmdbID: int32(id), UserID: &userID})
	if err != nil {
		return nil, err
	}
	out := make([]schema.ListMembership, 0, len(rows))
	for _, r := range rows {
		out = append(out, schema.ListMembership{ID: r.ID, Name: r.Name, InList: r.InList, Source: r.Source})
	}
	return out, nil
}

// remember writes a title page's metadata to the title cache when that
// changes something (never seen, only list-sourced, or a day old): the
// Python wrote it on every view.
func (t *Title) remember(ctx context.Context, m *titles.Meta) {
	have, err := t.d.Q.TitleFreshness(ctx, []string{m.Key})
	if err == nil && len(have) == 1 && have[0].Detailed && time.Since(have[0].FetchedAt) < titles.ListStale {
		return
	}
	if err := t.d.Titles.Upsert(ctx, []*titles.Meta{m}, true); err != nil {
		// The page has its data; a cache write that fails is logged only.
		t.d.Log.WarnContext(ctx, "title cache write failed", "err", err)
	}
}

// errTMDB marks TMDB trouble for the page's 502.
var errTMDB = errors.New("TMDB")

// build is a title's whole page; nil when TMDB has no such title.
func (t *Title) build(ctx context.Context, u store.User, media string, id int) (*schema.TitleDetail, error) {
	var d clients.JSON
	var info clients.JSON
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		d, err = tmdbcache.Details(gctx, t.d.Cache, t.d.Clients.TMDB, media, id)
		if err != nil {
			return fmt.Errorf("%w: %v", errTMDB, err)
		}
		return nil
	})
	g.Go(func() error { info = t.seerrInfo(gctx, media, id); return nil })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	meta := titles.Normalize(d, media)
	if meta == nil {
		return nil, nil
	}
	t.remember(ctx, meta)
	region := t.d.Cfg.WatchRegion
	credits, _ := d["credits"].(map[string]any)
	cast := []schema.CastMember{}
	// Everyone TMDB lists, in its order (no cut: the pages scroll the cast).
	for _, c := range objs(credits["cast"]) {
		cast = append(cast, schema.CastMember{ID: ip(c, "id"), Name: sp(c, "name"), Character: sp(c, "character"), Photo: sp(c, "profile_path")})
	}
	directors, writers := []string{}, []string{}
	seenD, seenW := map[string]bool{}, map[string]bool{}
	for _, c := range objs(credits["crew"]) {
		n, _ := c["name"].(string)
		if c["job"] == "Director" && !seenD[n] {
			seenD[n] = true
			directors = append(directors, n)
		}
		if c["department"] == "Writing" && !seenW[n] {
			seenW[n] = true
			writers = append(writers, n)
		}
	}
	seasons := []schema.Season{}
	if media == "tv" {
		directors = names(d["created_by"], "name")
		status := map[int]int{}
		for _, s := range objs(info["seasons"]) {
			n, _ := s["seasonNumber"].(float64)
			st, _ := s["status"].(float64)
			status[int(n)] = int(st)
		}
		watched, err := t.history.EpisodeWatches(ctx, u.ID, t.d.Zone(u), id)
		if err != nil {
			return nil, err
		}
		perSeason := map[int]int{}
		for k := range watched {
			perSeason[k[0]]++
		}
		for _, s := range objs(d["seasons"]) {
			n := ip(s, "season_number")
			if n == nil {
				continue
			}
			seasons = append(seasons, schema.Season{Number: *n, Name: sp(s, "name"), Episodes: ip(s, "episode_count"),
				AirDate: sp(s, "air_date"), Poster: sp(s, "poster_path"), Status: userstate.StatusLabel(status[*n]),
				Watched: perSeason[*n]})
		}
		// Specials last, as Jellyfin and Wako show them.
		sort.SliceStable(seasons, func(i, j int) bool {
			a, b := seasons[i].Number, seasons[j].Number
			if (a == 0) != (b == 0) {
				return b == 0
			}
			return a < b
		})
	}
	st, err := t.d.State.LoadKeys(ctx, u, []userstate.Key{{Media: media, ID: id}})
	if err != nil {
		return nil, err
	}
	state := t.withSeerr(st.Of(media, id), info)
	label := "Directed by"
	if media == "tv" {
		label = "Created by"
	}
	statusText := sp(d, "status")
	out := &schema.TitleDetail{
		Key: meta.Key, MediaType: schema.MediaType(media), TMDBID: id, Title: meta.Title, OriginalTitle: meta.OriginalTitle,
		Year: meta.Year, ReleaseDate: meta.ReleaseDate, Poster: meta.PosterPath, Backdrop: meta.BackdropPath,
		Overview: meta.Overview, Genres: nonNil(meta.Genres), Runtime: meta.Runtime, Vote: meta.Vote(), VoteCount: meta.VoteCount,
		IMDbID: meta.IMDbID, Tagline: sp(d, "tagline"), StatusText: statusText,
		Ongoing:       media == "tv" && statusText != nil && ongoing[*statusText],
		Certification: certification(d, media, region), Directors: directors, DirectorLabel: label, Writers: writers,
		Cast: cast, Studios: names(d["production_companies"], "name"), Networks: names(d["networks"], "name"),
		Languages: languages(d), Trailer: trailer(d), Providers: providers(d, region), Seasons: seasons,
		NumberOfSeasons: ip(d, "number_of_seasons"), NumberOfEpisodes: ip(d, "number_of_episodes"),
		LastEpisode: episodeRef(d["last_episode_to_air"]), NextEpisode: episodeRef(d["next_episode_to_air"]),
		TMDBURL: fmt.Sprintf("https://www.themoviedb.org/%s/%d", media, id), JellyfinURL: state.JellyfinURL, State: state,
	}
	if meta.IMDbID != nil {
		u := "https://www.imdb.com/title/" + *meta.IMDbID + "/"
		out.IMDbURL = &u
	}
	if c, _ := d["belongs_to_collection"].(map[string]any); c != nil {
		out.Collection = &schema.Collection{ID: ip(c, "id"), Name: sp(c, "name"), Poster: sp(c, "poster_path"), Backdrop: sp(c, "backdrop_path")}
	}
	if out.Scores, err = t.keptScores(ctx, media, id); err != nil {
		return nil, err
	}
	if out.Community, err = t.community(ctx, u.ID, media, id); err != nil {
		return nil, err
	}
	if out.Watches, err = t.history.TitleWatches(ctx, u.ID, t.d.Zone(u), media, id); err != nil {
		return nil, err
	}
	if out.Lists, err = t.memberships(ctx, u.ID, media, id); err != nil {
		return nil, err
	}
	if out.Resume, err = t.resume(ctx, u.ID, media, id); err != nil {
		return nil, err
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func languages(d clients.JSON) []string {
	out := []string{}
	for _, l := range objs(d["spoken_languages"]) {
		if n := sp(l, "english_name"); n != nil {
			out = append(out, *n)
		} else if n := sp(l, "name"); n != nil {
			out = append(out, *n)
		}
	}
	return out
}
