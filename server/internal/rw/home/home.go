// Package home is the Home tabs: which rows each tab shows (and the Home
// rows settings), each row's cards, the hero banner, the "See all" grids
// behind the rows, Discover, and the genre and provider lists
// (app/api/v1/home.py, app/services/rows.py, app/services/home_config.py).
//
// Every handler loads the person's state once, for the titles it is about
// to show (rows.Loader.Cards / userstate.Load): the Python reloaded their
// whole watchlist, history, ratings and favourites for every row.
package home

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/homecfg"
	"movienight/server/internal/lists"
	"movienight/server/internal/rows"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

type routes struct {
	d     *rw.Deps
	rows  *rows.Loader
	lists *lists.Service
	home  *homecfg.Service
}

// Register adds the routes.
func Register(d *rw.Deps) {
	r := &routes{d: d}
	r.rows, r.lists, r.home = rows.ForDeps(d)
	h := d.Huma()
	const tag = "home"
	huma.Register(h, rw.Op("home_api_v1_home_get", http.MethodGet, "/api/v1/home", "Home", tag,
		"The tab's enabled rows, in order; the page then loads each one."), r.homeTab)
	huma.Register(h, rw.Op("hero_api_v1_hero_get", http.MethodGet, "/api/v1/hero", "Hero", tag), r.hero)
	huma.Register(h, rw.Op("row_api_v1_rows__key__get", http.MethodGet, "/api/v1/rows/{key}", "Row", tag), r.row)
	huma.Register(h, rw.Op("browse_api_v1_browse_get", http.MethodGet, "/api/v1/browse", "Browse", tag,
		`Grid behind a row's "See all", one page at a time.`), r.browse)
	huma.Register(h, rw.Op("discover_api_v1_discover_get", http.MethodGet, "/api/v1/discover", "Discover", tag,
		"Filtered TMDB browsing (the Discover tab); the same filters make a smart list."), r.discover)
	huma.Register(h, rw.Op("get_config_api_v1_home_config_get", http.MethodGet, "/api/v1/home/config", "Get Config", tag), r.getConfig)
	huma.Register(h, rw.Op("put_config_api_v1_home_config_put", http.MethodPut, "/api/v1/home/config", "Put Config", tag), r.putConfig)
	huma.Register(h, rw.Op("reset_config_api_v1_home_config_reset_post", http.MethodPost, "/api/v1/home/config/reset", "Reset Config", tag), r.resetConfig)
	huma.Register(h, rw.Op("genres_api_v1_genres_get", http.MethodGet, "/api/v1/genres", "Genres", tag), genres)
	huma.Register(h, rw.Op("providers_api_v1_providers_get", http.MethodGet, "/api/v1/providers", "Providers", tag), providers)
	d.Limit("HomeConfigIn", "rows", rw.BatchItems)
	d.LimitQuery(http.MethodGet, "/api/v1/discover", "sort", rw.NameChars)
}

// tabIn is a Home tab.
type tabIn struct {
	Tab string `query:"tab" default:"for_you" enum:"for_you,movies,shows"`
}

