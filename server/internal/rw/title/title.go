// Package title is a title's page and everything done on it: details,
// live request status, scores, related titles, the Seerr request, the
// person's watchlist, watches, rating and favourite, the lists it is in,
// and its seasons and episodes (ticks, hearts, ratings).
//
// Playing (/play) belongs to the playback routes, not here.
package title

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/episodes"
	"movienight/server/internal/history"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/tmdbcache"
	"movienight/server/internal/userstate"
)

// RecsTTL is how long a title's TMDB recommendations are kept (the Home
// rows' "Because you watched" read the same key, recs:<media>:<id>:<page>).
const RecsTTL = 12 * time.Hour

// CollectionTTL is how long a film collection's parts are kept.
const CollectionTTL = 6 * time.Hour

// Title is the routes and the services they use.
type Title struct {
	d        *rw.Deps
	history  *history.Service
	episodes *episodes.Service
}

const base = "/api/v1/title/{media}/{tmdb_id}"

// TitleIn is every route's path: the media and TMDB's id. Exported:
// Huma reads the parameters of an embedded struct only when it is.
type TitleIn struct {
	Media  string `path:"media" enum:"movie,tv"`
	TMDBID int    `path:"tmdb_id" exclusiveMinimum:"0" maximum:"10000000000"`
}

// EpisodeIn adds one episode. A season numbered by its year (2023) and a
// long episode number are fine: only what an integer column can't hold is
// refused.
type EpisodeIn struct {
	TitleIn
	Season  int `path:"season" minimum:"0" maximum:"2147483647"`
	Episode int `path:"episode" minimum:"0" maximum:"2147483647"`
}

func tvOnly(media string) error {
	if media != "tv" {
		return rw.NotFound("Only shows have seasons")
	}
	return nil
}

// Register adds the routes.
func Register(d *rw.Deps) {
	t := &Title{d: d, history: d.History, episodes: episodes.New(d.Q, d.Cfg, d.Cache, d.Clients, d.History, d.Titles, d.JFSync, d.Libraries, d.Log)}
	t.register()
	d.Limit("RatingIn", "review", rw.TextChars)
	d.Limit("IdsIn", "ids", rw.BatchItems)
	d.Limit("EpisodesIn", "episodes", rw.BatchItems)
	d.Limit("RequestIn", "seasons", rw.BatchItems)
}

func op(id, method, path, summary string, desc ...string) huma.Operation {
	return rw.Op(id, method, base+path, summary, "titles", desc...)
}

func (t *Title) user(ctx context.Context) (store.User, error) {
	id, err := t.d.User(ctx)
	if err != nil {
		return store.User{}, err
	}
	return id.User, nil
}

