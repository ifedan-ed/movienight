package title

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
)

// Seasons and episodes: the season page, "Up next", ticking episodes, and
// an episode's own heart and rating. Each write on one episode is a single
// statement (an upsert or a delete), so taps from two devices, or a
// phone's outbox replaying, can't race each other into an error: the last
// one to reach the database wins.
func (t *Title) registerEpisodes() {
	a := t.d.Huma()

	huma.Register(a, op("get_season_api_v1_title__media___tmdb_id__season__number__get", http.MethodGet, "/season/{number}", "Get Season"),
		func(ctx context.Context, in *struct {
			TitleIn
			Number int `path:"number" minimum:"0" maximum:"2147483647"`
		}) (*rw.Body[schema.SeasonOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			page, err := t.episodes.SeasonPage(ctx, u, in.TMDBID, in.Number)
			if err != nil {
				t.d.Log.WarnContext(ctx, "season page failed", "err", err)
				return nil, rw.Err(http.StatusBadGateway, "Couldn't reach TMDB for this season")
			}
			if page == nil {
				return nil, rw.NotFound("Season not found")
			}
			return rw.Out(*page), nil
		})

	huma.Register(a, op("get_next_api_v1_title__media___tmdb_id__next_get", http.MethodGet, "/next", "Get Next"),
		func(ctx context.Context, in *TitleIn) (*rw.Body[schema.NextEpisodeOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			next, err := t.episodes.NextEpisode(ctx, u, in.TMDBID)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.NextEpisodeOut{Next: next}), nil
		})

	huma.Register(a, op("mark_episodes_api_v1_title__media___tmdb_id__episodes_post", http.MethodPost, "/episodes", "Mark Episodes",
		"Tick or untick episodes (a whole season is a list of them), all at once."),
		func(ctx context.Context, in *struct {
			TitleIn
			Body schema.EpisodesIn
		}) (*rw.Body[schema.ChangedOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			when, err := parseWhen(in.Body.WatchedAt, t.d.Zone(u), time.Now())
			if err != nil {
				return nil, err
			}
			eps := make([][2]int, len(in.Body.Episodes))
			for i, e := range in.Body.Episodes {
				eps[i] = [2]int{e.Season, e.Episode}
			}
			n, err := t.episodes.SetWatched(ctx, u, in.TMDBID, eps, in.Body.Watched == nil || *in.Body.Watched, when)
			if err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.ChangedOut{Ok: true, Changed: n}), nil
		})

	ep := func(u store.User, in *EpisodeIn) (int32, int32, int32, int32) {
		return u.ID, int32(in.TMDBID), int32(in.Season), int32(in.Episode)
	}

	huma.Register(a, op("like_episode_api_v1_title__media___tmdb_id__episode__season___episode__favorite_post",
		http.MethodPost, "/episode/{season}/{episode}/favorite", "Like Episode",
		"Like one episode (the heart on the season page). Liking it again\nchanges nothing. It doesn't favourite the show."),
		func(ctx context.Context, in *EpisodeIn) (*rw.Body[schema.FavoriteOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			uid, show, s, e := ep(u, in)
			if err := t.d.Q.EpisodeFavoriteAdd(ctx, store.EpisodeFavoriteAddParams{UserID: uid, TmdbID: show, Season: s, Episode: e}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.FavoriteOut{Ok: true, Favorite: true}), nil
		})

	huma.Register(a, op("unlike_episode_api_v1_title__media___tmdb_id__episode__season___episode__favorite_delete",
		http.MethodDelete, "/episode/{season}/{episode}/favorite", "Unlike Episode",
		"Unlike one episode: its row is deleted. An episode that wasn't\nliked is not an error."),
		func(ctx context.Context, in *EpisodeIn) (*rw.Body[schema.FavoriteOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			uid, show, s, e := ep(u, in)
			if err := t.d.Q.EpisodeFavoriteRemove(ctx, store.EpisodeFavoriteRemoveParams{UserID: uid, TmdbID: show, Season: s, Episode: e}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.FavoriteOut{Ok: true, Favorite: false}), nil
		})

	// PUT only: the Python's POST alias was dropped (no client sends it).
	huma.Register(a, op("rate_episode_api_v1_title__media___tmdb_id__episode__season___episode__rating_put",
		http.MethodPut, "/episode/{season}/{episode}/rating", "Rate Episode",
		"Rate one episode, 1 to 10 like a title (the stars next to its heart on\nthe season page); rating it again replaces the rating. It doesn't rate\nthe show, and it isn't counted in Ratings, Your ratings, the community\naverage, the AI picks or digests."),
		func(ctx context.Context, in *struct {
			EpisodeIn
			Body schema.EpisodeRatingIn
		}) (*rw.Body[schema.EpisodeRatingOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			uid, show, s, e := ep(u, &in.EpisodeIn)
			if err := t.d.Q.EpisodeRatingSet(ctx, store.EpisodeRatingSetParams{UserID: uid, TmdbID: show, Season: s, Episode: e,
				Rating: int32(in.Body.Rating)}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			r := in.Body.Rating
			return rw.Out(schema.EpisodeRatingOut{Ok: true, Rating: &r}), nil
		})

	huma.Register(a, op("unrate_episode_api_v1_title__media___tmdb_id__episode__season___episode__rating_delete",
		http.MethodDelete, "/episode/{season}/{episode}/rating", "Unrate Episode",
		"Remove one episode's rating: its row is deleted. An episode that\nwasn't rated is not an error."),
		func(ctx context.Context, in *EpisodeIn) (*rw.Body[schema.EpisodeRatingOut], error) {
			u, err := t.user(ctx)
			if err != nil {
				return nil, err
			}
			if err := tvOnly(in.Media); err != nil {
				return nil, err
			}
			uid, show, s, e := ep(u, in)
			if err := t.d.Q.EpisodeRatingRemove(ctx, store.EpisodeRatingRemoveParams{UserID: uid, TmdbID: show, Season: s, Episode: e}); err != nil {
				return nil, t.d.Fail(ctx, err)
			}
			return rw.Out(schema.EpisodeRatingOut{Ok: true}), nil
		})
}
