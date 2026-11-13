// Package lists is the Lists tab: watchlist, favourites and ratings as
// collections, the person's own lists (and smart lists), the install's
// curated and MDBList lists, stars and Home pins, list items and their
// order, and MDBList explore, preview, follow and sync
// (app/api/v1/lists.py, app/services/lists.py, mdblist_copy.py).
package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
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

// listPath is the list a path names: positive, within the database's ids.
// (Huma fills path parameters of the input struct's own fields only, not
// of an embedded struct's: inputs with more than this repeat the field.)
type listPath struct {
	ListID int32 `path:"list_id" exclusiveMinimum:"0" maximum:"2000000000"`
}

// Register adds the routes.
func Register(d *rw.Deps) {
	r := &routes{d: d}
	r.rows, r.lists, r.home = rows.ForDeps(d)
	h := d.Huma()
	const tag = "lists"

	huma.Register(h, rw.Op("all_lists_api_v1_lists_get", http.MethodGet, "/api/v1/lists", "All Lists", tag), r.all)
	huma.Register(h, rw.Op("create_list_api_v1_lists_post", http.MethodPost, "/api/v1/lists", "Create List", tag), r.create)
	huma.Register(h, rw.Op("create_smart_api_v1_lists_smart_post", http.MethodPost, "/api/v1/lists/smart", "Create Smart", tag), r.createSmart)
	huma.Register(h, rw.Op("get_list_api_v1_lists__list_id__get", http.MethodGet, "/api/v1/lists/{list_id}", "Get List", tag), r.get)
	huma.Register(h, rw.Op("patch_list_api_v1_lists__list_id__patch", http.MethodPatch, "/api/v1/lists/{list_id}", "Patch List", tag,
		"Rename (own lists), show on Home, star."), r.patch)
	huma.Register(h, rw.Op("delete_list_api_v1_lists__list_id__delete", http.MethodDelete, "/api/v1/lists/{list_id}", "Delete List", tag), r.delete)
	huma.Register(h, rw.Op("add_item_api_v1_lists__list_id__items_post", http.MethodPost, "/api/v1/lists/{list_id}/items", "Add Item", tag), r.addItem)
	huma.Register(h, rw.Op("remove_item_api_v1_lists__list_id__items__media___tmdb_id__delete", http.MethodDelete,
		"/api/v1/lists/{list_id}/items/{media}/{tmdb_id}", "Remove Item", tag), r.removeItem)
	huma.Register(h, rw.Op("copy_list_api_v1_lists__list_id__copy_post", http.MethodPost, "/api/v1/lists/{list_id}/copy", "Copy List", tag,
		"An editable copy of any list, as one of the person's own."), r.copy)
	huma.Register(h, rw.Op("sync_list_api_v1_lists__list_id__sync_post", http.MethodPost, "/api/v1/lists/{list_id}/sync", "Sync List", tag), r.sync)
	huma.Register(h, rw.Op("personal_collection_api_v1_collection__kind__get", http.MethodGet, "/api/v1/collection/{kind}",
		"Personal Collection", tag, "Watchlist / favourites / ratings as a grid."), r.collection)
	huma.Register(h, rw.Op("mdblist_explore_api_v1_mdblist_lists_get", http.MethodGet, "/api/v1/mdblist/lists", "Mdblist Explore", tag,
		"No query: MDBList's top lists. A link or list id: that list. Any other text: list search. Answered from\n"+
			"Movie Night's own copy while MDBList can't."), r.explore)
	huma.Register(h, rw.Op("mdblist_preview_api_v1_mdblist_lists__mdblist_id__get", http.MethodGet, "/api/v1/mdblist/lists/{mdblist_id}",
		"Mdblist Preview", tag, "A list before following it: details and its titles as cards."), r.preview)
	huma.Register(h, rw.Op("mdblist_follow_api_v1_mdblist_follow_post", http.MethodPost, "/api/v1/mdblist/follow", "Mdblist Follow", tag,
		"Add an MDBList list to the install's MDBList lists (kept in step daily)."), r.follow)

	d.Limit("ListIn", "name", rw.NameChars)
	d.Limit("ListIn", "description", rw.TextChars)
	d.Limit("SmartIn", "name", rw.NameChars)
	d.Limit("SmartFilters", "sort", rw.NameChars)
	d.Limit("ListPatch", "name", rw.NameChars)
	d.Limit("ListPatch", "description", rw.TextChars)
	d.Limit("FollowIn", "ref", rw.NameChars)
	d.LimitQuery(http.MethodGet, "/api/v1/mdblist/lists", "q", rw.NameChars)
}