func (r *routes) homeTab(ctx context.Context, in *tabIn) (*rw.Body[schema.HomeOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	visible, err := r.home.VisibleLists(ctx, id.User.ID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	cfg, err := r.home.Config(ctx, id.User.ID, in.Tab, visible)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	names := homecfg.Names(visible)
	out := schema.HomeOut{Tab: in.Tab, Rows: []schema.HomeRowSpec{}}
	for _, row := range cfg {
		if !row.On {
			continue
		}
		style := "poster"
		if row.Key == "continue" {
			style = "landscape"
		}
		out.Rows = append(out.Rows, schema.HomeRowSpec{Key: row.Key, Title: homecfg.RowTitle(in.Tab, row.Key, names),
			Multi: row.Key == "because" || row.Key == "ai", Style: style})
	}
	return rw.Out(out), nil
}

func (r *routes) hero(ctx context.Context, in *tabIn) (*rw.Body[schema.CardsOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	items, err := r.heroCards(ctx, &id.User, in.Tab)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.CardsOut{Items: items}), nil
}

func (r *routes) row(ctx context.Context, in *struct {
	Key string `path:"key"`
	Tab string `query:"tab" default:"for_you" enum:"for_you,movies,shows"`
}) (*rw.Body[schema.RowsOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	built, note, err := r.buildRows(ctx, &id.User, in.Tab, in.Key)
	if errors.Is(err, rows.ErrTMDBDown) {
		// TMDB couldn't send this row and no copy is kept: the row says so.
		n := rows.TMDBDownRow
		return rw.Out(schema.RowsOut{Rows: []schema.Row{}, Note: &n}), nil
	}
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.RowsOut{Rows: built, Note: note}), nil
}

func (r *routes) browse(ctx context.Context, in *struct {
	Key  string `query:"key" required:"true" pattern:"^[a-z_]+(:[a-z0-9:]+)?$" maxLength:"60"`
	Tab  string `query:"tab" default:"for_you" enum:"for_you,movies,shows"`
	Page int    `query:"page" default:"1" minimum:"1"`
}) (*rw.Body[schema.PageOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	page, err := r.rows.Load(ctx, id.User, in.Tab, in.Key, in.Page, rows.PageSize, "", "list")
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	items, err := r.rows.Cards(ctx, id.User, page.Metas)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	visible, err := r.home.VisibleLists(ctx, id.User.ID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	title := homecfg.RowTitle(in.Tab, in.Key, homecfg.Names(visible))
	out := schema.PageOut{Title: &title, Items: items, More: page.More, Page: in.Page}
	if kind, _, _ := strings.Cut(in.Key, ":"); rows.TMDBKinds[kind] {
		out.Note = rows.EndNote(in.Page, len(items), page.More)
	}
	return rw.Out(out), nil
}

type discoverIn struct {
	Media    string `query:"media" default:"movie" enum:"movie,tv"`
	Genre    string `query:"genre" pattern:"^\\d{1,6}$"`
	Provider string `query:"provider" pattern:"^\\d{1,6}$"`
	// Huma takes no pointer query parameters: 0 is "not given" (no year is
	// 0, and a minimum rating of 0 filters nothing, as in the Python).
	YearFrom  int     `query:"year_from" minimum:"1870" maximum:"2100"`
	YearTo    int     `query:"year_to" minimum:"1870" maximum:"2100"`
	MinRating float64 `query:"min_rating" minimum:"0" maximum:"10"`
	Language  string  `query:"language" pattern:"^[a-zA-Z]{2,3}$"`
	Sort      string  `query:"sort" maxLength:"300"`
	Page      int     `query:"page" default:"1" minimum:"1"`
}

func (r *routes) discover(ctx context.Context, in *discoverIn) (*rw.Body[schema.PageOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	raw := map[string]any{"media": in.Media, "genre": in.Genre, "provider": in.Provider, "language": in.Language, "sort": in.Sort}
	if in.YearFrom != 0 {
		raw["year_from"] = in.YearFrom
	}
	if in.YearTo != 0 {
		raw["year_to"] = in.YearTo
	}
	if in.MinRating != 0 {
		raw["min_rating"] = in.MinRating
	}
	f := lists.CleanFilters(raw)
	media := f["media"].(string)
	results, err := r.rows.Discover(ctx, media, in.Page, lists.TMDBFilters(f, r.d.Cfg.WatchRegion))
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	items, err := r.rows.Cards(ctx, id.User, r.rows.Remember(ctx, results, media))
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	more := rows.TMDBMore(len(results), in.Page)
	return rw.Out(schema.PageOut{Items: items, More: more, Page: in.Page, Note: rows.EndNote(in.Page, len(items), more)}), nil
}

// ---------------------------------------------------------------------------
// The Home rows screen
// ---------------------------------------------------------------------------

func (r *routes) configOut(ctx context.Context, userID int32, tab string) (schema.HomeConfigOut, error) {
	visible, err := r.home.VisibleLists(ctx, userID)
	if err != nil {
		return schema.HomeConfigOut{}, err
	}
	byID := make(map[int32]*store.List, len(visible))
	for i := range visible {
		byID[visible[i].ID] = &visible[i]
	}
	names := homecfg.Names(visible)
	cfg, err := r.home.Config(ctx, userID, tab, visible)
	if err != nil {
		return schema.HomeConfigOut{}, err
	}
	out := schema.HomeConfigOut{Tab: tab, Rows: make([]schema.HomeConfigRow, 0, len(cfg))}
	for _, row := range cfg {
		title := homecfg.RowTitle(tab, row.Key, names)
		e := schema.HomeConfigRow{Key: row.Key, On: row.On, Title: &title}
		if rest, ok := strings.CutPrefix(row.Key, "list:"); ok {
			t := true
			e.List = &t
			var lid int32
			fmt.Sscan(rest, &lid)
			if l := byID[lid]; l != nil {
				by, group := homecfg.ListByLine(l), homecfg.ListGroup(l)
				e.Subtitle, e.Group = &by, &group
			}
		}
		out.Rows = append(out.Rows, e)
	}
	return out, nil
}

func (r *routes) getConfig(ctx context.Context, in *tabIn) (*rw.Body[schema.HomeConfigOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	out, err := r.configOut(ctx, id.User.ID, in.Tab)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(out), nil
}

func (r *routes) putConfig(ctx context.Context, in *struct {
	Tab  string `query:"tab" default:"for_you" enum:"for_you,movies,shows"`
	Body schema.HomeConfigIn
}) (*rw.Body[schema.HomeConfigOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	rowsIn := make([]homecfg.Row, len(in.Body.Rows))
	for i, x := range in.Body.Rows {
		rowsIn[i] = homecfg.Row{Key: x.Key, On: x.On}
	}
	if err := r.home.Save(ctx, id.User.ID, in.Tab, rowsIn); err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	out, err := r.configOut(ctx, id.User.ID, in.Tab)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(out), nil
}

func (r *routes) resetConfig(ctx context.Context, in *struct {
	Tab string `query:"tab" default:"for_you" enum:"for_you,movies,shows,all"`
}) (*rw.Body[schema.HomeConfigOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	tabs, show := []string{in.Tab}, in.Tab
	if in.Tab == "all" {
		tabs, show = homecfg.Tabs, "for_you"
	}
	for _, t := range tabs {
		if err := r.home.Reset(ctx, id.User.ID, t); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	out, err := r.configOut(ctx, id.User.ID, show)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(out), nil
}

func genres(_ context.Context, in *struct {
	Media string `query:"media" default:"movie" enum:"movie,tv"`
}) (*rw.Body[schema.GenresOut], error) {
	table := titles.Genres(in.Media)
	out := make([]schema.Genre, 0, len(table))
	for id, name := range table {
		out = append(out, schema.Genre{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return rw.Out(schema.GenresOut{Genres: out}), nil
}

func providers(_ context.Context, _ *struct{}) (*rw.Body[schema.ProvidersOut], error) {
	out := make([]schema.Provider, len(homecfg.Providers))
	for i, p := range homecfg.Providers {
		out[i] = schema.Provider{ID: p.ID, Name: p.Name}
	}
	return rw.Out(schema.ProvidersOut{Providers: out}), nil
}
