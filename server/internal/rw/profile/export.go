package profile

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/rw"
)

// exportOut is the export download: the JSON and its file name.
type exportOut struct {
	ContentDisposition string `header:"Content-Disposition"`
	Body               map[string]any
}

type ref struct {
	MediaType string `json:"media_type"`
	TMDBID    int32  `json:"tmdb_id"`
}

// registerExport: GET /export, everything Movie Night keeps for the person
// as one JSON file. Each part is one ordered query (the Python's
// export, with the lists' items read in one query, not per list).
func registerExport(d *rw.Deps) {
	huma.Register(d.Huma(), rw.Op("export_api_v1_export_get", http.MethodGet, "/api/v1/export", "Export", "profile",
		"Everything Movie Night keeps for you, as one JSON file."),
		func(ctx context.Context, _ *struct{}) (*exportOut, error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			data, err := export(ctx, d, id.User.ID, id.User.Username)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return &exportOut{ContentDisposition: "attachment; filename=movienight-export.json", Body: data}, nil
		})
}

func export(ctx context.Context, d *rw.Deps, uid int32, username string) (map[string]any, error) {
	q := d.Q
	watchlist := []map[string]any{}
	wl, err := q.ExportWatchlist(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, w := range wl {
		watchlist = append(watchlist, map[string]any{"media_type": w.MediaType, "tmdb_id": w.TmdbID,
			"added_at": rw.ISOTime(w.AddedAt), "source": w.Source})
	}
	ratings := []map[string]any{}
	rs, err := q.ExportRatings(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		ratings = append(ratings, map[string]any{"media_type": r.MediaType, "tmdb_id": r.TmdbID, "rating": r.Rating,
			"review": r.Review, "rated_at": rw.ISOTime(r.RatedAt), "source": r.Source})
	}
	favorites := []ref{}
	fs, err := q.ExportFavorites(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		favorites = append(favorites, ref{f.MediaType, f.TmdbID})
	}
	epFavs := []map[string]any{}
	efs, err := q.ExportEpisodeFavorites(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, e := range efs {
		epFavs = append(epFavs, map[string]any{"tmdb_id": e.TmdbID, "season": e.Season, "episode": e.Episode,
			"added_at": rw.ISOTime(e.AddedAt), "source": e.Source})
	}
	epRatings := []map[string]any{}
	ers, err := q.ExportEpisodeRatings(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, e := range ers {
		epRatings = append(epRatings, map[string]any{"tmdb_id": e.TmdbID, "season": e.Season, "episode": e.Episode,
			"rating": e.Rating, "rated_at": rw.ISOTime(e.RatedAt), "source": e.Source})
	}
	history := []map[string]any{}
	hs, err := q.ExportHistory(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, h := range hs {
		history = append(history, map[string]any{"media_type": h.MediaType, "tmdb_id": h.TmdbID, "title": h.Title,
			"season": h.Season, "episode": h.Episode, "watched_at": rw.ISOTime(h.WatchedAt), "source": h.Source})
	}
	items := map[int32][]ref{}
	lis, err := q.ExportListItems(ctx, &uid)
	if err != nil {
		return nil, err
	}
	for _, li := range lis {
		items[li.ListID] = append(items[li.ListID], ref{li.MediaType, li.TmdbID})
	}
	lists := []map[string]any{}
	ls, err := q.ExportLists(ctx, &uid)
	if err != nil {
		return nil, err
	}
	for _, l := range ls {
		its := items[l.ID]
		if its == nil {
			its = []ref{}
		}
		// filters stays the stored JSON text, as the Python exported it.
		lists = append(lists, map[string]any{"name": l.Name, "kind": l.Kind, "description": l.Description,
			"filters": l.FiltersJson, "items": its})
	}
	return map[string]any{
		"exported_at": rw.ISOTime(time.Now()), "user": username, "watchlist": watchlist, "ratings": ratings,
		"favorites": favorites, "episode_favorites": epFavs, "episode_ratings": epRatings, "history": history,
		"lists": lists,
	}, nil
}