func (r *routes) visible(ctx context.Context, userID, listID int32) (*store.List, error) {
	l, err := r.lists.GetVisible(ctx, userID, listID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return l, nil
}

// ---------------------------------------------------------------------------
// The Lists tab
// ---------------------------------------------------------------------------

// collectionTile is a personal collection's size and its first five posters.
func (r *routes) collectionTile(ctx context.Context, u store.User, kind string) (schema.PersonalCollection, error) {
	keys, total, err := r.rows.PersonalKeys(ctx, u, kind, "", 5, 0)
	if err != nil {
		return schema.PersonalCollection{}, err
	}
	metas, err := r.d.Titles.Ensure(ctx, keys, 5)
	if err != nil {
		return schema.PersonalCollection{}, err
	}
	posters := []string{}
	for _, k := range keys {
		if m := metas[k]; m != nil && m.PosterPath != nil {
			posters = append(posters, *m.PosterPath)
		}
	}
	return schema.PersonalCollection{Count: total, Posters: posters}, nil
}

func (r *routes) all(ctx context.Context, _ *struct{}) (*rw.Body[schema.ListsOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	u := &id.User
	out, missing, err := r.lists.Summaries(ctx, u)
	if err == nil && len(missing) > 0 {
		// The first view of a list: fetch its collage posters, then redo.
		if _, err = r.d.Titles.Ensure(ctx, missing, 40); err == nil {
			out, _, err = r.lists.Summaries(ctx, u)
		}
	}
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	res := schema.ListsOut{Lists: out}
	for kind, dst := range map[string]*schema.PersonalCollection{"watchlist": &res.Watchlist, "favorites": &res.Favorites, "ratings": &res.Ratings} {
		if *dst, err = r.collectionTile(ctx, *u, kind); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	return rw.Out(res), nil
}

func (r *routes) create(ctx context.Context, in *rw.Body[schema.ListIn]) (*rw.Body[schema.CreatedOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.lists.CreateList(ctx, id.User.ID, in.Body.Name, in.Body.Description)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.CreatedOut{Ok: true, ID: l.ID}), nil
}

// filtersMap is the filters as the plain map the lists service cleans.
func filtersMap(f schema.SmartFilters) map[string]any {
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (r *routes) createSmart(ctx context.Context, in *rw.Body[schema.SmartIn]) (*rw.Body[schema.CreatedOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.lists.CreateSmart(ctx, id.User.ID, in.Body.Name, filtersMap(in.Body.Filters))
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.CreatedOut{Ok: true, ID: l.ID}), nil
}

func originURL(l *store.List) *string {
	if l.Origin != nil && *l.Origin == "mdblist" && l.OriginUser != nil && l.OriginSlug != nil {
		u := fmt.Sprintf("https://mdblist.com/lists/%s/%s", *l.OriginUser, *l.OriginSlug)
		return &u
	}
	return nil
}

type getListIn struct {
	ListID int32  `path:"list_id" exclusiveMinimum:"0" maximum:"2000000000"`
	Page   int    `query:"page" default:"1" minimum:"1"`
	Media  string `query:"media" enum:"movie,tv"`
	Sort   string `query:"sort" default:"list" enum:"list,added,title,year,rating"`
}

func (r *routes) flags(ctx context.Context, userID, listID int32) (pinned, starred bool, err error) {
	pins, err := r.home.Pinned(ctx, userID)
	if err != nil {
		return false, false, err
	}
	stars, err := r.lists.Starred(ctx, userID)
	if err != nil {
		return false, false, err
	}
	return slices.Contains(pins, listID), stars[listID], nil
}

func (r *routes) get(ctx context.Context, in *getListIn) (*rw.Body[schema.ListOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	page, err := r.rows.ListPage(ctx, l, in.Page, rows.PageSize, in.Media, in.Sort)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	items, err := r.rows.Cards(ctx, id.User, page.Metas)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	counts, err := r.lists.Counts(ctx, []int32{l.ID})
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	pinned, starred, err := r.flags(ctx, id.User.ID, l.ID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	c := counts[l.ID]
	out := schema.ListOut{
		ID: l.ID, Name: l.Name, Description: deref(l.Description), Kind: l.Kind, Source: l.Source, Origin: l.Origin,
		Group: homecfg.ListGroup(l), By: homecfg.ListByLine(l), OriginURL: originURL(l), Likes: l.Likes,
		Count: lists.CountOf(l, c), Movies: c.Movies, Shows: c.Shows, Pinned: pinned, Starred: starred,
		Editable: lists.CanEditItems(l, id.User.ID), Deletable: lists.CanDelete(l, &id.User),
		Sort: in.Sort, Items: items, More: page.More, Page: in.Page,
	}
	if l.NotShown != nil {
		out.NotShown = int(*l.NotShown)
	}
	if l.FiltersJson != nil {
		if err := json.Unmarshal([]byte(*l.FiltersJson), &out.Filters); err != nil {
			return nil, r.d.Fail(ctx, fmt.Errorf("list %d's filters: %w", l.ID, err))
		}
	}
	if l.SyncedAt != nil {
		s := l.SyncedAt.UTC().Format("2006-01-02T15:04:05.000000-07:00")
		out.SyncedAt = &s
	}
	if in.Media != "" {
		m := schema.MediaType(in.Media)
		out.Media = &m
	}
	if l.Kind == "smart" {
		// A smart list is TMDB's discover: it stops at TMDB's 500 pages.
		out.Note = rows.EndNote(in.Page, len(items), page.More)
	}
	return rw.Out(out), nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r *routes) patch(ctx context.Context, in *struct {
	ListID int32 `path:"list_id" exclusiveMinimum:"0" maximum:"2000000000"`
	Body   schema.ListPatch
}) (*rw.Body[schema.ListFlagsOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if b.Name != nil || b.Description != nil {
		if l.OwnerID == nil || *l.OwnerID != id.User.ID {
			return nil, rw.Err(http.StatusConflict, "Shared and MDBList lists keep their own names")
		}
		if err := r.lists.Rename(ctx, l, b.Name, b.Description); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	if b.Pinned != nil {
		if err := r.home.SetPinned(ctx, id.User.ID, l.ID, *b.Pinned); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	if b.Starred != nil {
		if err := r.lists.SetStar(ctx, id.User.ID, l.ID, *b.Starred); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	pinned, starred, err := r.flags(ctx, id.User.ID, l.ID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.ListFlagsOut{Ok: true, Pinned: pinned, Starred: starred}), nil
}

func (r *routes) delete(ctx context.Context, in *listPath) (*rw.Body[schema.Ok], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	if !lists.CanDelete(l, &id.User) {
		return nil, rw.Err(http.StatusForbidden, "Only the install's admins can remove a shared or MDBList list")
	}
	if err := r.lists.Delete(ctx, l.ID); err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.Ok{Ok: true}), nil
}

func (r *routes) addItem(ctx context.Context, in *struct {
	ListID int32 `path:"list_id" exclusiveMinimum:"0" maximum:"2000000000"`
	Body   schema.ListItemIn
}) (*rw.Body[schema.AddedOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	if !lists.CanEditItems(l, id.User.ID) {
		return nil, rw.Err(http.StatusConflict, "Curated lists are fixed; add it to one of your own lists")
	}
	added, err := r.lists.AddItem(ctx, l.ID, string(in.Body.MediaType), in.Body.TMDBID)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.AddedOut{Ok: true, Added: added}), nil
}

func (r *routes) removeItem(ctx context.Context, in *struct {
	ListID int32  `path:"list_id" exclusiveMinimum:"0" maximum:"2000000000"`
	Media  string `path:"media" enum:"movie,tv"`
	TMDBID int64  `path:"tmdb_id" exclusiveMinimum:"0" maximum:"10000000000"`
}) (*rw.Body[schema.Ok], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	if !lists.CanEditItems(l, id.User.ID) {
		return nil, rw.Err(http.StatusConflict, "Curated lists are fixed")
	}
	if in.TMDBID <= 2147483647 { // an id past the column's range is in no list
		if err := r.lists.RemoveItem(ctx, l.ID, in.Media, int(in.TMDBID)); err != nil {
			return nil, r.d.Fail(ctx, err)
		}
	}
	return rw.Out(schema.Ok{Ok: true}), nil
}

func (r *routes) copy(ctx context.Context, in *listPath) (*rw.Body[schema.CreatedOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	if l.Kind != "list" {
		return nil, rw.Err(http.StatusConflict, "Smart lists run their filters live; there is nothing to copy")
	}
	mine, err := r.lists.CopyToMine(ctx, id.User.ID, l)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.CreatedOut{Ok: true, ID: mine.ID}), nil
}

// mdbError turns an MDBList failure into the answer: a plain-words error
// as it is, MDBList refusing (its own limit for the key) as 503 with
// refused, anything else as 502 (logged).
func (r *routes) mdbError(ctx context.Context, err error, refused string) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	var no *clients.MDBListRefused
	if errors.As(err, &no) {
		return rw.Err(http.StatusServiceUnavailable, refused)
	}
	r.d.Log.WarnContext(ctx, "MDBList failed", "err", err)
	return rw.Err(http.StatusBadGateway, "MDBList didn't answer — try again")
}

func (r *routes) sync(ctx context.Context, in *listPath) (*rw.Body[schema.CountOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.visible(ctx, id.User.ID, in.ListID)
	if err != nil {
		return nil, err
	}
	// MDBList itself, not the copy: a sync from the copy would only rewrite
	// the list with what it already holds.
	n, err := r.lists.Sync(ctx, r.d.Clients.MDBList, l)
	if err != nil {
		return nil, r.mdbError(ctx, err, clients.MDBListRefusedText+". The titles already here are kept; try again later")
	}
	return rw.Out(schema.CountOut{Ok: true, Count: n}), nil
}

var collectionNames = map[string]string{"watchlist": "Watchlist", "favorites": "Favorites", "rated": "Your top rated", "ratings": "Your ratings"}

func (r *routes) collection(ctx context.Context, in *struct {
	Kind  string `path:"kind" enum:"watchlist,favorites,rated,ratings"`
	Page  int    `query:"page" default:"1" minimum:"1"`
	Media string `query:"media" enum:"movie,tv"`
}) (*rw.Body[schema.CollectionOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	page, total, err := r.rows.PersonalPage(ctx, id.User, in.Kind, in.Media, in.Page, rows.PageSize)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	items, err := r.rows.Cards(ctx, id.User, page.Metas)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.CollectionOut{Name: collectionNames[in.Kind], Count: total, Items: items, More: page.More, Page: in.Page}), nil
}

// ---------------------------------------------------------------------------
// MDBList: explore, preview, follow
// ---------------------------------------------------------------------------

// mdb is MDBList's lists with Movie Night's copy answering while MDBList
// can't, including when the install has no MDBList key (any more); nil
// when there is neither a key nor a copy.
func (r *routes) mdb(ctx context.Context) (*lists.WithCopy, error) {
	c := r.d.Clients.MDBList
	if !c.Enabled() {
		has, err := r.lists.HasCopy(ctx)
		if err != nil {
			return nil, err
		}
		if !has {
			return nil, nil
		}
	}
	return r.lists.WithCopy(c), nil
}

func summaryOut(s clients.JSON, followed map[string]int32) schema.MDBListSummary {
	out := schema.MDBListSummary{}
	out.Name, _ = s["name"].(string)
	out.User, _ = s["user"].(string)
	out.Description, _ = s["description"].(string)
	out.Dynamic, _ = s["dynamic"].(bool)
	if slug, ok := s["slug"].(string); ok {
		out.Slug = &slug
	}
	if m, ok := s["media"].(string); ok && (m == "movie" || m == "tv") {
		mt := schema.MediaType(m)
		out.Media = &mt
	}
	num := func(k string) *int {
		if f, ok := s[k].(float64); ok {
			n := int(f)
			return &n
		}
		return nil
	}
	out.MDBListID, out.ItemCount, out.Likes = num("mdblist_id"), num("item_count"), num("likes")
	if out.MDBListID != nil {
		if id, ok := followed[fmt.Sprint(*out.MDBListID)]; ok {
			out.FollowedID = &id
		}
	}
	return out
}

func (r *routes) mark(ctx context.Context, found []clients.JSON) ([]schema.MDBListSummary, error) {
	followed, err := r.lists.FollowedIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]schema.MDBListSummary, 0, len(found))
	for _, s := range found {
		out = append(out, summaryOut(normalizeJSON(s), followed))
	}
	return out, nil
}

// normalizeJSON makes numbers float64 whatever built the map (the client's
// summary, or the copy read back), so one reader serves both.
func normalizeJSON(s clients.JSON) clients.JSON {
	b, _ := json.Marshal(s)
	var out clients.JSON
	_ = json.Unmarshal(b, &out)
	return out
}

const (
	mdbSearchTTL = time.Hour
	mdbTopTTL    = 6 * time.Hour
	mdbListTTL   = time.Hour
	previewFetch = 60 // titles of a preview fetched from TMDB now; the rest show from the cache
)

func (r *routes) explore(ctx context.Context, in *struct {
	Q string `query:"q" default:"" maxLength:"300"`
}) (*rw.Body[schema.MDBListExploreOut], error) {
	if _, err := r.d.User(ctx); err != nil {
		return nil, err
	}
	mdb, err := r.mdb(ctx)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	if mdb == nil {
		// No key and no copy: an empty page, no notice.
		return rw.Out(schema.MDBListExploreOut{Mode: "browse", Results: []schema.MDBListSummary{}, Enabled: true}), nil
	}
	q := strings.TrimSpace(in.Q)
	mode := "browse"
	var found []clients.JSON
	ref, isRef := clients.ParseListURL(q)
	switch {
	case isRef:
		mode = "link"
		summary, gerr := mdb.GetList(ctx, ref)
		if gerr == nil && summary == nil {
			msg := "That MDBList list wasn't found — it may be private."
			return rw.Out(schema.MDBListExploreOut{Mode: mode, Results: []schema.MDBListSummary{}, Error: &msg, Enabled: true}), nil
		}
		found, err = []clients.JSON{summary}, gerr
	case q != "":
		mode = "search"
		found, err = cache.Cached(ctx, r.d.Cache, cache.Key(true, "mdblist", "search", q), cache.Options{TTL: mdbSearchTTL},
			func(ctx context.Context) ([]clients.JSON, error) { return mdb.SearchLists(ctx, q) })
	default:
		found, err = cache.Cached(ctx, r.d.Cache, "mdblist:top", cache.Options{TTL: mdbTopTTL}, mdb.TopLists)
	}
	if err != nil {
		var no *clients.MDBListRefused
		if errors.As(err, &no) {
			msg := clients.MDBListRefusedText + ", and Movie Night has no copy of this yet. Try again later."
			return rw.Out(schema.MDBListExploreOut{Mode: "browse", Results: []schema.MDBListSummary{}, Error: &msg, Enabled: true}), nil
		}
		r.d.Log.WarnContext(ctx, "MDBList explore failed", "err", err)
		return nil, rw.Err(http.StatusBadGateway, "MDBList didn't answer — try again")
	}
	results, err := r.mark(ctx, found)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.MDBListExploreOut{Mode: mode, Results: results, Enabled: true}), nil
}

// listItems is a list's titles as cached.
type listItems struct {
	Items  []clients.ListItem
	NoTMDB int
}

func (r *routes) preview(ctx context.Context, in *struct {
	MDBListID int32 `path:"mdblist_id" exclusiveMinimum:"0" maximum:"2000000000"`
}) (*rw.Body[schema.MDBListPreviewOut], error) {
	id, err := r.d.User(ctx)
	if err != nil {
		return nil, err
	}
	mdb, err := r.mdb(ctx)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	if mdb == nil {
		return nil, rw.Err(http.StatusServiceUnavailable, "MDBList isn't set up on this install")
	}
	listID := fmt.Sprint(in.MDBListID)
	refused := clients.MDBListRefusedText + ", and Movie Night has no copy of this list yet. Try again later"
	// Both kept an hour; the preview has every title of the list (docs/limits.md).
	summary, err := cache.Cached(ctx, r.d.Cache, "mdblist:list:"+listID, cache.Options{TTL: mdbListTTL},
		func(ctx context.Context) (clients.JSON, error) { return mdb.GetList(ctx, clients.ListRef{ID: listID}) })
	if err != nil {
		return nil, r.mdbError(ctx, err, refused)
	}
	if summary == nil {
		return nil, rw.NotFound("That MDBList list wasn't found — it may be private.")
	}
	got, err := cache.Cached(ctx, r.d.Cache, "mdblist:items:"+listID, cache.Options{TTL: mdbListTTL},
		func(ctx context.Context) (listItems, error) {
			items, no, err := mdb.ListItems(ctx, listID)
			return listItems{items, no}, err
		})
	if err != nil {
		return nil, r.mdbError(ctx, err, refused)
	}
	var keys []string
	var names []*titles.Meta
	seen := map[string]bool{}
	for _, e := range got.Items {
		k := titles.Key(e.Media, e.TMDBID)
		if seen[k] || e.TMDBID <= 0 {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
		names = append(names, &titles.Meta{Key: k, MediaType: e.Media, TMDBID: e.TMDBID, Title: e.Title})
	}
	r.d.Titles.Remember(ctx, names)
	metas, err := r.d.Titles.Ensure(ctx, keys[:min(len(keys), previewFetch)], previewFetch)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	if len(keys) > previewFetch {
		rest, err := r.d.Titles.GetMany(ctx, keys[previewFetch:])
		if err != nil {
			return nil, r.d.Fail(ctx, err)
		}
		for k, m := range rest {
			metas[k] = m
		}
	}
	ordered := make([]*titles.Meta, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, metas[k])
	}
	items, err := r.rows.Cards(ctx, id.User, ordered)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	marked, err := r.mark(ctx, []clients.JSON{summary})
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	return rw.Out(schema.MDBListPreviewOut{List: marked[0], Count: len(keys), Items: items, NotShown: got.NoTMDB}), nil
}

func (r *routes) follow(ctx context.Context, in *rw.Body[schema.FollowIn]) (*rw.Body[schema.FollowOut], error) {
	// Anyone signed in may follow a list, for everyone on the install; it
	// is then synced every night. No allowance (docs/limits.md).
	if _, err := r.d.User(ctx); err != nil {
		return nil, err
	}
	ref, ok := clients.ParseListURL(in.Body.Ref)
	if !ok {
		return nil, rw.Err(http.StatusUnprocessableEntity, "Paste an mdblist.com/lists/… link or a list number")
	}
	mdb, err := r.mdb(ctx)
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	if mdb == nil {
		return nil, rw.Err(http.StatusServiceUnavailable, "MDBList isn't set up on this install")
	}
	l, err := r.lists.Follow(ctx, mdb, ref)
	if err != nil {
		return nil, r.mdbError(ctx, err, clients.MDBListRefusedText+", and Movie Night has no copy of this list yet. Try again later")
	}
	counts, err := r.lists.Counts(ctx, []int32{l.ID})
	if err != nil {
		return nil, r.d.Fail(ctx, err)
	}
	c := counts[l.ID]
	return rw.Out(schema.FollowOut{Ok: true, ID: l.ID, Count: c.Movies + c.Shows}), nil
}