func (t *Title) register() {
	a := t.d.Huma()

	huma.Register(a, op("get_title_api_v1_title__media___tmdb_id__get", http.MethodGet, "", "Get Title"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.TitleDetail], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			out, err := t.build(ctx, u, in.Media, in.TMDBID)
			if errors.Is(err, errTMDB) {
				t.d.Log.WarnContext(ctx, "title page: TMDB failed", "err", err)
				return nil, rw.Err(http.StatusBadGateway, "Couldn't reach TMDB for this title")
			}
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			if out == nil {
				return nil, rw.NotFound("Title not found")
			}
			return rw.Out(*out), nil
		})

	huma.Register(a, op("get_status_api_v1_title__media___tmdb_id__status_get", http.MethodGet, "/status", "Get Status",
		"Fresh request and download state, for the polling request button. Seerr's record is shared\n"+
			"by every poller for a few seconds (one call per title per window)."),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.TitleState], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			st, err := t.liveState(ctx, u, in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(st), nil
		})

	huma.Register(a, op("get_scores_api_v1_title__media___tmdb_id__scores_get", http.MethodGet, "/scores", "Get Scores",
		"IMDb, Rotten Tomatoes, Metacritic, Letterboxd and Trakt scores, and a\nplain note when MDBList couldn't refresh them (it refused or didn't\nanswer)."),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.ScoresOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			out, err := t.scoresAndNote(ctx, t.d.Zone(u), in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})

	huma.Register(a, op("get_related_api_v1_title__media___tmdb_id__related_get", http.MethodGet, "/related", "Get Related",
		"\"More like this\" and, for movies, the rest of the collection."),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.RelatedOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			out, err := t.related(ctx, u, in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})

	huma.Register(a, op("request_title_api_v1_title__media___tmdb_id__request_post", http.MethodPost, "/request", "Request Title",
		"Send the request to Seerr as this person. A 409 from Seerr (it already\nhas one) is not an error: the button moves to that request's status.\nShows can ask for some seasons only."),
		func(ctx context.Context, in *struct {
			TitleIn
			Body *schema.RequestIn `required:"false"`
		}) (*rw.Body[schema.RequestOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			var seasons []int
			if in.Media == "tv" && in.Body != nil {
				seasons = in.Body.Seasons
			}
			for _, n := range seasons {
				// Huma cannot bound an array's items by tag; checked here.
				if n < 0 || n > 2147483647 {
					return nil, rw.Err(http.StatusUnprocessableEntity, "Seasons: Input should be greater than or equal to 0.")
				}
			}
			already, dry, err := t.createRequest(ctx, u, in.Media, in.TMDBID, seasons)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			// The new request shows at once: Seerr's record is read afresh
			// and put into the shared map every card reads.
			t.d.Cache.Forget(ctx, seerrInfoKey(in.Media, in.TMDBID))
			info := t.seerrInfo(ctx, in.Media, in.TMDBID)
			t.d.State.Seerr().Put(ctx, userstate.Key{Media: in.Media, ID: in.TMDBID}, info)
			st, err := t.d.State.LoadKeys(ctx, u, []userstate.Key{{Media: in.Media, ID: in.TMDBID}})
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			state := t.withSeerr(st.Of(in.Media, in.TMDBID), info)
			switch {
			case dry:
				s := "dry_run"
				state.Request = &s
			case state.Request == nil && !state.InLibrary:
				s := "pending" // Seerr has the request but hasn't indexed it yet
				state.Request = &s
			}
			return rw.Out(schema.RequestOut{Ok: true, Already: already, State: state}), nil
		})

	// The person's marks: each one statement (title_marks.sql).
	key := func(u store.User, in *TitleIn) (int32, string, int32) { return u.ID, in.Media, int32(in.TMDBID) }

	huma.Register(a, op("add_watchlist_api_v1_title__media___tmdb_id__watchlist_post", http.MethodPost, "/watchlist", "Add Watchlist"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.WatchlistOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			uid, m, id := key(u, in)
			if err := t.d.Q.WatchlistAdd(ctx, store.WatchlistAddParams{UserID: uid, MediaType: m, TmdbID: id}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.WatchlistOut{Ok: true, Watchlist: true}), nil
		})

	huma.Register(a, op("remove_watchlist_api_v1_title__media___tmdb_id__watchlist_delete", http.MethodDelete, "/watchlist", "Remove Watchlist"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.WatchlistOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			uid, m, id := key(u, in)
			if err := t.d.Q.WatchlistRemove(ctx, store.WatchlistRemoveParams{UserID: uid, MediaType: m, TmdbID: id}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.WatchlistOut{Ok: true, Watchlist: false}), nil
		})

	huma.Register(a, op("add_favorite_api_v1_title__media___tmdb_id__favorite_post", http.MethodPost, "/favorite", "Add Favorite"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.FavoriteOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			uid, m, id := key(u, in)
			if err := t.d.Q.FavoriteAdd(ctx, store.FavoriteAddParams{UserID: uid, MediaType: m, TmdbID: id}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.FavoriteOut{Ok: true, Favorite: true}), nil
		})

	huma.Register(a, op("remove_favorite_api_v1_title__media___tmdb_id__favorite_delete", http.MethodDelete, "/favorite", "Remove Favorite"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.FavoriteOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			uid, m, id := key(u, in)
			if err := t.d.Q.FavoriteRemove(ctx, store.FavoriteRemoveParams{UserID: uid, MediaType: m, TmdbID: id}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.FavoriteOut{Ok: true, Favorite: false}), nil
		})

	huma.Register(a, op("add_watched_api_v1_title__media___tmdb_id__watched_post", http.MethodPost, "/watched", "Add Watched"),
		func(ctx context.Context, in *struct {
			TitleIn
			Body schema.WatchedIn
		}) (*rw.Body[schema.WatchesOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			when, err := parseWhen(in.Body.WatchedAt, t.d.Zone(u), time.Now())
			if err != nil {
				return nil, err
			}
			k := titles.Key(in.Media, in.TMDBID)
			metas, err := t.d.Titles.Ensure(ctx, []string{k}, 1)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			name := ""
			if m := metas[k]; m != nil {
				name = m.Title
			}
			id, err := t.history.MarkWatched(ctx, u.ID, history.Mark{Media: in.Media, TMDBID: in.TMDBID, Title: name, When: when})
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			watches, err := t.history.TitleWatches(ctx, u.ID, t.d.Zone(u), in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.WatchesOut{Ok: true, ID: &id, Watches: watches}), nil
		})

	huma.Register(a, op("remove_watched_api_v1_title__media___tmdb_id__unwatch_post", http.MethodPost, "/unwatch", "Remove Watched"),
		func(ctx context.Context, in *struct {
			TitleIn
			Body schema.IdsIn
		}) (*rw.Body[schema.WatchesOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := t.history.RemoveEntries(ctx, u.ID, in.Body.IDs, in.Media, in.TMDBID); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			watches, err := t.history.TitleWatches(ctx, u.ID, t.d.Zone(u), in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.WatchesOut{Ok: true, Watches: watches}), nil
		})

	huma.Register(a, op("set_rating_api_v1_title__media___tmdb_id__rating_post", http.MethodPost, "/rating", "Set Rating"),
		func(ctx context.Context, in *struct {
			TitleIn
			Body schema.RatingIn
		}) (*rw.Body[schema.RatingOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			// A comment sent replaces the one kept (blank clears it); a
			// rating sent without one keeps it.
			var review *string
			if in.Body.Review != nil {
				if r := strings.TrimSpace(*in.Body.Review); r != "" {
					review = &r
				}
			}
			kept, err := t.d.Q.RatingSet(ctx, store.RatingSetParams{UserID: u.ID, MediaType: in.Media, TmdbID: int32(in.TMDBID),
				Rating: int32(in.Body.Rating), Review: review, SetReview: in.Body.Review != nil})
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			r := in.Body.Rating
			return rw.Out(schema.RatingOut{Ok: true, Rating: &r, Review: kept}), nil
		})

	huma.Register(a, op("clear_rating_api_v1_title__media___tmdb_id__rating_delete", http.MethodDelete, "/rating", "Clear Rating"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.RatingOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			uid, m, id := key(u, in)
			if err := t.d.Q.RatingClear(ctx, store.RatingClearParams{UserID: uid, MediaType: m, TmdbID: id}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.RatingOut{Ok: true}), nil
		})

	huma.Register(a, op("title_lists_api_v1_title__media___tmdb_id__lists_get", http.MethodGet, "/lists", "Title Lists"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.ListsForTitleOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			lists, err := t.memberships(ctx, u.ID, in.Media, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.ListsForTitleOut{Lists: lists}), nil
		})

	t.registerEpisodes()
}

// related is "More like this" (TMDB's recommendations, or similar titles
// when it has none) and, for a film, the rest of its collection, oldest
// first. TMDB trouble leaves a section out (logged): the title page shows
// without it, as in the Python.
func (t *Title) related(ctx context.Context, u store.User, media string, id int) (schema.RelatedOut, error) {
	tmdb := t.d.Clients.TMDB
	recs, err := cache.Cached(ctx, t.d.Cache, "recs:"+media+":"+strconv.Itoa(id)+":1", cache.Options{TTL: RecsTTL},
		func(ctx context.Context) ([]clients.JSON, error) {
			got, err := tmdb.Recommendations(ctx, media, id, 1)
			if err == nil && len(got) == 0 {
				got, err = tmdb.Similar(ctx, media, id, 1)
			}
			return got, err
		})
	if err != nil {
		t.d.Log.WarnContext(ctx, "recommendations failed", "err", err)
		recs = nil
	}
	recMetas := titles.NormalizeAll(recs, media)
	var collMetas []*titles.Meta
	var collName *string
	haveColl := false
	if media == "movie" {
		d, err := tmdbcache.Details(ctx, t.d.Cache, tmdb, media, id)
		if err != nil {
			t.d.Log.WarnContext(ctx, "details for the collection failed", "err", err)
		}
		if c, _ := d["belongs_to_collection"].(map[string]any); c != nil {
			if cid, ok := c["id"].(float64); ok && cid > 0 {
				coll, err := cache.Cached(ctx, t.d.Cache, "collection:"+strconv.Itoa(int(cid)), cache.Options{TTL: CollectionTTL},
					func(ctx context.Context) (clients.JSON, error) { return tmdb.Collection(ctx, int(cid)) })
				if err != nil {
					t.d.Log.InfoContext(ctx, "collection unavailable", "err", err)
				} else {
					parts := objs(coll["parts"])
					date := func(p map[string]any) string {
						if s, _ := p["release_date"].(string); s != "" {
							return s
						}
						return "9999"
					}
					sort.SliceStable(parts, func(i, j int) bool { return date(parts[i]) < date(parts[j]) })
					js := make([]clients.JSON, len(parts))
					for i, p := range parts {
						js[i] = p
					}
					collMetas, collName, haveColl = titles.NormalizeAll(js, "movie"), sp(c, "name"), true
				}
			}
		}
	}
	all := append(append([]*titles.Meta{}, recMetas...), collMetas...)
	t.d.Titles.Remember(ctx, all)
	keys := make([]string, len(all))
	for i, m := range all {
		keys[i] = m.Key
	}
	st, err := t.d.State.Load(ctx, u, keys)
	if err != nil {
		return schema.RelatedOut{}, err
	}
	out := schema.RelatedOut{Recommendations: st.Cards(recMetas)}
	if haveColl {
		out.Collection = &schema.RelatedCollection{Name: collName, Items: st.Cards(collMetas)}
	}
	return out, nil
}
